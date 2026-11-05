// Package calibrate estimates how the subscription's weekly limit weighs tokens.
//
// Each observation is one account-week: the account's usage from the start of its
// weekly window up to a known reading of the weekly figure. Readings come from:
//   - limit-hit messages (100%),
//   - switches away from an account whose owner switches only at the limit (100%),
//   - utilization samples (the statusline hook, or readings taken by hand).
//
// If the weights were right and the budget the same in every week, every
// observation would give the same cost per 1%. The fit therefore prefers the
// weights under which that figure varies least across observations.
package calibrate

import (
	"math"
	"sort"
	"time"

	"github.com/mehdashti/limit-ledger/internal/price"
	"github.com/mehdashti/limit-ledger/internal/store"
)

// Week is the length of a weekly window in milliseconds.
const Week = 7 * 24 * int64(time.Hour/time.Millisecond)

const hourMs = int64(time.Hour / time.Millisecond)

// Kinds of tokens, in the order of Units.
const (
	Input = iota
	CacheWrite
	CacheRead
	Output
	nKinds
)

// KindNames names the kinds for reports.
var KindNames = [nKinds]string{"input", "cache write", "cache read", "output"}

// Units are token counts converted to Opus 5.5 equivalents per kind, using API price
// ratios between models (Fable output counts 2.5 Opus outputs, Sonnet output 0.5,
// and so on). Cache writes are in 5-minute-write equivalents (a 1-hour write costs 1.6
// of those). Only the ratio between models is borrowed from API prices; the weight of
// each kind is what the fit estimates.
type Units [nKinds]float64

// APIWeights are the API price ratios of each kind to an Opus 5.5 output token.
var APIWeights = Units{Input: 4.0 / 20, CacheWrite: 5.0 / 20, CacheRead: 0.20 / 20, Output: 1}

const opusIn, opusOut, opusRead = 4.0, 20.0, 0.20

// ToUnits converts summed token groups into Opus 5.5 equivalent units.
func ToUnits(groups []store.Group) (u Units, dollars float64) {
	for _, g := range groups {
		r, ok := price.Lookup(g.Model)
		if !ok {
			r = price.Rate{Input: opusIn, Output: opusOut, CacheRead: opusRead} // unknown model: count as Opus 5.5
		}
		f := 1.0
		if g.Speed == "fast" {
			f = price.FastFactor
		}
		u[Input] += f * float64(g.Input) * r.Input / opusIn
		u[CacheWrite] += f * (float64(g.Cache5m)*r.Input*price.Cache5mFactor +
			float64(g.Cache1h)*r.Input*price.Cache1hFactor) / (opusIn * price.Cache5mFactor)
		u[CacheRead] += f * float64(g.CacheRead) * r.CacheRead / opusRead
		u[Output] += f * float64(g.Output) * r.Output / opusOut
		if d, ok := price.Cost(g.Model, g.Speed, g.Tokens); ok {
			dollars += d
		}
	}
	return u, dollars
}

// Observation is one account-week with a known reading of the weekly figure.
type Observation struct {
	Account, Plan string
	Start, Reset  int64 // the weekly window, Unix ms
	At            int64 // when the reading applies
	Percent       float64
	Source        string // "limit hit", "switch" or "reading"
	Units         Units
	Dollars       float64
}

// Options selects the observations.
type Options struct {
	Since int64 // only windows that reset at or after this time
	Now   int64
	// MinPercent drops readings too low to carry signal.
	MinPercent float64
}

type reading struct {
	at      int64
	percent float64
	source  string
}

// Collect gathers observations. Notes explain what was left out and why.
func Collect(st *store.Store, opt Options) ([]Observation, []string, error) {
	if opt.MinPercent <= 0 {
		opt.MinPercent = 10
	}
	segs, err := st.Timeline()
	if err != nil {
		return nil, nil, err
	}
	accts, err := st.Accounts()
	if err != nil {
		return nil, nil, err
	}
	byKey := map[string]store.Account{}
	for _, a := range accts {
		byKey[a.Key] = a
	}
	samples, err := st.Samples(math.MinInt64, math.MaxInt64)
	if err != nil {
		return nil, nil, err
	}
	hits, err := st.Anchors("weekly")
	if err != nil {
		return nil, nil, err
	}
	var notes []string

	// A reference reset time per account; windows repeat every week from it.
	ref := map[string]int64{}
	for _, h := range hits {
		if a := store.AccountAt(segs, h.Hit); a != "" {
			ref[a] = h.Resets
		}
	}
	for _, s := range samples {
		if s.SevenReset == nil {
			continue
		}
		if a := store.AccountAt(segs, s.TS); a != "" {
			if _, ok := ref[a]; !ok {
				ref[a] = roundMinute(*s.SevenReset)
			}
		}
	}
	windowEnd := func(acct string, t int64) (int64, bool) {
		r0, ok := ref[acct]
		if !ok {
			return 0, false
		}
		k := floorDiv(t-r0, Week) + 1
		return r0 + k*Week, true
	}

	type wkey struct {
		acct  string
		reset int64
	}
	readings := map[wkey][]reading{}
	add := func(acct string, reset int64, r reading) {
		readings[wkey{acct, reset}] = append(readings[wkey{acct, reset}], r)
	}

	for _, h := range hits {
		if a := store.AccountAt(segs, h.Hit); a != "" {
			add(a, roundMinute(h.Resets), reading{h.Hit, 100, "limit hit"})
		}
	}
	for _, s := range samples {
		if s.SevenPct == nil || s.SevenReset == nil {
			continue
		}
		reset := roundMinute(*s.SevenReset)
		// The account whose schedule matches the sample's reset time; the timeline
		// decides only when the schedules do not.
		acct := ""
		for a := range ref {
			if e, ok := windowEnd(a, s.TS); ok && abs(e-reset) < hourMs {
				if acct != "" {
					acct = ""
					break
				}
				acct = a
			}
		}
		if acct == "" {
			acct = store.AccountAt(segs, s.TS)
		}
		if acct != "" {
			add(acct, reset, reading{s.TS, *s.SevenPct, "reading"})
		}
	}
	for i := 1; i+1 < len(segs); i++ {
		sg := segs[i]
		if sg.Account == "" || !byKey[sg.Account].SwitchFull {
			continue
		}
		reset, ok := windowEnd(sg.Account, sg.End)
		if !ok {
			notes = append(notes, "switch away from "+name(byKey[sg.Account])+" skipped: its reset schedule is unknown yet")
			continue
		}
		back := false
		for j := i + 1; j < len(segs); j++ {
			if segs[j].Account == sg.Account {
				back = segs[j].Start < reset
				break
			}
		}
		if back {
			continue // switched back before the reset, so the limit was not used up
		}
		add(sg.Account, reset, reading{sg.End, 100, "switch"})
	}

	firstRecord := int64(math.MaxInt64)
	if len(segs) > 1 {
		firstRecord = segs[1].Start
	}
	var out []Observation
	for k, rs := range readings {
		if k.reset < opt.Since {
			continue
		}
		start := k.reset - Week
		if start < firstRecord {
			notes = append(notes, "window ending "+stamp(k.reset)+" of "+name(byKey[k.acct])+
				" skipped: it starts before account records exist")
			continue
		}
		// The highest reading wins; at equal readings a limit message beats a switch,
		// which beats a sample, and a later reading beats an earlier one.
		prio := map[string]int{"limit hit": 0, "switch": 1, "reading": 2}
		sort.Slice(rs, func(i, j int) bool {
			if rs[i].percent != rs[j].percent {
				return rs[i].percent > rs[j].percent
			}
			if prio[rs[i].source] != prio[rs[j].source] {
				return prio[rs[i].source] < prio[rs[j].source]
			}
			return rs[i].at > rs[j].at
		})
		best := rs[0]
		if best.percent < opt.MinPercent {
			continue
		}
		groups, err := st.AccountTotals(segs, k.acct, start, best.at)
		if err != nil {
			return nil, nil, err
		}
		u, d := ToUnits(groups)
		if d <= 0 {
			// A switch away from an account that was not used in this window says
			// nothing about its limit.
			continue
		}
		out = append(out, Observation{Account: k.acct, Plan: byKey[k.acct].Plan, Start: start, Reset: k.reset,
			At: best.at, Percent: best.percent, Source: best.source, Units: u, Dollars: d})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Reset < out[j].Reset })
	sort.Strings(notes)
	return out, notes, nil
}

// Hypothesis is a set of weights: each kind's weight is Multiplier × its API ratio.
type Hypothesis struct {
	Multiplier Units
	Weights    Units
	PerPercent float64 // mean units per 1% across observations
	Spread     float64 // coefficient of variation of units per 1%
}

// Grid of multipliers tried for the cache kinds. Input tokens are too few to matter
// and keep their API ratio; output is the unit.
var (
	cacheWriteGrid = []float64{0, 0.25, 0.5, 1, 2, 4}
	cacheReadGrid  = []float64{0, 0.1, 0.25, 0.5, 1, 2, 4}
)

// Evaluate scores one set of multipliers on the observations.
func Evaluate(obs []Observation, m Units) Hypothesis {
	h := Hypothesis{Multiplier: m}
	for k := range h.Weights {
		h.Weights[k] = m[k] * APIWeights[k]
	}
	if len(obs) == 0 {
		return h
	}
	per := make([]float64, len(obs))
	var sum float64
	for i, o := range obs {
		var u float64
		for k := range o.Units {
			u += h.Weights[k] * o.Units[k]
		}
		per[i] = u / o.Percent
		sum += per[i]
	}
	mean := sum / float64(len(per))
	var ss float64
	for _, p := range per {
		ss += (p - mean) * (p - mean)
	}
	h.PerPercent = mean
	if mean > 0 {
		h.Spread = math.Sqrt(ss/float64(len(per))) / mean
	}
	return h
}

// PerPercent is what 1% of the weekly figure was worth in one observation under the
// given weights.
func PerPercent(o Observation, w Units) float64 {
	var u float64
	for k := range o.Units {
		u += w[k] * o.Units[k]
	}
	return u / o.Percent
}

// Fit ranks every multiplier combination of the grid, best (lowest spread) first.
func Fit(obs []Observation) []Hypothesis {
	var out []Hypothesis
	for _, w := range cacheWriteGrid {
		for _, r := range cacheReadGrid {
			out = append(out, Evaluate(obs, Units{Input: 1, CacheWrite: w, CacheRead: r, Output: 1}))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Spread < out[j].Spread })
	return out
}

// References are named hypotheses shown next to the fit for comparison.
var References = []struct {
	Name       string
	Multiplier Units
}{
	{"API prices", Units{1, 1, 1, 1}},
	{"output only", Units{0, 0, 0, 1}},
	{"cache reads free", Units{1, 1, 0, 1}},
}

func name(a store.Account) string {
	if a.Key == "" {
		return "unknown"
	}
	return a.Name()
}

func stamp(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04 UTC") }

func roundMinute(ms int64) int64 { return time.UnixMilli(ms).Round(time.Minute).UnixMilli() }

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
