package calibrate

import (
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/mehdashti/limit-ledger/internal/sample"
	"github.com/mehdashti/limit-ledger/internal/store"
	"github.com/mehdashti/limit-ledger/internal/transcript"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6*math.Max(1, math.Abs(b)) }

func TestToUnits(t *testing.T) {
	u, d := ToUnits([]store.Group{
		{Model: "claude-fable-5-1", Tokens: transcript.Tokens{Output: 1e6, CacheRead: 1e6}},
		{Model: "claude-opus-5-5", Tokens: transcript.Tokens{Cache1h: 1e6, Input: 1e6}},
		{Model: "claude-sonnet-5-5", Speed: "fast", Tokens: transcript.Tokens{Output: 1e6}},
	})
	// Fable output is 2.5 Opus outputs; Sonnet output 0.5, doubled by fast mode.
	if !near(u[Output], 2.5e6+1e6) {
		t.Errorf("output units %v", u[Output])
	}
	if !near(u[CacheRead], 1.25e6) || !near(u[CacheWrite], 1.6e6) || !near(u[Input], 1e6) {
		t.Errorf("units %v", u)
	}
	// $50 + $0.25 + $8 + $4 + $20
	if !near(d, 82.25) {
		t.Errorf("dollars %v", d)
	}
}

func TestFitRecoversWeights(t *testing.T) {
	// Observations made with cache writes free and cache reads at 0.25 × API, and a
	// budget of 300K units per 1%.
	truth := Units{Input: 0.2, CacheWrite: 0, CacheRead: 0.25 * 0.01, Output: 1}
	mk := func(out, write, read, pct float64) Observation {
		o := Observation{Percent: pct, Units: Units{Output: out, CacheWrite: write, CacheRead: read}}
		units := truth[Output]*out + truth[CacheRead]*read
		// Scale to the budget exactly.
		f := 300e3 * pct / units
		for k := range o.Units {
			o.Units[k] *= f
		}
		return o
	}
	obs := []Observation{mk(18e6, 70e6, 6.3e9, 100), mk(20e6, 100e6, 6.2e9, 100), mk(14e6, 140e6, 7.5e9, 86), mk(10e6, 20e6, 9e9, 60)}
	fits := Fit(obs)
	best := fits[0]
	if best.Multiplier[CacheWrite] != 0 || best.Multiplier[CacheRead] != 0.25 {
		t.Errorf("best multipliers %v", best.Multiplier)
	}
	if best.Spread > 1e-9 || !near(best.PerPercent, 300e3) {
		t.Errorf("best spread %v, per percent %v", best.Spread, best.PerPercent)
	}
	if api := Evaluate(obs, Units{1, 1, 1, 1}); api.Spread <= best.Spread {
		t.Errorf("API weights should fit worse here: %v", api.Spread)
	}
}

func ms(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UnixMilli()
}

func TestCollect(t *testing.T) {
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
	use := func(ts string, out int64) {
		n++
		if err := b.PutResponse(&transcript.Response{MsgID: fmt.Sprint("m", n), TS: ms(ts), Model: "claude-opus-5-5",
			Kind: "main", Tokens: transcript.Tokens{Output: out}}); err != nil {
			t.Fatal(err)
		}
	}
	ev := func(ts, org string) {
		if err := b.PutOrgEvent(&transcript.OrgEvent{TS: ms(ts), Org: org, SessionID: ts}); err != nil {
			t.Fatal(err)
		}
	}
	// A resets Tuesdays 07:00 UTC (known from a limit hit); B resets Sundays 22:00 UTC
	// (known from a reading).
	ev("2026-10-01T00:00:00Z", "org-a")
	use("2026-10-12T10:00:00Z", 6_000_000)
	if err := b.PutAnchor(&transcript.Anchor{Kind: "weekly", Hit: ms("2026-10-12T16:00:00Z"), Resets: ms("2026-10-13T07:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	ev("2026-10-26T00:00:00Z", "org-b") // A → B
	use("2026-10-27T00:00:00Z", 17_000_000)
	ev("2026-10-29T07:00:00Z", "org-a") // B → A, but B comes back on 10-31, before its reset on 11-01 22:00
	use("2026-10-30T00:00:00Z", 20_000_000)
	ev("2026-10-31T00:00:00Z", "org-b") // A → B, but A comes back on 11-01, before its reset on 11-03 07:00
	use("2026-10-31T01:00:00Z", 1_000_000)
	ev("2026-11-01T00:00:00Z", "org-a") // B → A; B not back before 11-01 22:00: a switch reading for B
	use("2026-11-01T01:00:00Z", 1_000_000)
	ev("2026-11-02T06:00:00Z", "org-b") // A → B; A not back before 11-03 07:00: a switch reading
	use("2026-11-03T00:00:00Z", 14_000_000)
	p, r := 86.0, ms("2026-11-08T21:59:59Z")
	if err := b.PutSample(sample.Sample{TS: ms("2026-11-05T11:00:00Z"), SessionID: "s", SevenPct: &p, SevenReset: &r}); err != nil {
		t.Fatal(err)
	}
	low, r2 := 5.0, ms("2026-11-08T22:00:00Z")
	if err := b.PutSample(sample.Sample{TS: ms("2026-11-02T08:00:00Z"), SessionID: "s2", SevenPct: &low, SevenReset: &r2}); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	on := true
	for _, org := range []string{"org-a", "org-b"} {
		if _, err := st.SetAccountPolicy(st.AccountKey(org), nil, nil, &on); err != nil {
			t.Fatal(err)
		}
	}
	A, B := st.AccountKey("org-a"), st.AccountKey("org-b")

	obs, notes, err := Collect(st, Options{Since: ms("2026-10-20T00:00:00Z"), Now: ms("2026-11-05T12:00:00Z")})
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		acct, reset, at, source string
		pct, output             float64
	}
	wants := []want{
		// The first A → B switch (10-26) is followed by no use of A in its window: no observation.
		{B, "2026-11-01T22:00:00Z", "2026-11-01T00:00:00Z", "switch", 100, 17e6 + 1e6},
		{A, "2026-11-03T07:00:00Z", "2026-11-02T06:00:00Z", "switch", 100, 20e6 + 1e6},
		{B, "2026-11-08T22:00:00Z", "2026-11-05T11:00:00Z", "reading", 86, 14e6},
	}
	if len(obs) != len(wants) {
		t.Fatalf("got %d observations: %+v (notes %v)", len(obs), obs, notes)
	}
	for i, w := range wants {
		o := obs[i]
		if o.Account != w.acct || o.Reset != ms(w.reset) || o.At != ms(w.at) || o.Source != w.source ||
			o.Percent != w.pct || !near(o.Units[Output], w.output) {
			t.Errorf("observation %d: got %+v, want %+v", i, o, w)
		}
	}

	// The A → B switch on 10-31 is not a reading, because A came back before its reset.
	// The Oct 12 hit is left out by Since; with Since 0 it comes back.
	obs, _, err = Collect(st, Options{Now: ms("2026-11-05T12:00:00Z")})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range obs {
		if o.Source == "limit hit" && o.Account == A && near(o.Units[Output], 6e6) {
			found = true
		}
		if o.Source == "switch" && (o.At == ms("2026-10-31T00:00:00Z") || o.At == ms("2026-10-29T07:00:00Z")) {
			t.Error("a switch followed by a return before the reset must not count as 100%")
		}
	}
	if !found {
		t.Errorf("limit-hit observation missing: %+v", obs)
	}
}
