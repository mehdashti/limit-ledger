package report

import (
	"fmt"
	"io"
	"math"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/mehdashti/limit-ledger/internal/sample"
	"github.com/mehdashti/limit-ledger/internal/store"
)

// Window is one account's weekly window as seen through utilization samples.
type Window struct {
	Account      string
	Resets       int64 // Unix ms
	Samples      int
	First, Last  sample.Sample
	Between      Summary // the account's local usage between the first and last sample
	SinceStart   Summary // the account's local usage from the window start to the last sample
	DeltaPercent float64
}

// PerPercent returns the API-equivalent dollars and output tokens per 1% of the
// weekly limit between the first and last sample. ok is false when the figure did
// not move enough to divide by.
func (w Window) PerPercent() (dollars, output float64, ok bool) {
	if w.DeltaPercent < 1 {
		return 0, 0, false
	}
	return w.Between.Dollars / w.DeltaPercent, float64(w.Between.Output) / w.DeltaPercent, true
}

// Windows groups samples by account and weekly reset and sums local usage for each.
func Windows(st *store.Store) ([]Window, error) {
	samples, err := st.Samples(math.MinInt64, math.MaxInt64)
	if err != nil {
		return nil, err
	}
	segs, err := st.Timeline()
	if err != nil {
		return nil, err
	}
	// The weekly reset time identifies an account's window: accounts reset on their own
	// schedules. So samples are grouped by reset time, and the window's account is the
	// one the timeline names for most of its samples. A sample taken just after a
	// switch, before the new account's first record, then cannot split a window.
	groups := map[int64][]sample.Sample{}
	var order []int64
	for _, s := range samples {
		if s.SevenPct == nil || s.SevenReset == nil {
			continue
		}
		r := *s.SevenReset
		if _, ok := groups[r]; !ok {
			order = append(order, r)
		}
		groups[r] = append(groups[r], s)
	}
	var out []Window
	for _, r := range order {
		g := groups[r]
		votes := map[string]int{}
		acct := ""
		for _, s := range g {
			a := store.AccountAt(segs, s.TS)
			votes[a]++
			if votes[a] > votes[acct] || (votes[a] == votes[acct] && acct == "") {
				acct = a
			}
		}
		w := Window{Account: acct, Resets: r, Samples: len(g), First: g[0], Last: g[len(g)-1]}
		w.DeltaPercent = *w.Last.SevenPct - *w.First.SevenPct
		by, err := usageBy(st, segs, w.First.TS, w.Last.TS)
		if err != nil {
			return nil, err
		}
		w.Between = by[acct]
		start := r - 7*24*int64(time.Hour/time.Millisecond)
		by, err = usageBy(st, segs, start, w.Last.TS)
		if err != nil {
			return nil, err
		}
		w.SinceStart = by[acct]
		out = append(out, w)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Last.TS < out[j].Last.TS })
	return out, nil
}

// WindowsReport prints the weekly windows that have utilization samples.
func WindowsReport(st *store.Store, w io.Writer, loc *time.Location) error {
	names, _, err := loadNames(st)
	if err != nil {
		return err
	}
	wins, err := Windows(st)
	if err != nil {
		return err
	}
	if len(wins) == 0 {
		fmt.Fprintln(w, "No utilization samples yet. Install the statusline hook (limit-ledger statusline install),")
		fmt.Fprintln(w, "use Claude Code for a while, then run limit-ledger ingest.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "account\twindow resets\tsamples\tweekly %\tlocal $ meanwhile\t$ per 1%\toutput per 1%\tsince window start: $ / %")
	for _, x := range wins {
		per, outPer := "-", "-"
		if d, o, ok := x.PerPercent(); ok {
			per, outPer = "$"+fmt.Sprintf("%.1f", d), Tokens(int64(o))
		}
		since := "-"
		if p := *x.Last.SevenPct; p >= 1 {
			since = fmt.Sprintf("$%s at %.1f%% = $%.1f per 1%%", Thousands(x.SinceStart.Dollars), p, x.SinceStart.Dollars/p)
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%.1f → %.1f\t$%s\t%s\t%s\t%s\n", names.of(x.Account),
			time.UnixMilli(x.Resets).In(loc).Format("Mon 01-02 15:04"), x.Samples,
			*x.First.SevenPct, *x.Last.SevenPct, Thousands(x.Between.Dollars), per, outPer, since)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(w, "\n\"$ per 1%\" divides the account's usage on this machine by the rise of its weekly figure.")
	fmt.Fprintln(w, "Usage on other machines or in claude.ai raises the figure too, so these are lower bounds.")
	return nil
}
