package store

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/mehdashti/limit-ledger/internal/sample"
)

// PutSample stores a utilization sample. The session id is kept only as a salted hash.
func (b *Batch) PutSample(s sample.Sample) error {
	_, err := b.tx.Exec(`INSERT OR IGNORE INTO samples(ts, session_key, five_pct, five_reset, seven_pct,
		seven_reset, version) VALUES(?,?,?,?,?,?,?)`, s.TS, b.st.SessionKey(s.SessionID),
		s.FivePct, s.FiveReset, s.SevenPct, s.SevenReset, s.Version)
	return err
}

// SetMeta sets a value in the meta table within the batch.
func (b *Batch) SetMeta(key string, v int64) error {
	_, err := b.tx.Exec(`INSERT INTO meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, strconv.FormatInt(v, 10))
	return err
}

// MetaInt reads an integer from the meta table; ok is false when it is not set.
func (s *Store) MetaInt(key string) (v int64, ok bool, err error) {
	var str string
	err = s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&str)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	v, err = strconv.ParseInt(str, 10, 64)
	return v, err == nil, err
}

// Samples returns all samples in fromMs <= ts < toMs, oldest first. Session ids are
// not returned.
func (s *Store) Samples(fromMs, toMs int64) ([]sample.Sample, error) {
	rows, err := s.db.Query(`SELECT ts, five_pct, five_reset, seven_pct, seven_reset, version
		FROM samples WHERE ts >= ? AND ts < ? ORDER BY ts`, fromMs, toMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sample.Sample
	for rows.Next() {
		var x sample.Sample
		var fp, sp sql.NullFloat64
		var fr, sr sql.NullInt64
		if err := rows.Scan(&x.TS, &fp, &fr, &sp, &sr, &x.Version); err != nil {
			return nil, err
		}
		if fp.Valid {
			x.FivePct = &fp.Float64
		}
		if fr.Valid {
			x.FiveReset = &fr.Int64
		}
		if sp.Valid {
			x.SevenPct = &sp.Float64
		}
		if sr.Valid {
			x.SevenReset = &sr.Int64
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// LastSample returns the newest stored sample, or nil.
func (s *Store) LastSample() (*sample.Sample, error) {
	var ts sql.NullInt64
	if err := s.db.QueryRow(`SELECT max(ts) FROM samples`).Scan(&ts); err != nil {
		return nil, err
	}
	if !ts.Valid {
		return nil, nil
	}
	l, err := s.Samples(ts.Int64, ts.Int64+1)
	if err != nil || len(l) == 0 {
		return nil, err
	}
	return &l[len(l)-1], nil
}
