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
		coalesce(max(e.ts), 0) FROM accounts a LEFT JOIN org_events e ON e.account = a.account
		GROUP BY a.account ORDER BY 5`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.Key, &a.Label, &a.Plan, &a.Events, &a.First, &a.Last); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAccount sets the label and/or plan of the one account whose key starts with
// prefix. A nil value is left unchanged.
func (s *Store) SetAccount(prefix string, label, plan *string) (string, error) {
	if prefix == "" {
		return "", fmt.Errorf("empty account key")
	}
	rows, err := s.db.Query(`SELECT account FROM accounts WHERE substr(account, 1, ?) = ?`, len(prefix), prefix)
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
	res, err = s.db.Exec(`UPDATE accounts SET label = coalesce(?, label), plan = coalesce(?, plan)
		WHERE account = ?`, label, plan, keys[0])
	if err == nil {
		_, err = res.RowsAffected()
	}
	return keys[0], err
}
