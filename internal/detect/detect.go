// Package detect decides whether a week's weekly limit behaved differently from the
// other weeks, and how.
//
// Each observation (an account-week with a known reading) is compared with the weeks
// before it (up to Baseline of them). All weeks are measured with one fixed set of
// weights (the saved fit, see calibrate --save), and a week's cost of 1% is set
// against the median of the earlier weeks'. Fixed weights and a median keep a few
// odd weeks from distorting the verdict on the others. A week far off is classified by checking whether the
// difference survives other plausible weightings:
//   - budget: off by about the same under every weighting, so the whole limit moved
//     (or, when 1% got cheaper, usage this machine does not see);
//   - weighting: the difference depends on how cache tokens are counted, so the mix
//     of tokens changed or the weights did;
//
// and, separately, rises of the weekly figure while this machine used the account
// not at all prove usage elsewhere.
package detect

import (
	"math"
	"sort"

	"github.com/mehdashti/limit-ledger/internal/calibrate"
	"github.com/mehdashti/limit-ledger/internal/sample"
	"github.com/mehdashti/limit-ledger/internal/store"
)

// Verdicts.
const (
	Normal    = "normal"
	Budget    = "budget"
	Weighting = "weighting"
	TooFew    = "too few weeks to compare"
)

// Finding is one week compared with the other weeks of the same plan.
type Finding struct {
	Obs       calibrate.Observation
	Others    int
	Expected  float64 // the other weeks' mean cost of 1% under their best fit
	Observed  float64 // this week's cost of 1% under those weights
	Deviation float64 // Observed/Expected - 1
	// Under other weightings: the same comparison with fixed reference weights.
	ByReference map[string]float64
	Verdict     string
}

// Cheaper reports whether 1% cost fewer tokens than usual: the weekly figure rose
// faster than local usage explains.
func (f Finding) Cheaper() bool { return f.Deviation < 0 }

// Baseline is how many earlier weeks each week is compared with.
const Baseline = 4

// Check compares every observation with up to Baseline earlier observations of its
// plan (by reset time). threshold is a fraction (0.15 = 15%).
func Check(obs []calibrate.Observation, weights calibrate.Units, threshold float64) []Finding {
	byPlan := map[string][]int{}
	for i, o := range obs {
		byPlan[o.Plan] = append(byPlan[o.Plan], i)
	}
	out := make([]Finding, len(obs))
	for _, idx := range byPlan {
		sort.SliceStable(idx, func(a, b int) bool { return obs[idx[a]].Reset < obs[idx[b]].Reset })
		for n, i := range idx {
			var earlier []calibrate.Observation
			for _, j := range idx[max(0, n-Baseline):n] {
				earlier = append(earlier, obs[j])
			}
			out[i] = compare(obs[i], earlier, weights, threshold)
		}
	}
	return out
}

func compare(o calibrate.Observation, earlier []calibrate.Observation, weights calibrate.Units, threshold float64) Finding {
	f := Finding{Obs: o, Others: len(earlier), ByReference: map[string]float64{}}
	if len(earlier) < 2 {
		f.Verdict = TooFew
		return f
	}
	dev := func(w calibrate.Units) (observed, expected, d float64) {
		var ps []float64
		for _, e := range earlier {
			ps = append(ps, calibrate.PerPercent(e, w))
		}
		expected = median(ps)
		observed = calibrate.PerPercent(o, w)
		if expected <= 0 {
			return observed, expected, 0
		}
		return observed, expected, observed/expected - 1
	}
	f.Observed, f.Expected, f.Deviation = dev(weights)
	sameSign, allLarge := true, true
	for _, r := range calibrate.References {
		_, _, d := dev(calibrate.Evaluate(nil, r.Multiplier).Weights)
		f.ByReference[r.Name] = d
		if (d < 0) != (f.Deviation < 0) {
			sameSign = false
		}
		if math.Abs(d) < threshold/2 {
			allLarge = false
		}
	}
	switch {
	case math.Abs(f.Deviation) < threshold:
		f.Verdict = Normal
	case sameSign && allLarge:
		f.Verdict = Budget
	default:
		f.Verdict = Weighting
	}
	return f
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// IdleRise is a rise of an account's weekly figure between two readings while this
// machine did not use that account: the usage happened somewhere else.
type IdleRise struct {
	Account      string
	From, To     sample.Sample
	Rise         float64 // percentage points
	LocalDollars float64 // API-equivalent usage of the account here in between
}

// IdleRises finds rises of at least minRise points between consecutive readings of
// the same weekly window while the account's local usage was at most maxLocal
// API-equivalent dollars per point of rise.
func IdleRises(st *store.Store, minRise, maxLocal float64) ([]IdleRise, error) {
	samples, err := st.Samples(math.MinInt64, math.MaxInt64)
	if err != nil {
		return nil, err
	}
	segs, err := st.Timeline()
	if err != nil {
		return nil, err
	}
	// Group by reset time, as the windows report does: within an hour is one window.
	type win struct {
		reset int64
		list  []sample.Sample
	}
	var wins []*win
	for _, s := range samples {
		if s.SevenPct == nil || s.SevenReset == nil {
			continue
		}
		var w *win
		for _, x := range wins {
			if abs(x.reset-*s.SevenReset) < hour {
				w = x
				break
			}
		}
		if w == nil {
			w = &win{reset: *s.SevenReset}
			wins = append(wins, w)
		}
		w.list = append(w.list, s)
	}
	var out []IdleRise
	for _, w := range wins {
		sort.Slice(w.list, func(i, j int) bool { return w.list[i].TS < w.list[j].TS })
		votes := map[string]int{}
		for _, s := range w.list {
			votes[store.AccountAt(segs, s.TS)]++
		}
		acct, n := "", 0
		for a, v := range votes {
			if a != "" && v > n {
				acct, n = a, v
			}
		}
		if acct == "" {
			continue
		}
		for i := 1; i < len(w.list); i++ {
			a, b := w.list[i-1], w.list[i]
			rise := *b.SevenPct - *a.SevenPct
			if rise < minRise {
				continue
			}
			g, err := st.AccountTotals(segs, acct, a.TS, b.TS)
			if err != nil {
				return nil, err
			}
			_, d := calibrate.ToUnits(g)
			if d <= maxLocal*rise {
				out = append(out, IdleRise{Account: acct, From: a, To: b, Rise: rise, LocalDollars: d})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].To.TS < out[j].To.TS })
	return out, nil
}

const hour = int64(3600 * 1000)

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
