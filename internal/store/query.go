package store

import (
	"github.com/mehdashti/limit-ledger/internal/transcript"
)

// Group is a sum of responses for one model and speed, optionally on one day.
type Group struct {
	Day      string // YYYY-MM-DD in UTC; empty when not grouped by day
	Model    string
	Speed    string
	Requests int64
	transcript.Tokens
}

const sums = `count(*), sum(input), sum(cache_5m), sum(cache_1h), sum(cache_read), sum(output)`

func scanGroup(sc interface{ Scan(...any) error }, g *Group, withDay bool) error {
	dst := []any{}
	if withDay {
		dst = append(dst, &g.Day)
	}
	dst = append(dst, &g.Model, &g.Speed, &g.Requests,
		&g.Input, &g.Cache5m, &g.Cache1h, &g.CacheRead, &g.Output)
	return sc.Scan(dst...)
}

// allRows is local and imported usage together.
const allRows = `(SELECT ts, model, speed, input, cache_5m, cache_1h, cache_read, output FROM responses
	UNION ALL SELECT ts, model, speed, input, cache_5m, cache_1h, cache_read, output FROM remote_responses)`

// Daily returns sums per UTC day, model and speed for fromMs <= ts < toMs, over this
// machine's usage and the usage imported from other machines.
func (s *Store) Daily(fromMs, toMs int64) ([]Group, error) {
	rows, err := s.db.Query(`SELECT date(ts/1000, 'unixepoch'), model, speed, `+sums+`
		FROM `+allRows+` WHERE ts >= ? AND ts < ? GROUP BY 1, 2, 3 ORDER BY 1`, fromMs, toMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if err := scanGroup(rows, &g, true); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Totals returns sums per model and speed for fromMs <= ts < toMs, over this machine's
// usage and the usage imported from other machines.
func (s *Store) Totals(fromMs, toMs int64) ([]Group, error) {
	return s.groups(`SELECT model, speed, `+sums+` FROM `+allRows+` WHERE ts >= ? AND ts < ? GROUP BY 1, 2`,
		fromMs, toMs)
}

// LocalTotals is Totals over this machine's usage only.
func (s *Store) LocalTotals(fromMs, toMs int64) ([]Group, error) {
	return s.groups(`SELECT model, speed, `+sums+` FROM responses WHERE ts >= ? AND ts < ? GROUP BY 1, 2`,
		fromMs, toMs)
}

// RemoteTotals is the usage imported from other machines for one account.
func (s *Store) RemoteTotals(acct string, fromMs, toMs int64) ([]Group, error) {
	return s.groups(`SELECT model, speed, `+sums+` FROM remote_responses
		WHERE account = ? AND ts >= ? AND ts < ? GROUP BY 1, 2`, acct, fromMs, toMs)
}

func (s *Store) groups(q string, args ...any) ([]Group, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if err := scanGroup(rows, &g, false); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Anchors returns the anchors of one kind ordered by reset time, then hit time.
func (s *Store) Anchors(kind string) ([]transcript.Anchor, error) {
	rows, err := s.db.Query(`SELECT kind, hit_ts, resets_ts FROM anchors WHERE kind = ?
		ORDER BY resets_ts, hit_ts`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []transcript.Anchor
	for rows.Next() {
		var a transcript.Anchor
		if err := rows.Scan(&a.Kind, &a.Hit, &a.Resets); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// KindCounts returns the number of responses per kind (main, subagent, workflow).
func (s *Store) KindCounts() (map[string]int64, error) {
	rows, err := s.db.Query(`SELECT kind, count(*) FROM responses GROUP BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// Counts reports how many responses, anchors and files the database holds.
func (s *Store) Counts() (responses, anchors, files int64, err error) {
	err = s.db.QueryRow(`SELECT (SELECT count(*) FROM responses), (SELECT count(*) FROM anchors),
		(SELECT count(*) FROM files)`).Scan(&responses, &anchors, &files)
	return
}

// RemoteAccounts lists the accounts that have imported usage in [fromMs, toMs).
func (s *Store) RemoteAccounts(fromMs, toMs int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT account FROM remote_responses WHERE ts >= ? AND ts < ? ORDER BY 1`,
		fromMs, toMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
