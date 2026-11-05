package merge

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mehdashti/limit-ledger/internal/store"
	"github.com/mehdashti/limit-ledger/internal/transcript"
)

func ms(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UnixMilli()
}

func open(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func fill(t *testing.T, st *store.Store, events map[string]string, responses map[string]int64) {
	t.Helper()
	b, err := st.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for ts, org := range events {
		if err := b.PutOrgEvent(&transcript.OrgEvent{TS: ms(ts), Org: org, SessionID: ts}); err != nil {
			t.Fatal(err)
		}
	}
	for ts, out := range responses {
		if err := b.PutResponse(&transcript.Response{MsgID: "msg-" + ts, TS: ms(ts), Model: "claude-opus-5-5",
			Kind: "main", Tokens: transcript.Tokens{Output: out}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
}

func output(groups []store.Group, err error) int64 {
	if err != nil {
		panic(err)
	}
	var n int64
	for _, g := range groups {
		n += g.Output
	}
	return n
}

func TestExportImport(t *testing.T) {
	main, laptop := open(t), open(t)
	// main: account A from Nov 1, then B from Nov 2.
	fill(t, main, map[string]string{"2026-11-01T00:00:00Z": "org-a", "2026-11-02T00:00:00Z": "org-b"},
		map[string]int64{"2026-11-01T10:00:00Z": 100, "2026-11-02T10:00:00Z": 200})
	// laptop: B on Nov 1, A from Nov 2 (the opposite way round), and one response from
	// before any account record, which cannot be attributed.
	fill(t, laptop, map[string]string{"2026-11-01T00:00:00Z": "org-b", "2026-11-02T00:00:00Z": "org-a",
		"2026-11-03T00:00:00Z": "org-c"},
		map[string]int64{"2026-10-31T10:00:00Z": 7, "2026-11-01T10:00:00Z": 30, "2026-11-02T10:00:00Z": 40,
			"2026-11-02T11:00:00Z": 50, "2026-11-03T10:00:00Z": 9})
	if store.XID("org-a") == store.XID("org-b") || main.AccountKey("org-a") == laptop.AccountKey("org-a") {
		t.Fatal("cross-machine ids must differ per account and agree across machines; local keys must not")
	}

	b, stats, err := Export(laptop, 0, ms("2026-11-05T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Rows != 4 || stats.Unassigned != 1 || len(b.Accounts) != 3 {
		t.Fatalf("export stats %+v, accounts %+v", stats, b.Accounts)
	}
	data, _ := json.Marshal(b)
	var back Bundle
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"msg-", "org-a"} {
		if json.Valid(data) && contains(string(data), s) {
			t.Errorf("bundle leaks %q", s)
		}
	}

	is, err := Import(main, back, ms("2026-11-05T01:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if is.Rows != 4 || len(is.NewAccounts) != 1 || is.NewAccounts[0] != "x:"+store.XID("org-c") {
		t.Errorf("import stats %+v", is)
	}
	segs, err := main.Timeline()
	if err != nil {
		t.Fatal(err)
	}
	A, B := main.AccountKey("org-a"), main.AccountKey("org-b")
	all := func(acct string) int64 {
		g, err := main.AccountTotals(segs, acct, 0, ms("2026-11-10T00:00:00Z"))
		return output(g, err)
	}
	if a, b := all(A), all(B); a != 100+40+50 || b != 200+30 {
		t.Errorf("merged totals A=%d B=%d", a, b)
	}
	if n := output(main.LocalTotals(0, ms("2026-11-10T00:00:00Z"))); n != 300 {
		t.Errorf("local totals %d", n)
	}
	if n := output(main.Totals(0, ms("2026-11-10T00:00:00Z"))); n != 300+129 {
		t.Errorf("all totals %d", n)
	}

	// Importing again replaces; it does not add.
	if _, err := Import(main, back, ms("2026-11-05T02:00:00Z")); err != nil {
		t.Fatal(err)
	}
	if a := all(A); a != 190 {
		t.Errorf("after re-import A=%d", a)
	}
	machines, err := main.Machines()
	if err != nil || len(machines) != 1 || machines[0].Responses != 4 {
		t.Errorf("machines %+v %v", machines, err)
	}
	// A ledger refuses its own bundle.
	own, _, err := Export(main, 0, ms("2026-11-05T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Import(main, own, ms("2026-11-05T03:00:00Z")); err == nil {
		t.Error("importing a ledger's own bundle must fail")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
