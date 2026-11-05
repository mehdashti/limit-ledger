package report

import (
	"fmt"
	"io"
	"math"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/mehdashti/limit-ledger/internal/calibrate"
	"github.com/mehdashti/limit-ledger/internal/detect"
	"github.com/mehdashti/limit-ledger/internal/store"
)

// DetectOptions configures change detection.
type DetectOptions struct {
	calibrate.Options
	Threshold float64 // fraction, e.g. 0.15
	// Notify, when set, is called once for every new alert.
	Notify func(title, body string) bool
	// Quiet prints only new alerts.
	Quiet bool
	Loc   *time.Location
}

// Detect compares every week with the others, prints the comparison, and raises
// alerts for the newest week of each account and for usage seen elsewhere. Each
// alert is raised once.
func Detect(st *store.Store, w io.Writer, opt DetectOptions) error {
	if opt.Loc == nil {
		opt.Loc = time.Local
	}
	if opt.Threshold <= 0 {
		opt.Threshold = 0.15
	}
	if opt.MinPercent <= 0 {
		opt.MinPercent = 20
	}
	names, _, err := loadNames(st)
	if err != nil {
		return err
	}
	obs, _, err := calibrate.Collect(st, opt.Options)
	if err != nil {
		return err
	}
	// One fixed set of weights: the newest saved fit for the plan, or else a fit on all
	// observations of the span.
	weights := map[string]calibrate.Units{}
	source := map[string]string{}
	fits, err := st.Fits()
	if err != nil {
		return err
	}
	for _, f := range fits {
		weights[f.Plan] = calibrate.Units{calibrate.Input: f.Input, calibrate.CacheWrite: f.CacheWrite,
			calibrate.CacheRead: f.CacheRead, calibrate.Output: f.Output}
		source[f.Plan] = "saved fit of " + time.UnixMilli(f.TS).In(opt.Loc).Format("2006-01-02 15:04")
	}
	byPlan := map[string][]calibrate.Observation{}
	for _, o := range obs {
		byPlan[o.Plan] = append(byPlan[o.Plan], o)
	}
	var findings []detect.Finding
	for plan, po := range byPlan {
		wt, ok := weights[plan]
		if !ok {
			wt = calibrate.Fit(po)[0].Weights
			source[plan] = "fit on these weeks (no saved fit yet; run calibrate --save)"
		}
		findings = append(findings, detect.Check(po, wt, opt.Threshold)...)
		if !opt.Quiet {
			fmt.Fprintf(w, "plan %s: weights from the %s: cache write %.4g, cache read %.4g, input %.4g, output 1\n",
				dash(plan), source[plan], wt[calibrate.CacheWrite], wt[calibrate.CacheRead], wt[calibrate.Input])
		}
	}
	if !opt.Quiet && len(byPlan) > 0 {
		fmt.Fprintln(w)
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].Obs.Reset < findings[j].Obs.Reset })
	rises, err := detect.IdleRises(st, 1, 2)
	if err != nil {
		return err
	}
	when := func(ms int64) string { return time.UnixMilli(ms).In(opt.Loc).Format("Mon 01-02 15:04") }
	pct := func(d float64) string { return fmt.Sprintf("%+.0f%%", d*100) }

	if !opt.Quiet {
		if len(findings) == 0 {
			fmt.Fprintln(w, "No weeks to compare yet.")
		} else {
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "account\twindow resets\treading\t1% this week\t1% earlier weeks\tdeviation\tunder API prices\tunder output only\tverdict")
			for _, f := range findings {
				o := f.Obs
				if f.Verdict == detect.TooFew {
					fmt.Fprintf(tw, "%s\t%s\t%.0f%%\t-\t-\t-\t-\t-\t%s\n", names.of(o.Account), when(o.Reset), o.Percent, f.Verdict)
					continue
				}
				fmt.Fprintf(tw, "%s\t%s\t%.0f%%\t%s\t%s\t%s\t%s\t%s\t%s\n", names.of(o.Account), when(o.Reset), o.Percent,
					Tokens(int64(f.Observed)), Tokens(int64(f.Expected)), pct(f.Deviation),
					pct(f.ByReference["API prices"]), pct(f.ByReference["output only"]), f.Verdict)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			fmt.Fprintf(w, "\nEach week is compared with the median of up to %d earlier weeks of its plan, all measured with the\nweights above.\n", detect.Baseline)
			fmt.Fprintf(w, "A deviation of %.0f%% or more is a change. \"budget\": the same under every weighting, so the whole\n", opt.Threshold*100)
			fmt.Fprintln(w, "limit moved (or, if 1% got cheaper, the account was also used elsewhere). \"weighting\": it depends")
			fmt.Fprintln(w, "on how cache tokens count, so the token mix or the weights changed.")
		}
		for _, r := range rises {
			fmt.Fprintf(w, "\nusage elsewhere: %s's weekly figure rose %.0f points (%s → %s) while this machine used it for $%s.\n",
				names.of(r.Account), r.Rise, when(r.From.TS), when(r.To.TS), Thousands(r.LocalDollars))
		}
	}

	// Alerts: the newest week of each account, and every rise seen elsewhere.
	newest := map[string]detect.Finding{}
	for _, f := range findings {
		if cur, ok := newest[f.Obs.Account]; !ok || f.Obs.Reset > cur.Obs.Reset {
			newest[f.Obs.Account] = f
		}
	}
	type alert struct{ key, msg string }
	var alerts []alert
	for acct, f := range newest {
		if f.Verdict != detect.Budget && f.Verdict != detect.Weighting {
			continue
		}
		var msg string
		size := math.Abs(f.Deviation) * 100
		switch {
		case f.Verdict == detect.Budget && f.Cheaper():
			msg = fmt.Sprintf("%s, week resetting %s: 1%% of the weekly limit took %.0f%% fewer tokens than in earlier weeks, "+
				"under every weighting. The limit looks smaller, or the account was also used elsewhere.",
				names.of(acct), when(f.Obs.Reset), size)
		case f.Verdict == detect.Budget:
			msg = fmt.Sprintf("%s, week resetting %s: 1%% of the weekly limit took %.0f%% more tokens than in earlier weeks, "+
				"under every weighting. The limit looks larger.", names.of(acct), when(f.Obs.Reset), size)
		default:
			msg = fmt.Sprintf("%s, week resetting %s: the cost of 1%% differs by %.0f%% from earlier weeks under the fitted "+
				"weights, but not under every weighting. The token mix changed, or the limit now weighs some tokens differently.",
				names.of(acct), when(f.Obs.Reset), size)
		}
		alerts = append(alerts, alert{fmt.Sprintf("change|%s|%d|%s", acct, f.Obs.Reset, f.Verdict), msg})
	}
	for _, r := range rises {
		alerts = append(alerts, alert{fmt.Sprintf("elsewhere|%s|%d", r.Account, r.From.TS),
			fmt.Sprintf("%s: the weekly figure rose %.0f points between %s and %s while this machine used it for $%s. "+
				"It was used elsewhere (another machine or claude.ai).", names.of(r.Account), r.Rise, when(r.From.TS),
				when(r.To.TS), Thousands(r.LocalDollars))})
	}
	for _, a := range alerts {
		isNew, err := st.RaiseAlert(a.key, opt.Now, a.msg)
		if err != nil {
			return err
		}
		if !isNew {
			continue
		}
		fmt.Fprintln(w, "\nALERT:", a.msg)
		if opt.Notify != nil {
			opt.Notify("limit-ledger", a.msg)
		}
	}
	return nil
}

// Alerts prints the alerts raised so far, newest first.
func Alerts(st *store.Store, w io.Writer, loc *time.Location) error {
	list, err := st.Alerts()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(w, "No alerts so far.")
		return nil
	}
	for _, a := range list {
		fmt.Fprintf(w, "%s  %s\n", time.UnixMilli(a.TS).In(loc).Format("2006-01-02 15:04"), a.Message)
	}
	return nil
}
