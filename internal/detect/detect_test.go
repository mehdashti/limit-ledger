package detect

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mehdashti/limit-ledger/internal/calibrate"
	"github.com/mehdashti/limit-ledger/internal/sample"
	"github.com/mehdashti/limit-ledger/internal/store"
	"github.com/mehdashti/limit-ledger/internal/transcript"
)

// week makes an observation whose cost of 1% is perPct units under the weights
// output = 1, cache read = 0.0025 (×0.25 API), for a given mix.
func week(acct string, reset int64, output, cacheRead, pct, perPct float64) calibrate.Observation {
	u := calibrate.Units{calibrate.Output: output, calibrate.CacheRead: cacheRead}
	scale := perPct * pct / (output + 0.0025*cacheRead)
	for k := range u {
		u[k] *= scale
	}
	return calibrate.Observation{Account: acct, Plan: "ultra", Reset: reset, Percent: pct, Units: u}
}

func TestCheckVerdicts(t *testing.T) {
	obs := []calibrate.Observation{
		week("A", 1, 18e6, 6.3e9, 100, 350e3),
		week("B", 2, 20e6, 6.2e9, 100, 350e3),
		week("A", 3, 15e6, 7.0e9, 100, 350e3),
		week("B", 4, 17e6, 5.0e9, 90, 350e3),
		week("A", 5, 16e6, 6.0e9, 100, 350e3*0.7), // the limit is 30% smaller this week
	}
	w := calibrate.Units{calibrate.Input: 0.2, calibrate.CacheRead: 0.0025, calibrate.Output: 1}
	f := Check(obs, w, 0.15)
	for i := 0; i < 2; i++ {
		if f[i].Verdict != TooFew {
			t.Errorf("week %d has no earlier week of its account: %s", i, f[i].Verdict)
		}
	}
	for i := 2; i < 4; i++ {
		if f[i].Verdict != Normal {
			t.Errorf("week %d: %s (deviation %.2f)", i, f[i].Verdict, f[i].Deviation)
		}
	}
	last := f[4]
	if last.Verdict != Budget || !last.Cheaper() || last.Deviation > -0.25 || last.Deviation < -0.35 {
		t.Errorf("smaller budget: %+v", last)
	}
	if f := Check(obs[:2], w, 0.15); f[0].Verdict != TooFew || f[1].Verdict != TooFew {
		t.Errorf("one week per account only: %s %s", f[0].Verdict, f[1].Verdict)
	}
	// Accounts are compared with themselves: a steady difference between accounts is no change.
	steady := []calibrate.Observation{week("A", 1, 18e6, 6e9, 100, 350e3), week("B", 2, 18e6, 6e9, 100, 260e3),
		week("A", 3, 18e6, 6e9, 100, 350e3), week("B", 4, 18e6, 6e9, 100, 260e3)}
	for i, x := range Check(steady, w, 0.15)[2:] {
		if x.Verdict != Normal {
			t.Errorf("steady week %d: %s (%.2f)", i+2, x.Verdict, x.Deviation)
		}
	}
}

func TestCheckWeighting(t *testing.T) {
	obs := []calibrate.Observation{
		week("A", 1, 18e6, 6.3e9, 100, 350e3),
		week("B", 2, 20e6, 6.2e9, 100, 350e3),
		week("A", 3, 15e6, 7.0e9, 100, 350e3),
	}
	// A week with the usual output per 1% but four times the cache reads: off under
	// the fitted weights, normal if only output counted.
	odd := calibrate.Observation{Account: "B", Plan: "ultra", Reset: 4, Percent: 100}
	odd.Units[calibrate.Output] = 18e6
	odd.Units[calibrate.CacheRead] = 25e9
	w := calibrate.Units{calibrate.Input: 0.2, calibrate.CacheRead: 0.0025, calibrate.Output: 1}
	f := Check(append(obs, odd), w, 0.15)[3]
	if f.Verdict != Weighting {
		t.Errorf("mix-dependent week: %s (deviation %.2f, by reference %v)", f.Verdict, f.Deviation, f.ByReference)
	}
}

func ms(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UnixMilli()
}

func TestIdleRises(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	b, err := st.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.PutOrgEvent(&transcript.OrgEvent{TS: ms("2026-11-01T00:00:00Z"), Org: "org-a", SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	reset := ms("2026-11-08T22:00:00Z")
	put := func(ts string, pct float64) {
		p, r := pct, reset
		if err := b.PutSample(sample.Sample{TS: ms(ts), SessionID: ts, SevenPct: &p, SevenReset: &r}); err != nil {
			t.Fatal(err)
		}
	}
	put("2026-11-02T00:00:00Z", 10)
	put("2026-11-02T06:00:00Z", 15) // +5 with no local use: elsewhere
	if err := b.PutResponse(&transcript.Response{MsgID: "m1", TS: ms("2026-11-02T08:00:00Z"), Model: "claude-opus-5-5",
		Kind: "main", Tokens: transcript.Tokens{Output: 5_000_000}}); err != nil { // $100
		t.Fatal(err)
	}
	put("2026-11-02T10:00:00Z", 20) // +5 with $100 of local use: explained
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	rises, err := IdleRises(st, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rises) != 1 || rises[0].Rise != 5 || rises[0].To.TS != ms("2026-11-02T06:00:00Z") || rises[0].Account != st.AccountKey("org-a") {
		t.Fatalf("rises %+v", rises)
	}
}
