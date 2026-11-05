package report

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mehdashti/limit-ledger/internal/sample"
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

func TestAnchorsWindow(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	b, err := st.Begin()
	if err != nil {
		t.Fatal(err)
	}
	put := func(id, ts string, out int64) {
		r := &transcript.Response{MsgID: id, TS: ms(ts), Model: "claude-opus-5-5", Kind: "main",
			Tokens: transcript.Tokens{Output: out}}
		if err := b.PutResponse(r); err != nil {
			t.Fatal(err)
		}
	}
	// Window: resets 2026-10-13T07:00Z, so it starts 2026-10-06T07:00Z; hit at 2026-10-12T16:23Z.
	put("before", "2026-10-06T06:59:00Z", 1_000_000)
	put("in1", "2026-10-06T07:00:00Z", 1_000_000)
	put("in2", "2026-10-12T16:00:00Z", 1_000_000)
	put("after", "2026-10-12T16:30:00Z", 1_000_000)
	for _, a := range []transcript.Anchor{
		{Kind: "weekly", Hit: ms("2026-10-12T16:23:00Z"), Resets: ms("2026-10-13T07:00:00Z")},
		{Kind: "weekly", Hit: ms("2026-10-12T16:25:00Z"), Resets: ms("2026-10-13T07:00:00Z")}, // repeat of the same hit
	} {
		if err := b.PutAnchor(&a); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Anchors(st, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if n := strings.Count(text, "2026-10-12 16:2"); n != 1 {
		t.Errorf("want one row for the repeated hit, got %d:\n%s", n, text)
	}
	// Two responses in the window, 2M output tokens, $40 at Opus 5.5 output price.
	if !strings.Contains(text, "2.0M") || !strings.Contains(text, "opus-5-5 $40") {
		t.Errorf("window sums wrong:\n%s", text)
	}
}

func TestAnchorsPerAccount(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	b, err := st.Begin()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	put := func(ts string) {
		n++
		r := &transcript.Response{MsgID: fmt.Sprint("m", n), TS: ms(ts), Model: "claude-opus-5-5", Kind: "main",
			Tokens: transcript.Tokens{Output: 1_000_000}} // $20 each
		if err := b.PutResponse(r); err != nil {
			t.Fatal(err)
		}
	}
	ev := func(ts, org string) {
		if err := b.PutOrgEvent(&transcript.OrgEvent{TS: ms(ts), Org: org, SessionID: ts}); err != nil {
			t.Fatal(err)
		}
	}
	// Window of the hit: 2026-10-06T07:00Z .. hit 2026-10-12T16:23Z.
	put("2026-10-06T08:00:00Z") // before any account record: unknown
	ev("2026-10-07T00:00:00Z", "org-a")
	put("2026-10-07T01:00:00Z") // A
	ev("2026-10-08T00:00:00Z", "org-b")
	put("2026-10-08T01:00:00Z") // B
	ev("2026-10-09T00:00:00Z", "org-a")
	put("2026-10-09T01:00:00Z") // A
	if err := b.PutAnchor(&transcript.Anchor{Kind: "weekly", Hit: ms("2026-10-12T16:23:00Z"),
		Resets: ms("2026-10-13T07:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	la, lb := "A", "B"
	if _, err := st.SetAccount(st.AccountKey("org-a"), &la, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetAccount(st.AccountKey("org-b"), &lb, nil); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Anchors(st, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"  A  ", "2.0M", "opus-5-5 $40", "B used $20 meanwhile", "lower bound: $20 before account records",
		"switch uncertainty: $20–$60"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}

	out.Reset()
	if err := Weekly(st, &out, 0, time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), true); err != nil {
		t.Fatal(err)
	}
	text = out.String()
	// ISO week 2026-W41 holds all four responses: A twice, B once, unknown once.
	for _, want := range []string{"2026-W41  A", "2026-W41  B", "2026-W41  unknown"} {
		if !strings.Contains(strings.Join(strings.Fields(text), "  "), strings.Join(strings.Fields(want), "  ")) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}

	out.Reset()
	if err := Accounts(st, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Tue 07:00 ×1") {
		t.Errorf("schedule not attributed to A:\n%s", out.String())
	}
}

func TestFormatting(t *testing.T) {
	for in, want := range map[int64]string{950: "950", 12_345: "12.3K", 108_000_000: "108.0M", 12_152_000_000: "12.15B"} {
		if got := Tokens(in); got != want {
			t.Errorf("Tokens(%d) = %s want %s", in, got, want)
		}
	}
	for in, want := range map[float64]string{0: "0", 999.6: "1,000", 1234567: "1,234,567", -2500: "-2,500"} {
		if got := Thousands(in); got != want {
			t.Errorf("Thousands(%v) = %s want %s", in, got, want)
		}
	}
	if got := isoWeekStart(time.Date(2026, 11, 5, 15, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("iso week start %v", got)
	}
}

func TestWindowsPerPercent(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	b, err := st.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.PutOrgEvent(&transcript.OrgEvent{TS: ms("2026-11-02T00:00:00Z"), Org: "org-b", SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	reset := ms("2026-11-08T22:00:00Z")
	samp := func(ts string, pct float64) {
		p, r := pct, reset
		if err := b.PutSample(sample.Sample{TS: ms(ts), SessionID: "s", SevenPct: &p, SevenReset: &r}); err != nil {
			t.Fatal(err)
		}
	}
	samp("2026-11-03T00:00:00Z", 20)
	samp("2026-11-04T00:00:00Z", 30)
	// 10 points of the weekly figure, with $200 of Opus 5.5 output in between.
	for i, ts := range []string{"2026-11-03T06:00:00Z", "2026-11-03T12:00:00Z"} {
		if err := b.PutResponse(&transcript.Response{MsgID: fmt.Sprint("w", i), TS: ms(ts), Model: "claude-opus-5-5",
			Kind: "main", Tokens: transcript.Tokens{Output: 5_000_000}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	wins, err := Windows(st)
	if err != nil || len(wins) != 1 {
		t.Fatalf("windows %+v %v", wins, err)
	}
	w := wins[0]
	if w.Account != st.AccountKey("org-b") || w.Samples != 2 || w.DeltaPercent != 10 {
		t.Errorf("window %+v", w)
	}
	d, o, ok := w.PerPercent()
	if !ok || d != 20 || o != 1_000_000 {
		t.Errorf("per percent: $%v, %v output, %v", d, o, ok)
	}
	var out bytes.Buffer
	if err := WindowsReport(st, &out, time.UTC); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "$20.0") || !strings.Contains(out.String(), "20.0 → 30.0") {
		t.Errorf("report:\n%s", out.String())
	}
}

func TestWindowsJitteredResets(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	b, err := st.Begin()
	if err != nil {
		t.Fatal(err)
	}
	put := func(ts string, pct float64, reset string) {
		p, r := pct, ms(reset)
		if err := b.PutSample(sample.Sample{TS: ms(ts), SessionID: ts, SevenPct: &p, SevenReset: &r}); err != nil {
			t.Fatal(err)
		}
	}
	put("2026-11-05T10:00:00Z", 86, "2026-11-08T22:00:00Z")
	put("2026-11-05T11:00:00Z", 87, "2026-11-08T21:59:59Z") // same reset, a second off
	put("2026-11-05T12:00:00Z", 88, "2026-11-08T22:00:01Z")
	put("2026-11-10T12:00:00Z", 5, "2026-11-15T22:00:00Z") // next week
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	wins, err := Windows(st)
	if err != nil {
		t.Fatal(err)
	}
	if len(wins) != 2 || wins[0].Samples != 3 || wins[0].DeltaPercent != 2 || wins[0].Resets != ms("2026-11-08T22:00:00Z") {
		t.Errorf("windows %+v", wins)
	}
}

func TestCompare(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	b, err := st.Begin()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	put := func(ts string, out, cacheRead int64) {
		n++
		if err := b.PutResponse(&transcript.Response{MsgID: fmt.Sprint("c", n), TS: ms(ts), Model: "claude-opus-5-5",
			Kind: "main", Tokens: transcript.Tokens{Output: out, CacheRead: cacheRead}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.PutOrgEvent(&transcript.OrgEvent{TS: ms("2026-10-01T00:00:00Z"), Org: "org-a", SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	// Baseline: 4 days, 1M output per day.
	for _, d := range []string{"01", "02", "03", "04"} {
		put("2026-10-"+d+"T12:00:00Z", 1_000_000, 0)
	}
	// Period: 2 days, 1.5M output per day (+50%), plus cache reads.
	put("2026-10-10T12:00:00Z", 1_500_000, 10_000_000)
	put("2026-10-11T12:00:00Z", 1_500_000, 10_000_000)
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	la := "A"
	if _, err := st.SetAccount(st.AccountKey("org-a"), &la, nil); err != nil {
		t.Fatal(err)
	}
	day := func(s string) time.Time { return time.UnixMilli(ms(s)).UTC() }
	opt := CompareOptions{
		Period: Period{day("2026-10-10T00:00:00Z"), day("2026-10-12T00:00:00Z")},
		Base:   Period{day("2026-10-01T00:00:00Z"), day("2026-10-05T00:00:00Z")},
		Now:    day("2026-11-01T00:00:00Z"), Loc: time.UTC,
	}
	var out bytes.Buffer
	if err := Compare(st, &out, opt); err != nil {
		t.Fatal(err)
	}
	text := strings.Join(strings.Fields(out.String()), " ")
	for _, want := range []string{
		"(2.0 days) all accounts",
		"output 1.5M 1.0M +50% ▲",
		"requests 1 1 +0%",
		"API-equiv $ $32 $20 +60% ▲", // 1.5M*$20 + 10M*$0.20 = $32/day vs $20
		"cache read 10.0M 0 -",
		"models, period: opus-5-5 100%",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}

	key, err := ResolveAccount(st, "a")
	if err != nil || key != st.AccountKey("org-a") {
		t.Fatalf("resolve: %q %v", key, err)
	}
	opt.Account, opt.ByAccount = key, true
	out.Reset()
	if err := Compare(st, &out, opt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "account A") {
		t.Errorf("account scope missing:\n%s", out.String())
	}
	if _, err := ResolveAccount(st, "zzz"); err == nil {
		t.Error("unknown account should fail")
	}

	// A period that runs into the future counts only days up to now.
	if d := (Period{day("2026-10-31T00:00:00Z"), day("2026-11-03T00:00:00Z")}).Days(day("2026-11-01T12:00:00Z")); d != 1.5 {
		t.Errorf("days so far %v", d)
	}
}
