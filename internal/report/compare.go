package report

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mehdashti/limit-ledger/internal/price"
	"github.com/mehdashti/limit-ledger/internal/store"
)

// Period is a half-open time range [From, To).
type Period struct {
	From, To time.Time
}

// Days is the length of the period in days, counting only time up to now.
func (p Period) Days(now time.Time) float64 {
	to := p.To
	if to.After(now) {
		to = now
	}
	d := to.Sub(p.From).Hours() / 24
	return math.Max(d, 0)
}

// CompareOptions selects what to compare.
type CompareOptions struct {
	Period, Base Period
	// Account is an account key, or "" for the time before account records exist.
	// It is used only when ByAccount is true; otherwise all usage is counted.
	Account   string
	ByAccount bool
	Threshold float64 // percent change that gets flagged
	Now       time.Time
	Loc       *time.Location
}

// usage is one side of a comparison.
type usage struct {
	Summary
	days   float64
	byKind map[string]float64 // API-equivalent dollars per token kind
}

var kinds = []string{"input", "cache write", "cache read", "output"}

func measure(st *store.Store, segs []store.Segment, opt CompareOptions, p Period) (usage, error) {
	from, to := p.From.UnixMilli(), p.To.UnixMilli()
	var groups []store.Group
	if !opt.ByAccount {
		g, err := st.Totals(from, to)
		if err != nil {
			return usage{}, err
		}
		groups = g
	} else {
		for _, sg := range segs {
			if sg.Account != opt.Account {
				continue
			}
			a, b := max(from, sg.Start), min(to, sg.End)
			if a >= b {
				continue
			}
			g, err := st.Totals(a, b)
			if err != nil {
				return usage{}, err
			}
			groups = append(groups, g...)
		}
	}
	u := usage{Summary: Summarize(groups), days: p.Days(opt.Now), byKind: map[string]float64{}}
	for _, g := range groups {
		r, ok := price.Lookup(g.Model)
		if !ok {
			continue
		}
		f := 1.0
		if g.Speed == "fast" {
			f = price.FastFactor
		}
		u.byKind["input"] += f * float64(g.Input) * r.Input / 1e6
		u.byKind["cache write"] += f * (float64(g.Cache5m)*r.Input*price.Cache5mFactor + float64(g.Cache1h)*r.Input*price.Cache1hFactor) / 1e6
		u.byKind["cache read"] += f * float64(g.CacheRead) * r.CacheRead / 1e6
		u.byKind["output"] += f * float64(g.Output) * r.Output / 1e6
	}
	return u, nil
}

func perDay(v, days float64) float64 {
	if days <= 0 {
		return 0
	}
	return v / days
}

// change returns the relative change of a over b in percent, and whether it is defined.
func change(a, b float64) (float64, bool) {
	if b == 0 {
		return 0, false
	}
	return (a - b) / b * 100, true
}

// Compare prints a period's daily averages against a baseline's.
func Compare(st *store.Store, w io.Writer, opt CompareOptions) error {
	if opt.Loc == nil {
		opt.Loc = time.Local
	}
	if opt.Threshold <= 0 {
		opt.Threshold = 20
	}
	names, _, err := loadNames(st)
	if err != nil {
		return err
	}
	segs, err := st.Timeline()
	if err != nil {
		return err
	}
	cur, err := measure(st, segs, opt, opt.Period)
	if err != nil {
		return err
	}
	base, err := measure(st, segs, opt, opt.Base)
	if err != nil {
		return err
	}
	scope := "all accounts"
	if opt.ByAccount {
		scope = "account " + names.of(opt.Account)
	}
	day := func(t time.Time) string { return t.In(opt.Loc).Format("2006-01-02 15:04") }
	length := func(p Period, days float64) string {
		if p.To.After(opt.Now) {
			return fmt.Sprintf("%.1f days so far", days)
		}
		return fmt.Sprintf("%.1f days", days)
	}
	fmt.Fprintf(w, "period:   %s → %s  (%s)  %s\n", day(opt.Period.From), day(opt.Period.To), length(opt.Period, cur.days), scope)
	fmt.Fprintf(w, "baseline: %s → %s  (%s)\n\n", day(opt.Base.From), day(opt.Base.To), length(opt.Base, base.days))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "per day\tperiod\tbaseline\tchange\t\t")
	row := func(name string, a, b float64, format func(float64) string) {
		pa, pb := perDay(a, cur.days), perDay(b, base.days)
		ch, flag := "-", ""
		if c, ok := change(pa, pb); ok {
			ch = fmt.Sprintf("%+.0f%%", c)
			if c >= opt.Threshold {
				flag = "▲"
			} else if c <= -opt.Threshold {
				flag = "▼"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t\n", name, format(pa), format(pb), ch, flag)
	}
	tok := func(v float64) string { return Tokens(int64(v)) }
	usd := func(v float64) string { return "$" + Thousands(v) }
	row("requests", float64(cur.Requests), float64(base.Requests), func(v float64) string { return Thousands(v) })
	row("input", float64(cur.Input), float64(base.Input), tok)
	row("cache write", float64(cur.Cache5m+cur.Cache1h), float64(base.Cache5m+base.Cache1h), tok)
	row("cache read", float64(cur.CacheRead), float64(base.CacheRead), tok)
	row("output", float64(cur.Output), float64(base.Output), tok)
	row("API-equiv $", cur.Dollars, base.Dollars, usd)
	if err := tw.Flush(); err != nil {
		return err
	}

	fmt.Fprintln(w, "\nshare of API-equivalent $ by token kind:")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "\tperiod\tbaseline\t")
	for _, k := range kinds {
		fmt.Fprintf(tw, "%s\t%s\t%s\t\n", k, share(cur.byKind[k], cur.Dollars), share(base.byKind[k], base.Dollars))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "\nmodels, period:   %s\n", modelShares(cur.Summary))
	fmt.Fprintf(w, "models, baseline: %s\n", modelShares(base.Summary))
	fmt.Fprintf(w, "\n▲/▼ mark a change of %.0f%% or more in the daily average. Dollars use API list prices as of %s;\n", opt.Threshold, price.AsOf)
	fmt.Fprintln(w, "the subscription limit weighs tokens its own way (see report windows for cost per 1%).")
	fmt.Fprintln(w, "Daily averages divide by calendar days, including days an account was not in use.")
	if opt.ByAccount && opt.Account != "" {
		if first := firstEvent(segs, opt.Account); first > opt.Base.From.UnixMilli() {
			fmt.Fprintf(w, "Note: account records start %s, so earlier usage in the baseline is not attributed to this account.\n",
				day(time.UnixMilli(first)))
		}
	}
	return nil
}

func share(part, total float64) string {
	if total <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", part/total*100)
}

func modelShares(s Summary) string {
	type mc struct {
		m string
		d float64
	}
	var l []mc
	for m, d := range s.ByModel {
		l = append(l, mc{m, d})
	}
	sort.Slice(l, func(i, j int) bool { return l[i].d > l[j].d })
	var parts []string
	for i, e := range l {
		if i == 4 || s.Dollars <= 0 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s %.0f%%", strings.TrimPrefix(e.m, "claude-"), e.d/s.Dollars*100))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func firstEvent(segs []store.Segment, acct string) int64 {
	for _, sg := range segs {
		if sg.Account == acct {
			return sg.Start
		}
	}
	return math.MaxInt64
}

// ResolveAccount finds an account by label (case-insensitive) or key prefix.
// "unknown" selects the time before account records exist.
func ResolveAccount(st *store.Store, name string) (string, error) {
	if strings.EqualFold(name, unknownAccount) {
		return "", nil
	}
	accts, err := st.Accounts()
	if err != nil {
		return "", err
	}
	var hits []string
	for _, a := range accts {
		if strings.EqualFold(a.Label, name) || strings.HasPrefix(a.Key, name) {
			hits = append(hits, a.Key)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return "", fmt.Errorf("no account labelled or keyed %q (see limit-ledger accounts)", name)
	}
	return "", fmt.Errorf("%q matches %d accounts", name, len(hits))
}
