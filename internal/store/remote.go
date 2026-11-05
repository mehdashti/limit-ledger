package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

// MachineID is this ledger's random id, created on first use. Bundles carry it so a
// ledger never imports its own usage.
func (s *Store) MachineID() (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'machine_id'`).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id = hex.EncodeToString(b)
	_, err = s.db.Exec(`INSERT INTO meta(key, value) VALUES('machine_id', ?)`, id)
	return id, err
}

// Row is one locally recorded response with its raw message id.
type Row struct {
	MsgID                                      string
	TS                                         int64
	Model                                      string
	Speed                                      string
	Input, Cache5m, Cache1h, CacheRead, Output int64
}

// LocalRows returns this machine's responses in fromMs <= ts < toMs.
func (s *Store) LocalRows(fromMs, toMs int64) ([]Row, error) {
	rows, err := s.db.Query(`SELECT msg_id, ts, model, speed, input, cache_5m, cache_1h, cache_read, output
		FROM responses WHERE ts >= ? AND ts < ? ORDER BY ts`, fromMs, toMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.MsgID, &r.TS, &r.Model, &r.Speed, &r.Input, &r.Cache5m, &r.Cache1h, &r.CacheRead,
			&r.Output); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AccountForXID returns the local account with this cross-machine id. If there is
// none, it creates one with the key "x:"+xid, and with the label and plan given,
// unless the label is already taken.
func (s *Store) AccountForXID(xid, label, plan string) (key string, created bool, err error) {
	err = s.db.QueryRow(`SELECT account FROM accounts WHERE xid = ? ORDER BY account LIMIT 1`, xid).Scan(&key)
	if err == nil {
		return key, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	if xid == "" {
		return "", false, fmt.Errorf("empty account id")
	}
	key = "x:" + xid
	var taken int
	if label != "" {
		if err := s.db.QueryRow(`SELECT count(*) FROM accounts WHERE lower(label) = lower(?)`, label).Scan(&taken); err != nil {
			return "", false, err
		}
		if taken > 0 {
			label = ""
		}
	}
	_, err = s.db.Exec(`INSERT OR IGNORE INTO accounts(account, label, plan, xid) VALUES(?,?,?,?)`, key, label, plan, xid)
	return key, err == nil, err
}

// RemoteRow is one imported response.
type RemoteRow struct {
	MID, Account, Model, Speed                 string
	TS                                         int64
	Input, Cache5m, Cache1h, CacheRead, Output int64
}

// ImportRemote stores rows imported from machine, replacing earlier imports of the
// same rows (keeping the larger count of each kind), and records the import.
func (s *Store) ImportRemote(machine string, rows []RemoteRow, now int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.Prepare(`INSERT INTO remote_responses(machine, mid, ts, account, model, speed, input, cache_5m,
		cache_1h, cache_read, output) VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(machine, mid) DO UPDATE SET ts = excluded.ts, account = excluded.account,
		input = max(input, excluded.input), cache_5m = max(cache_5m, excluded.cache_5m),
		cache_1h = max(cache_1h, excluded.cache_1h), cache_read = max(cache_read, excluded.cache_read),
		output = max(output, excluded.output)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, r := range rows {
		if _, err := st.Exec(machine, r.MID, r.TS, r.Account, r.Model, r.Speed, r.Input, r.Cache5m, r.Cache1h,
			r.CacheRead, r.Output); err != nil {
			return err
		}
	}
	var n int64
	if err := tx.QueryRow(`SELECT count(*) FROM remote_responses WHERE machine = ?`, machine).Scan(&n); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO machines(machine, imported_ts, responses) VALUES(?,?,?)
		ON CONFLICT(machine) DO UPDATE SET imported_ts = excluded.imported_ts, responses = excluded.responses`,
		machine, now, n); err != nil {
		return err
	}
	return tx.Commit()
}

// Machine is another machine whose usage was imported.
type Machine struct {
	ID         string
	ImportedTS int64
	Responses  int64
	First      int64
	Last       int64
}

// Machines lists the machines whose usage was imported.
func (s *Store) Machines() ([]Machine, error) {
	rows, err := s.db.Query(`SELECT m.machine, m.imported_ts, m.responses, coalesce(min(r.ts), 0), coalesce(max(r.ts), 0)
		FROM machines m LEFT JOIN remote_responses r ON r.machine = m.machine GROUP BY m.machine ORDER BY 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Machine
	for rows.Next() {
		var m Machine
		if err := rows.Scan(&m.ID, &m.ImportedTS, &m.Responses, &m.First, &m.Last); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
