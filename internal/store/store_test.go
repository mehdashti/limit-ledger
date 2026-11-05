package store

import (
	"database/sql"
	"math"
	"path/filepath"
	"testing"

	"github.com/mehdashti/limit-ledger/internal/transcript"
)

func open(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func putEvents(t *testing.T, st *Store, evs ...transcript.OrgEvent) {
	t.Helper()
	b, err := st.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range evs {
		if err := b.PutOrgEvent(&evs[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestTimeline(t *testing.T) {
	st := open(t)
	segs, err := st.Timeline()
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 1 || segs[0].Account != "" || segs[0].Start != math.MinInt64 || segs[0].End != math.MaxInt64 {
		t.Fatalf("empty timeline: %+v", segs)
	}

	putEvents(t, st,
		transcript.OrgEvent{TS: 100, Org: "org-a", SessionID: "s1"},
		transcript.OrgEvent{TS: 150, Org: "org-a", SessionID: "s2"},
		transcript.OrgEvent{TS: 200, Org: "org-b", SessionID: "s3"},
		transcript.OrgEvent{TS: 300, Org: "org-a", SessionID: "s4"},
		transcript.OrgEvent{TS: 300, Org: "org-a", SessionID: "s4"}, // duplicate copy
	)
	a, b := st.AccountKey("org-a"), st.AccountKey("org-b")
	if a == b || len(a) != 12 {
		t.Fatalf("account keys %q %q", a, b)
	}
	segs, err = st.Timeline()
	if err != nil {
		t.Fatal(err)
	}
	want := []Segment{
		{Account: "", Start: math.MinInt64, End: 100},
		{Account: a, Start: 100, End: 200, Events: 2, LastEvent: 150},
		{Account: b, Start: 200, End: 300, Events: 1, LastEvent: 200},
		{Account: a, Start: 300, End: math.MaxInt64, Events: 1, LastEvent: 300},
	}
	if len(segs) != len(want) {
		t.Fatalf("segments %+v", segs)
	}
	for i := range want {
		if segs[i] != want[i] {
			t.Errorf("segment %d: got %+v want %+v", i, segs[i], want[i])
		}
	}
	for ts, acct := range map[int64]string{50: "", 100: "", 101: a, 200: a, 201: b, 300: b, 301: a} {
		if got := AccountAt(segs, ts); got != acct {
			t.Errorf("AccountAt(%d) = %q want %q", ts, got, acct)
		}
	}
}

func TestAccountsAndLabels(t *testing.T) {
	st := open(t)
	putEvents(t, st,
		transcript.OrgEvent{TS: 100, Org: "org-a", SessionID: "s1"},
		transcript.OrgEvent{TS: 200, Org: "org-b", SessionID: "s2"},
	)
	accts, err := st.Accounts()
	if err != nil || len(accts) != 2 {
		t.Fatalf("accounts %+v %v", accts, err)
	}
	if accts[0].Key != st.AccountKey("org-a") || accts[0].First != 100 {
		t.Errorf("order or first seen wrong: %+v", accts)
	}
	label, plan := "home", "ultra"
	key, err := st.SetAccount(accts[0].Key[:4], &label, &plan)
	if err != nil || key != accts[0].Key {
		t.Fatalf("set: %q %v", key, err)
	}
	other := "work"
	if _, err := st.SetAccount(accts[1].Key, &other, nil); err != nil {
		t.Fatal(err)
	}
	accts, _ = st.Accounts()
	if accts[0].Name() != "home" || accts[0].Plan != "ultra" || accts[1].Name() != "work" || accts[1].Plan != "" {
		t.Errorf("labels not stored: %+v", accts)
	}
	if _, err := st.SetAccount("zzzz", &label, nil); err == nil {
		t.Error("unknown prefix should fail")
	}
}

func TestMigrationFromV1RereadsFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{schemaV1, "PRAGMA user_version=1",
		"INSERT INTO files VALUES('k', 1, 2, 300, 300, 4)",
		"INSERT INTO responses(msg_id, ts, model, output) VALUES('m', 1, 'claude-opus-5-5', 7)"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	resp, _, files, err := st.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if files != 0 {
		t.Errorf("v1 file offsets should be cleared so files are read again, have %d", files)
	}
	if resp != 1 {
		t.Errorf("existing responses must be kept, have %d", resp)
	}
	if _, err := st.Accounts(); err != nil {
		t.Errorf("v2 tables missing: %v", err)
	}
}
