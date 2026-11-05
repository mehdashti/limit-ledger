// Package merge moves one machine's usage into another machine's ledger, so the
// usage of an account is complete even when the account is used on several machines.
//
// The exporting machine attributes each response to an account with its own timeline
// (the account that was active there at that moment) and names the account by its
// cross-machine id. The bundle holds numbers only: times, models, token counts and a
// hash of each message id, so a repeated import replaces rather than adds.
package merge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/mehdashti/limit-ledger/internal/store"
)

// Version is the bundle format version.
const Version = 1

// Bundle is one machine's usage per account.
type Bundle struct {
	BundleVersion int       `json:"bundle_version"`
	Machine       string    `json:"machine"`
	Created       int64     `json:"created"`
	Since         int64     `json:"since"`
	Accounts      []Account `json:"accounts"`
	Models        []string  `json:"models"`
	Rows          []Row     `json:"rows"`
}

// Account names an account by its cross-machine id.
type Account struct {
	XID   string `json:"xid"`
	Label string `json:"label,omitempty"`
	Plan  string `json:"plan,omitempty"`
}

// Row is one response. Account and Model index the bundle's lists.
type Row struct {
	ID        string `json:"id"` // hash of the message id
	TS        int64  `json:"t"`
	Account   int    `json:"a"`
	Model     int    `json:"m"`
	Speed     string `json:"s,omitempty"`
	Input     int64  `json:"i,omitempty"`
	Cache5m   int64  `json:"w5,omitempty"`
	Cache1h   int64  `json:"w1,omitempty"`
	CacheRead int64  `json:"r,omitempty"`
	Output    int64  `json:"o,omitempty"`
}

// ExportStats describes an export.
type ExportStats struct {
	Rows       int
	Unassigned int // responses from before account records, which cannot be attributed
	NoXID      int // responses of accounts without a cross-machine id (run ingest)
}

// Export builds a bundle of this machine's usage since sinceMs.
func Export(st *store.Store, sinceMs, nowMs int64) (Bundle, ExportStats, error) {
	var stats ExportStats
	id, err := st.MachineID()
	if err != nil {
		return Bundle{}, stats, err
	}
	b := Bundle{BundleVersion: Version, Machine: id, Created: nowMs, Since: sinceMs}
	accts, err := st.Accounts()
	if err != nil {
		return b, stats, err
	}
	byKey := map[string]store.Account{}
	for _, a := range accts {
		byKey[a.Key] = a
	}
	segs, err := st.Timeline()
	if err != nil {
		return b, stats, err
	}
	acctIdx, modelIdx := map[string]int{}, map[string]int{}
	for _, sg := range segs {
		from, to := max(sinceMs, sg.Start), min(nowMs+1, sg.End)
		if from >= to {
			continue
		}
		rows, err := st.LocalRows(from, to)
		if err != nil {
			return b, stats, err
		}
		a := byKey[sg.Account]
		switch {
		case sg.Account == "":
			stats.Unassigned += len(rows)
			continue
		case a.XID == "":
			stats.NoXID += len(rows)
			continue
		}
		ai, ok := acctIdx[a.XID]
		if !ok {
			ai = len(b.Accounts)
			acctIdx[a.XID] = ai
			b.Accounts = append(b.Accounts, Account{XID: a.XID, Label: a.Label, Plan: a.Plan})
		}
		for _, r := range rows {
			mi, ok := modelIdx[r.Model]
			if !ok {
				mi = len(b.Models)
				modelIdx[r.Model] = mi
				b.Models = append(b.Models, r.Model)
			}
			h := sha256.Sum256([]byte(r.MsgID))
			b.Rows = append(b.Rows, Row{ID: hex.EncodeToString(h[:10]), TS: r.TS, Account: ai, Model: mi,
				Speed: r.Speed, Input: r.Input, Cache5m: r.Cache5m, Cache1h: r.Cache1h, CacheRead: r.CacheRead,
				Output: r.Output})
		}
		stats.Rows += len(rows)
	}
	return b, stats, nil
}

// ImportStats describes an import.
type ImportStats struct {
	Rows        int
	PerAccount  map[string]int // local account key -> rows
	NewAccounts []string       // accounts this machine had not seen
}

// Import adds a bundle from another machine to the ledger.
func Import(st *store.Store, b Bundle, nowMs int64) (ImportStats, error) {
	stats := ImportStats{PerAccount: map[string]int{}}
	if b.BundleVersion != Version {
		return stats, fmt.Errorf("bundle version %d, this build reads %d", b.BundleVersion, Version)
	}
	own, err := st.MachineID()
	if err != nil {
		return stats, err
	}
	switch b.Machine {
	case "":
		return stats, fmt.Errorf("bundle has no machine id")
	case own:
		return stats, fmt.Errorf("this bundle comes from this machine's own ledger")
	}
	keys := make([]string, len(b.Accounts))
	for i, a := range b.Accounts {
		k, created, err := st.AccountForXID(a.XID, a.Label, a.Plan)
		if err != nil {
			return stats, err
		}
		keys[i] = k
		if created {
			stats.NewAccounts = append(stats.NewAccounts, k)
		}
	}
	rows := make([]store.RemoteRow, 0, len(b.Rows))
	for i, r := range b.Rows {
		if r.Account < 0 || r.Account >= len(keys) || r.Model < 0 || r.Model >= len(b.Models) || r.ID == "" {
			return stats, fmt.Errorf("row %d refers to an unknown account or model", i)
		}
		rows = append(rows, store.RemoteRow{MID: r.ID, Account: keys[r.Account], Model: b.Models[r.Model],
			Speed: r.Speed, TS: r.TS, Input: r.Input, Cache5m: r.Cache5m, Cache1h: r.Cache1h,
			CacheRead: r.CacheRead, Output: r.Output})
		stats.PerAccount[keys[r.Account]]++
	}
	if err := st.ImportRemote(b.Machine, rows, nowMs); err != nil {
		return stats, err
	}
	stats.Rows = len(rows)
	return stats, nil
}
