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
// history when weeks <= 0. With byAccount, each week is split by the account that
// was active.
func Weekly(st *store.Store, w io.Writer, weeks int, now time.Time, byAccount bool) error {
	from := int64(0)
	if weeks > 0 {
		from = isoWeekStart(now.UTC()).AddDate(0, 0, -7*(weeks-1)).UnixMilli()
	}
	days, err := st.Daily(from, math.MaxInt64)
	if err != nil {
		return err
	}
	byWeek := map[string][]store.Group{}
	weekStart := map[string]time.Time{}
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
			weekStart[k] = isoWeekStart(d)
		}
		byWeek[k] = append(byWeek[k], g)
	}
	sort.Strings(keys)
	var (
		names accountNames
		segs  []store.Segment
	)
	if byAccount {
		if names, _, err = loadNames(st); err != nil {
			return err
		}
		if segs, err = st.Timeline(); err != nil {
			return err
		}
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	head := "week\trequests\tinput\tcache write\tcache read\toutput\tAPI-equiv $\t"
	if byAccount {
		head = "week\taccount\trequests\tinput\tcache write\tcache read\toutput\tAPI-equiv $\t"
	}
	fmt.Fprintln(tw, head)
	row := func(prefix string, s Summary) {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t  %s\n", prefix, Thousands(float64(s.Requests)),
			Tokens(s.Input), Tokens(s.Cache5m+s.Cache1h), Tokens(s.CacheRead), Tokens(s.Output),
			Thousands(s.Dollars), s.TopModels(2))
	}
	for _, k := range keys {
		if !byAccount {
			row(k, Summarize(byWeek[k]))
			continue
		}
		ws := weekStart[k]
		by, err := usageBy(st, segs, ws.UnixMilli(), ws.AddDate(0, 0, 7).UnixMilli())
		if err != nil {
			return err
		}
		accts := make([]string, 0, len(by))
		for a := range by {
			accts = append(accts, a)
		}
		sort.Slice(accts, func(i, j int) bool {
			if (accts[i] == "") != (accts[j] == "") {
				return accts[j] == "" // unknown last
			}
			return names.of(accts[i]) < names.of(accts[j])
		})
		for _, a := range accts {
			row(k+"\t"+names.of(a), by[a])
		}
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
	names, _, err := loadNames(st)
	if err != nil {
		return err
	}
	segs, err := st.Timeline()
	if err != nil {
		return err
	}
	hits, err := weeklyHits(st, segs)
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		fmt.Fprintln(w, "No weekly-limit hits found in the transcripts.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "hit (UTC)\tresets (UTC)\taccount\trequests\toutput\tcache read\tAPI-equiv $\tmodels\tnote")
	for _, h := range hits {
		start := h.resets - 7*24*int64(time.Hour/time.Millisecond)
		by, err := usageBy(st, segs, start, h.hit)
		if err != nil {
			return err
		}
		var s Summary
		var notes []string
		if h.account == "" {
			// Account unknown: all usage in the window, from whichever account.
			all, err := st.Totals(start, h.hit)
			if err != nil {
				return err
			}
			s = Summarize(all)
			notes = append(notes, "account unknown: sums every account")
		} else {
			s = by[h.account]
			lo, hi, err := switchRange(st, segs, h.account, start, h.hit)
			if err != nil {
				return err
			}
			if lo > 0 || hi > 0 {
				notes = append(notes, fmt.Sprintf("switch uncertainty: $%s–$%s",
					Thousands(s.Dollars-lo), Thousands(s.Dollars+hi)))
			}
			if u := by[""]; u.Requests > 0 {
				notes = append(notes, fmt.Sprintf("lower bound: $%s before account records", Thousands(u.Dollars)))
			}
			for acct, o := range by {
				if acct != "" && acct != h.account && o.Requests > 0 {
					notes = append(notes, fmt.Sprintf("%s used $%s meanwhile", names.of(acct), Thousands(o.Dollars)))
				}
			}
		}
		sort.Strings(notes)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", stamp(h.hit), stamp(h.resets), names.of(h.account),
			Thousands(float64(s.Requests)), Tokens(s.Output), Tokens(s.CacheRead), Thousands(s.Dollars),
			s.TopModels(2), strings.Join(notes, "; "))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(w, "\nEach row sums the hitting account's usage from (reset - 7 days) to the hit, when its weekly")
	fmt.Fprintln(w, "limit was at 100%. The account active at each moment comes from Claude Code's credential_org records.")
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
