package report

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/mehdashti/limit-ledger/internal/calibrate"
	"github.com/mehdashti/limit-ledger/internal/store"
)

// CalibrateOptions configures the calibration report.
type CalibrateOptions struct {
	calibrate.Options
	Save bool
	Top  int
	Loc  *time.Location
}

// Calibrate prints the observations and the best-fitting weights per plan, and
// optionally saves the best fit of each plan.
func Calibrate(st *store.Store, w io.Writer, opt CalibrateOptions) error {
	if opt.Loc == nil {
		opt.Loc = time.Local
	}
	if opt.Top <= 0 {
		opt.Top = 5
	}
	names, _, err := loadNames(st)
	if err != nil {
		return err
	}
	obs, notes, err := calibrate.Collect(st, opt.Options)
	if err != nil {
		return err
	}
	if len(obs) == 0 {
		fmt.Fprintln(w, "No observations yet: no weekly window with a limit hit, a switch at the limit, or a reading.")
		for _, n := range notes {
			fmt.Fprintln(w, "note:", n)
		}
		return nil
	}
	byPlan := map[string][]calibrate.Observation{}
	var plans []string
	for _, o := range obs {
		if _, ok := byPlan[o.Plan]; !ok {
			plans = append(plans, o.Plan)
		}
		byPlan[o.Plan] = append(byPlan[o.Plan], o)
	}
	sort.Strings(plans)
	when := func(ms int64) string { return time.UnixMilli(ms).In(opt.Loc).Format("Mon 01-02 15:04") }
	for _, plan := range plans {
		po := byPlan[plan]
		fmt.Fprintf(w, "plan %s: %d observations\n\n", dash(plan), len(po))
		fits := calibrate.Fit(po)
		best := fits[0]
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "account\twindow resets\treading\tat\toutput\tcache write\tcache read\tAPI-equiv $\t1% (best fit)\tvs mean")
		for _, o := range po {
			pp := calibrate.PerPercent(o, best.Weights)
			dev := "-"
			if best.PerPercent > 0 {
				dev = fmt.Sprintf("%+.0f%%", (pp/best.PerPercent-1)*100)
			}
			fmt.Fprintf(tw, "%s\t%s\t%.0f%% (%s)\t%s\t%s\t%s\t%s\t$%s\t%s\t%s\n", names.of(o.Account), when(o.Reset),
				o.Percent, o.Source, when(o.At), Tokens(int64(o.Units[calibrate.Output])),
				Tokens(int64(o.Units[calibrate.CacheWrite])), Tokens(int64(o.Units[calibrate.CacheRead])),
				Thousands(o.Dollars), Tokens(int64(pp)), dev)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Fprintf(w, "\nweights relative to one Opus 5.5 output token, best fits first:\n")
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
		fmt.Fprintln(tw, "\tcache write\tcache read\tinput\toutput\t1% of the week\tspread\t")
		line := func(label string, h calibrate.Hypothesis) {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s units\t%.1f%%\t\n", label,
				weight(h, calibrate.CacheWrite), weight(h, calibrate.CacheRead), weight(h, calibrate.Input),
				weight(h, calibrate.Output), Tokens(int64(h.PerPercent)), h.Spread*100)
		}
		for i, h := range fits {
			if i == opt.Top {
				break
			}
			line(fmt.Sprintf("fit #%d", i+1), h)
		}
		for _, r := range calibrate.References {
			line(r.Name, calibrate.Evaluate(po, r.Multiplier))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Fprintf(w, "\nBest fit: 1%% of the weekly limit ≈ %s units, e.g. %s Opus 5.5 output tokens if nothing else were used.\n",
			Tokens(int64(best.PerPercent)), Tokens(int64(best.PerPercent)))
		fmt.Fprintln(w, "\"×N API\" is the weight as a multiple of the API price ratio; spread is how much the cost of 1%")
		fmt.Fprintln(w, "varies across the observations under those weights (lower fits better).")
		fmt.Fprintln(w, "\"vs mean\" shows each week against the others: a week far off means a different budget that week,")
		fmt.Fprintln(w, "or usage this machine does not see (another machine, claude.ai).")
		if len(po) < 4 {
			fmt.Fprintf(w, "Only %d observations: treat this as a first estimate; it firms up as weeks are added.\n", len(po))
		}
		if opt.Save {
			f := store.Fit{TS: opt.Now, Plan: plan, SinceTS: opt.Since, Observations: len(po),
				Input: best.Weights[calibrate.Input], CacheWrite: best.Weights[calibrate.CacheWrite],
				CacheRead: best.Weights[calibrate.CacheRead], Output: best.Weights[calibrate.Output],
				PerPercent: best.PerPercent, Spread: best.Spread}
			if err := st.SaveFit(f); err != nil {
				return err
			}
			fmt.Fprintln(w, "Saved this fit (see limit-ledger report fits).")
		}
		fmt.Fprintln(w)
	}
	for _, n := range notes {
		fmt.Fprintln(w, "note:", n)
	}
	fmt.Fprintln(w, "Usage on other machines or in claude.ai also moves the weekly figure; it is not in these counts.")
	return nil
}

func weight(h calibrate.Hypothesis, k int) string {
	if k == calibrate.Output {
		return "1"
	}
	if h.Multiplier[k] == 0 {
		return "0"
	}
	return fmt.Sprintf("%.4g (×%g API)", h.Weights[k], h.Multiplier[k])
}

// Fits prints the saved fits, so changes in the weights or the budget show over time.
func Fits(st *store.Store, w io.Writer, loc *time.Location) error {
	fits, err := st.Fits()
	if err != nil {
		return err
	}
	if len(fits) == 0 {
		fmt.Fprintln(w, "No saved fits yet. Run limit-ledger calibrate --save.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "saved\tplan\tobservations\tcache write\tcache read\tinput\t1% of the week\tspread")
	for _, f := range fits {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%.4g\t%.4g\t%.4g\t%s units\t%.1f%%\n",
			time.UnixMilli(f.TS).In(loc).Format("2006-01-02 15:04"), dash(f.Plan), f.Observations,
			f.CacheWrite, f.CacheRead, f.Input, Tokens(int64(f.PerPercent)), f.Spread*100)
	}
	return tw.Flush()
}
