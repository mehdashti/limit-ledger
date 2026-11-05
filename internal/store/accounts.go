package store

import (
	"database/sql"
	"fmt"
	"math"
)

// Segment is a stretch of time during which one account was active on this machine.
// Claude Code keeps one set of credentials per configuration directory, so one
// account is active at a time. The account changes at the first credential_org
// record that names another organization. Account "" is the time before any record
// exists, when the account is unknown.
type Segment struct {
	Account    string
	Start, End int64 // Unix ms, half-open [Start, End)
	Events     int
	LastEvent  int64
}

// Timeline returns the account segments covering all time, in order.
func (s *Store) Timeline() ([]Segment, error) {
	rows, err := s.db.Query(`SELECT ts, account FROM org_events ORDER BY ts, account`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var segs []Segment
	for rows.Next() {
		var ts int64
		var acct string
		if err := rows.Scan(&ts, &acct); err != nil {
			return nil, err
		}
		if n := len(segs); n > 0 && segs[n-1].Account == acct {
			segs[n-1].Events++
			segs[n-1].LastEvent = ts
			continue
		}
		segs = append(segs, Segment{Account: acct, Start: ts, Events: 1, LastEvent: ts})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(segs) == 0 {
		return []Segment{{Start: math.MinInt64, End: math.MaxInt64}}, nil
	}
	for i := range segs {
		if i+1 < len(segs) {
			segs[i].End = segs[i+1].Start
		} else {
			segs[i].End = math.MaxInt64
		}
	}
	unknown := Segment{Start: math.MinInt64, End: segs[0].Start}
	return append([]Segment{unknown}, segs...), nil
}

// AccountAt returns the account active just before ts ("" when unknown). A limit
// message belongs to the account that was active before it was written.
func AccountAt(segs []Segment, ts int64) string {
	for _, sg := range segs {
		if ts > sg.Start && ts <= sg.End {
			return sg.Account
		}
	}
	return ""
}

// Account describes one account seen on this machine.
type Account struct {
	Key, Label, Plan string
	Events           int
	First, Last      int64
	// SwitchFull says the user switches away from this account only when its weekly
	// limit is (nearly) used up, so a switch counts as a 100% reading.
	SwitchFull bool
}

// Name is the label if one is set, else the key.
func (a Account) Name() string {
	if a.Label != "" {
		return a.Label
	}
	return a.Key
}

// Accounts lists the known accounts in the order they were first seen.
func (s *Store) Accounts() ([]Account, error) {
	rows, err := s.db.Query(`SELECT a.account, a.label, a.plan, count(e.ts), coalesce(min(e.ts), 0),
		coalesce(max(e.ts), 0), a.switch_full FROM accounts a LEFT JOIN org_events e ON e.account = a.account
		GROUP BY a.account ORDER BY 5`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.Key, &a.Label, &a.Plan, &a.Events, &a.First, &a.Last, &a.SwitchFull); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAccount sets the label and/or plan of the one account whose key starts with
// prefix. A nil value is left unchanged.
func (s *Store) SetAccount(prefix string, label, plan *string) (string, error) {
	return s.SetAccountPolicy(prefix, label, plan, nil)
}

// SetAccountPolicy is SetAccount with the switch policy as well.
func (s *Store) SetAccountPolicy(prefix string, label, plan *string, switchFull *bool) (string, error) {
	if prefix == "" {
		return "", fmt.Errorf("empty account key")
	}
	// prefix is the start of a key, or a label (case-insensitive).
	rows, err := s.db.Query(`SELECT account FROM accounts WHERE substr(account, 1, ?) = ?
		OR (label != '' AND lower(label) = lower(?))`, len(prefix), prefix, prefix)
	if err != nil {
		return "", err
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return "", err
		}
		keys = append(keys, k)
	}
	rows.Close()
	switch len(keys) {
	case 0:
		return "", fmt.Errorf("no account key starts with %q", prefix)
	case 1:
	default:
		return "", fmt.Errorf("%q matches %d accounts; give more of the key", prefix, len(keys))
	}
	var res sql.Result
	res, err = s.db.Exec(`UPDATE accounts SET label = coalesce(?, label), plan = coalesce(?, plan),
		switch_full = coalesce(?, switch_full) WHERE account = ?`, label, plan, switchFull, keys[0])
	if err == nil {
		_, err = res.RowsAffected()
	}
	return keys[0], err
}

// AccountTotals sums one account's usage in [from, to) along the timeline.
func (s *Store) AccountTotals(segs []Segment, acct string, from, to int64) ([]Group, error) {
	var out []Group
	for _, sg := range segs {
		if sg.Account != acct {
			continue
		}
		a, b := max(from, sg.Start), min(to, sg.End)
		if a >= b {
			continue
		}
		g, err := s.Totals(a, b)
		if err != nil {
			return nil, err
		}
		out = append(out, g...)
	}
	return out, nil
}

// Fit is one saved estimate of the subscription weights for a plan. Weights are
// relative to one Opus 5.5 output token; PerPercent is how many such units one
// percent of the weekly limit was worth; Spread is the relative spread of that
// figure across the observations (lower is a better fit).
type Fit struct {
	TS                                   int64
	Plan                                 string
	SinceTS                              int64
	Observations                         int
	Input, CacheWrite, CacheRead, Output float64
	PerPercent, Spread                   float64
}

// SaveFit stores a fit.
func (s *Store) SaveFit(f Fit) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO fits(ts, plan, since_ts, observations, w_input, w_cache_write,
		w_cache_read, w_output, per_percent, spread) VALUES(?,?,?,?,?,?,?,?,?,?)`, f.TS, f.Plan, f.SinceTS,
		f.Observations, f.Input, f.CacheWrite, f.CacheRead, f.Output, f.PerPercent, f.Spread)
	return err
}

// Fits returns the saved fits, oldest first.
func (s *Store) Fits() ([]Fit, error) {
	rows, err := s.db.Query(`SELECT ts, plan, since_ts, observations, w_input, w_cache_write, w_cache_read,
		w_output, per_percent, spread FROM fits ORDER BY ts, plan`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fit
	for rows.Next() {
		var f Fit
		if err := rows.Scan(&f.TS, &f.Plan, &f.SinceTS, &f.Observations, &f.Input, &f.CacheWrite, &f.CacheRead,
			&f.Output, &f.PerPercent, &f.Spread); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
