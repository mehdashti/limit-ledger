// Package store keeps limit-ledger's numbers in a local SQLite database. It holds
// token counts, limit anchors and file read positions only, never content.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mehdashti/limit-ledger/internal/transcript"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, no cgo
)

// migrations[i] takes the schema from version i to i+1.
var migrations = []string{schemaV1, schemaV2, schemaV3, schemaV4, schemaV5}

// schemaV5 remembers which change alerts were already raised, so each is raised once.
const schemaV5 = `
CREATE TABLE alerts (
	key     TEXT PRIMARY KEY,
	ts      INTEGER NOT NULL,
	message TEXT NOT NULL
) WITHOUT ROWID;
`

// schemaV4 adds the per-account switch policy and the history of weight fits.
const schemaV4 = `
ALTER TABLE accounts ADD COLUMN switch_full INTEGER NOT NULL DEFAULT 0;

CREATE TABLE fits (
	ts           INTEGER NOT NULL,
	plan         TEXT NOT NULL,
	since_ts     INTEGER NOT NULL,
	observations INTEGER NOT NULL,
	w_input      REAL NOT NULL,
	w_cache_write REAL NOT NULL,
	w_cache_read REAL NOT NULL,
	w_output     REAL NOT NULL,
	per_percent  REAL NOT NULL,
	spread       REAL NOT NULL,
	PRIMARY KEY (ts, plan)
) WITHOUT ROWID;
`

// schemaV3 adds utilization samples from the statusline hook.
const schemaV3 = `
CREATE TABLE samples (
	ts          INTEGER NOT NULL,
	session_key TEXT NOT NULL,
	five_pct    REAL,
	five_reset  INTEGER,
	seven_pct   REAL,
	seven_reset INTEGER,
	version     TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (ts, session_key)
) WITHOUT ROWID;
`

const schemaV1 = `
CREATE TABLE responses (
	msg_id     TEXT PRIMARY KEY,
	ts         INTEGER NOT NULL,
	model      TEXT NOT NULL,
	speed      TEXT NOT NULL DEFAULT '',
	input      INTEGER NOT NULL DEFAULT 0,
	cache_5m   INTEGER NOT NULL DEFAULT 0,
	cache_1h   INTEGER NOT NULL DEFAULT 0,
	cache_read INTEGER NOT NULL DEFAULT 0,
	output     INTEGER NOT NULL DEFAULT 0,
	thinking   INTEGER NOT NULL DEFAULT 0,
	web_search INTEGER NOT NULL DEFAULT 0,
	web_fetch  INTEGER NOT NULL DEFAULT 0,
	sidechain  INTEGER NOT NULL DEFAULT 0,
	kind       TEXT NOT NULL DEFAULT 'main'
) WITHOUT ROWID;
CREATE INDEX responses_ts ON responses(ts);

CREATE TABLE anchors (
	kind      TEXT NOT NULL,
	hit_ts    INTEGER NOT NULL,
	resets_ts INTEGER NOT NULL,
	PRIMARY KEY (kind, hit_ts, resets_ts)
) WITHOUT ROWID;

CREATE TABLE files (
	path_key TEXT PRIMARY KEY,
	dev      INTEGER NOT NULL,
	ino      INTEGER NOT NULL,
	size     INTEGER NOT NULL,
	offset   INTEGER NOT NULL,
	mtime    INTEGER NOT NULL
) WITHOUT ROWID;

CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
) WITHOUT ROWID;
`

// schemaV2 adds accounts. Version 1 did not read credential_org records, so every
// file is read again from the start; responses are upserts, so nothing is counted twice.
const schemaV2 = `
CREATE TABLE org_events (
	ts          INTEGER NOT NULL,
	account     TEXT NOT NULL,
	session_key TEXT NOT NULL,
	PRIMARY KEY (ts, account, session_key)
) WITHOUT ROWID;

CREATE TABLE accounts (
	account TEXT PRIMARY KEY,
	label   TEXT NOT NULL DEFAULT '',
	plan    TEXT NOT NULL DEFAULT ''
) WITHOUT ROWID;

DELETE FROM files;
`

// Store is an open ledger database.
type Store struct {
	db   *sql.DB
	salt string
}

// Open opens or creates the database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	_ = os.Chmod(path, 0o600)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.loadSalt(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("database schema %d is newer than this build (%d)", v, len(migrations))
	}
	for ; v < len(migrations); v++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration to schema %d: %w", v+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) loadSalt() error {
	err := s.db.QueryRow("SELECT value FROM meta WHERE key='path_salt'").Scan(&s.salt)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	s.salt = hex.EncodeToString(b)
	_, err = s.db.Exec("INSERT INTO meta(key, value) VALUES('path_salt', ?)", s.salt)
	return err
}

// PathKey is the stored identity of a transcript path: a salted hash, so paths are
// not kept in the clear.
func (s *Store) PathKey(rel string) string {
	h := sha256.Sum256([]byte(s.salt + "\x00" + filepath.ToSlash(rel)))
	return hex.EncodeToString(h[:16])
}

// AccountKey is the stored identity of an organization: a salted hash of its UUID.
func (s *Store) AccountKey(org string) string { return s.key("org", org, 6) }

// SessionKey is the stored identity of a session id.
func (s *Store) SessionKey(id string) string { return s.key("session", id, 8) }

func (s *Store) key(kind, v string, n int) string {
	h := sha256.Sum256([]byte(s.salt + "\x00" + kind + "\x00" + v))
	return hex.EncodeToString(h[:n])
}

// FileState is how far a transcript file has been read.
type FileState struct {
	Dev, Ino            uint64
	Size, Offset, Mtime int64
}

// FileStates returns the read state of every known file, keyed by PathKey.
func (s *Store) FileStates() (map[string]FileState, error) {
	rows, err := s.db.Query("SELECT path_key, dev, ino, size, offset, mtime FROM files")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]FileState{}
	for rows.Next() {
		var k string
		var dev, ino int64
		var st FileState
		if err := rows.Scan(&k, &dev, &ino, &st.Size, &st.Offset, &st.Mtime); err != nil {
			return nil, err
		}
		st.Dev, st.Ino = uint64(dev), uint64(ino)
		out[k] = st
	}
	return out, rows.Err()
}

// Batch is one write transaction.
type Batch struct {
	st                   *Store
	tx                   *sql.Tx
	resp, anchor, fileSt *sql.Stmt
	orgEv, account       *sql.Stmt
}

const upsertResponse = `
INSERT INTO responses(msg_id, ts, model, speed, input, cache_5m, cache_1h, cache_read, output,
                      thinking, web_search, web_fetch, sidechain, kind)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(msg_id) DO UPDATE SET
	ts         = min(ts, excluded.ts),
	speed      = CASE WHEN speed = '' THEN excluded.speed ELSE speed END,
	input      = max(input, excluded.input),
	cache_5m   = max(cache_5m, excluded.cache_5m),
	cache_1h   = max(cache_1h, excluded.cache_1h),
	cache_read = max(cache_read, excluded.cache_read),
	output     = max(output, excluded.output),
	thinking   = max(thinking, excluded.thinking),
	web_search = max(web_search, excluded.web_search),
	web_fetch  = max(web_fetch, excluded.web_fetch)`

// Begin starts a write transaction.
func (s *Store) Begin() (*Batch, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	b := &Batch{st: s, tx: tx}
	for _, p := range []struct {
		dst **sql.Stmt
		sql string
	}{
		{&b.resp, upsertResponse},
		{&b.anchor, "INSERT OR IGNORE INTO anchors(kind, hit_ts, resets_ts) VALUES(?,?,?)"},
		{&b.orgEv, "INSERT OR IGNORE INTO org_events(ts, account, session_key) VALUES(?,?,?)"},
		{&b.account, "INSERT OR IGNORE INTO accounts(account) VALUES(?)"},
		{&b.fileSt, `INSERT INTO files(path_key, dev, ino, size, offset, mtime) VALUES(?,?,?,?,?,?)
			ON CONFLICT(path_key) DO UPDATE SET dev=excluded.dev, ino=excluded.ino,
			size=excluded.size, offset=excluded.offset, mtime=excluded.mtime`},
	} {
		st, err := tx.Prepare(p.sql)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		*p.dst = st
	}
	return b, nil
}

// PutResponse inserts a response. A duplicate message id keeps the larger count of
// each kind, because a partial copy of a response can carry zeros.
func (b *Batch) PutResponse(r *transcript.Response) error {
	side := 0
	if r.Sidechain {
		side = 1
	}
	_, err := b.resp.Exec(r.MsgID, r.TS, r.Model, r.Speed, r.Input, r.Cache5m, r.Cache1h, r.CacheRead,
		r.Output, r.Thinking, r.WebSearch, r.WebFetch, side, r.Kind)
	return err
}

// PutAnchor inserts a limit anchor once.
func (b *Batch) PutAnchor(a *transcript.Anchor) error {
	_, err := b.anchor.Exec(a.Kind, a.Hit, a.Resets)
	return err
}

// PutOrgEvent records which account was active at a moment. Only salted hashes of
// the organization and session are stored.
func (b *Batch) PutOrgEvent(e *transcript.OrgEvent) error {
	acct := b.st.AccountKey(e.Org)
	if _, err := b.orgEv.Exec(e.TS, acct, b.st.SessionKey(e.SessionID)); err != nil {
		return err
	}
	_, err := b.account.Exec(acct)
	return err
}

// PutFile records how far a file has been read.
func (b *Batch) PutFile(key string, st FileState) error {
	_, err := b.fileSt.Exec(key, int64(st.Dev), int64(st.Ino), st.Size, st.Offset, st.Mtime)
	return err
}

// Commit commits the transaction.
func (b *Batch) Commit() error { return b.tx.Commit() }

// Rollback abandons the transaction.
func (b *Batch) Rollback() error { return b.tx.Rollback() }
