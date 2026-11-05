// Package report prints the ledger's text reports.
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
	"github.com/mehdashti/limit-ledger/internal/transcript"
)

// Summary is a sum of groups with their API-equivalent cost.
type Summary struct {
	Requests int64
	transcript.Tokens
	Dollars   float64
	ByModel   map[string]float64 // API-equivalent dollars per model
	Unpriced  map[string]bool    // models missing from the price table
	ReqsModel map[string]int64
}

// Summarize adds up groups.
func Summarize(groups []store.Group) Summary {
	s := Summary{ByModel: map[string]float64{}, Unpriced: map[string]bool{}, ReqsModel: map[string]int64{}}
	for _, g := range groups {
		s.Requests += g.Requests
		s.Tokens = s.Tokens.Add(g.Tokens)
		s.ReqsModel[g.Model] += g.Requests
		d, ok := price.Cost(g.Model, g.Speed, g.Tokens)
		if !ok {
			s.Unpriced[g.Model] = true
			continue
		}
		s.Dollars += d
		s.ByModel[g.Model] += d
	}
	return s
}

// TopModels lists the n most expensive models as "name $N".
func (s Summary) TopModels(n int) string {
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
		if i == n {
			break
		}
		parts = append(parts, fmt.Sprintf("%s $%s", strings.TrimPrefix(e.m, "claude-"), Thousands(e.d)))
	}
	for m := range s.Unpriced {
		parts = append(parts, strings.TrimPrefix(m, "claude-")+" (unpriced)")
	}
	return strings.Join(parts, ", ")
}

// Weekly prints one line per ISO week (UTC) for the last weeks weeks, or for all
// history when weeks <= 0.
func Weekly(st *store.Store, w io.Writer, weeks int, now time.Time) error {
	from := int64(0)
	if weeks > 0 {
		from = isoWeekStart(now.UTC()).AddDate(0, 0, -7*(weeks-1)).UnixMilli()
	}
	days, err := st.Daily(from, math.MaxInt64)
	if err != nil {
		return err
	}
	byWeek := map[string][]store.Group{}
	var keys []string
	for _, g := range days {
		d, err := time.Parse("2006-01-02", g.Day)
		if err != nil {
			return err
		}
		y, wk := d.ISOWeek()
		k := fmt.Sprintf("%d-W%02d", y, wk)
		if _, ok := byWeek[k]; !ok {
			keys = append(keys, k)
		}
		byWeek[k] = append(byWeek[k], g)
	}
	sort.Strings(keys)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "week\trequests\tinput\tcache write\tcache read\toutput\tAPI-equiv $\t")
	for _, k := range keys {
		s := Summarize(byWeek[k])
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t  %s\n", k, Thousands(float64(s.Requests)),
			Tokens(s.Input), Tokens(s.Cache5m+s.Cache1h), Tokens(s.CacheRead), Tokens(s.Output),
			Thousands(s.Dollars), s.TopModels(2))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "\nWeeks are ISO weeks in UTC. Dollars use API list prices as of %s; the subscription\n"+
		"limit weighs tokens its own way.\n", price.AsOf)
	return nil
}

// Anchors prints, for every weekly-limit hit, what was used from the start of that
// window up to the hit: the whole weekly budget in that week.
func Anchors(st *store.Store, w io.Writer) error {
	all, err := st.Anchors("weekly")
	if err != nil {
		return err
	}
	// Keep the earliest hit per reset time.
	var hits []transcript.Anchor
	for _, a := range all {
		if n := len(hits); n > 0 && hits[n-1].Resets == a.Resets {
			continue
		}
		hits = append(hits, a)
	}
	if len(hits) == 0 {
		fmt.Fprintln(w, "No weekly-limit hits found in the transcripts.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "hit (UTC)\tresets (UTC)\trequests\toutput\tcache read\tAPI-equiv $\tmodels\tnote")
	var prevReset int64
	for _, a := range hits {
		start := a.Resets - 7*24*int64(time.Hour/time.Millisecond)
		groups, err := st.Totals(start, a.Hit)
		if err != nil {
			return err
		}
		s := Summarize(groups)
		note := ""
		if prevReset > start {
			note = "overlaps previous window"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", stamp(a.Hit), stamp(a.Resets),
			Thousands(float64(s.Requests)), Tokens(s.Output), Tokens(s.CacheRead), Thousands(s.Dollars),
			s.TopModels(2), note)
		prevReset = a.Resets
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(w, "\nEach row sums usage from (reset - 7 days) to the hit, when the weekly limit was at 100%.")
	return nil
}

func stamp(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04") }

func isoWeekStart(t time.Time) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	wd := int(d.Weekday())
	if wd == 0 {
		wd = 7
	}
	return d.AddDate(0, 0, 1-wd)
}

// Tokens formats a token count compactly: 950, 12.3K, 108.0M, 12.15B.
func Tokens(n int64) string {
	f := float64(n)
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.2fB", f/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1fM", f/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.1fK", f/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// Thousands formats a number rounded to an integer with comma separators.
func Thousands(f float64) string {
	n := int64(math.Round(f))
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
