// Command limit-ledger measures Claude Code usage against the subscription limits.
// It is unofficial and not affiliated with Anthropic.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "time/tzdata" // reset times name IANA zones; embed them for systems without zoneinfo

	"github.com/mehdashti/limit-ledger/internal/calibrate"
	"github.com/mehdashti/limit-ledger/internal/ingest"
	"github.com/mehdashti/limit-ledger/internal/notify"
	"github.com/mehdashti/limit-ledger/internal/paths"
	"github.com/mehdashti/limit-ledger/internal/report"
	"github.com/mehdashti/limit-ledger/internal/sample"
	"github.com/mehdashti/limit-ledger/internal/settings"
	"github.com/mehdashti/limit-ledger/internal/store"
)

var version = "dev"

const usage = `limit-ledger measures Claude Code usage against the subscription limits (unofficial).

Usage:
  limit-ledger ingest          [--db PATH] [--projects DIR] [--workers N]
  limit-ledger report weekly   [--db PATH] [--weeks N] [--by-account]
  limit-ledger report anchors  [--db PATH]
  limit-ledger report windows  [--db PATH]
  limit-ledger report compare  --from DATE --to DATE [--vs-weeks N | --vs-from DATE --vs-to DATE]
                               [--account NAME] [--threshold PCT] [--db PATH]
  limit-ledger accounts        [--db PATH]
  limit-ledger accounts set KEY [--label NAME] [--plan PLAN] [--switch-full] [--db PATH]
  limit-ledger calibrate        [--weeks N] [--save] [--top N] [--db PATH]
  limit-ledger report fits     [--db PATH]
  limit-ledger detect          [--weeks N] [--threshold PCT] [--notify] [--quiet] [--db PATH]
  limit-ledger report alerts   [--db PATH]
  limit-ledger statusline install | uninstall | status
  limit-ledger sample add --weekly PCT --resets TIME [--five PCT --five-resets TIME]
  limit-ledger version

The statusline hook (installed with "statusline install") records the weekly and 5-hour
utilization that Claude Code shows, without any request of its own.

ingest reads Claude Code transcripts (usage fields only) into a local database.
The first run reads all history; later runs read what was appended.
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		var u usageError
		if errors.As(err, &u) {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "limit-ledger:", err)
		os.Exit(1)
	}
}

type usageError struct{ msg string }

func (u usageError) Error() string { return u.msg }

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError{"no command"}
	}
	switch args[0] {
	case "ingest":
		return cmdIngest(args[1:], stdout, stderr)
	case "report":
		if len(args) < 2 {
			return usageError{"report needs weekly or anchors"}
		}
		return cmdReport(args[1], args[2:], stdout, stderr)
	case "statusline":
		return cmdStatusline(args[1:], os.Stdin, stdout, stderr)
	case "calibrate":
		return cmdCalibrate(args[1:], stdout, stderr)
	case "detect":
		return cmdDetect(args[1:], stdout, stderr)
	case "sample":
		return cmdSample(args[1:], stdout, stderr)
	case "accounts":
		return cmdAccounts(args[1:], stdout, stderr)
	case "version", "--version":
		fmt.Fprintln(stdout, "limit-ledger", version)
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	}
	return usageError{"unknown command " + args[0]}
}

func dbFlag(fs *flag.FlagSet) *string {
	def, _ := paths.DefaultDB()
	return fs.String("db", def, "database file")
}

func cmdIngest(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := dbFlag(fs)
	defProj, _ := paths.DefaultProjects()
	projects := fs.String("projects", defProj, "Claude Code projects directory")
	workers := fs.Int("workers", 0, "parallel file readers (0 = number of CPUs)")
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	st, err := store.Open(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	stats, err := ingest.Run(st, ingest.Options{Root: *projects, Workers: *workers})
	var ss ingest.SampleStats
	if spool, perr := paths.Spool(); perr == nil && err == nil {
		ss, err = ingest.Samples(st, spool)
	}
	resp, anchors, files, cerr := st.Counts()
	fmt.Fprintf(stdout, "read %d of %d files, %.1f MB, %d lines in %s\n",
		stats.FilesRead, stats.Files, float64(stats.Bytes)/1e6, stats.Lines, stats.Elapsed.Round(time.Millisecond))
	fmt.Fprintf(stdout, "parsed %d response records, %d limit anchors, %d account records, %d unparsable lines\n",
		stats.Responses, stats.Anchors, stats.OrgEvents, stats.ParseErrors)
	if ss.Read > 0 {
		fmt.Fprintf(stdout, "statusline samples: %d new, %d kept\n", ss.Read, ss.Kept)
	}
	if cerr == nil {
		fmt.Fprintf(stdout, "ledger now holds %d unique responses, %d anchors, %d files (%s)\n",
			resp, anchors, files, *db)
	}
	return err
}

func cmdReport(which string, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("report "+which, flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := dbFlag(fs)
	weeks := fs.Int("weeks", 8, "number of recent weeks (0 = all history)")
	byAccount := fs.Bool("by-account", false, "split each week by account")
	cmpFlags := compareFlags(fs)
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	st, err := openExisting(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	switch which {
	case "weekly":
		return report.Weekly(st, stdout, *weeks, time.Now(), *byAccount)
	case "anchors":
		return report.Anchors(st, stdout)
	case "windows":
		return report.WindowsReport(st, stdout, time.Local)
	case "compare":
		return cmdCompare(st, cmpFlags, stdout)
	case "fits":
		return report.Fits(st, stdout, time.Local)
	case "alerts":
		return report.Alerts(st, stdout, time.Local)
	}
	return usageError{"unknown report " + which}
}

func openExisting(db string) (*store.Store, error) {
	if _, err := os.Stat(db); err != nil {
		return nil, fmt.Errorf("no ledger at %s; run limit-ledger ingest first", db)
	}
	return store.Open(db)
}

func cmdAccounts(args []string, stdout, stderr io.Writer) error {
	setMode := len(args) > 0 && args[0] == "set"
	if setMode {
		args = args[1:]
	}
	fs := flag.NewFlagSet("accounts", flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := dbFlag(fs)
	label := fs.String("label", "", "a name for the account")
	plan := fs.String("plan", "", "the account's plan, for example pro, max, ultra, team")
	switchFull := fs.Bool("switch-full", false, "you switch away from this account only at its weekly limit (use --switch-full=false to undo)")
	// Accept the key before or after the flags.
	var key string
	if setMode && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		key, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if setMode && key == "" && fs.NArg() > 0 {
		key = fs.Arg(0)
	}
	st, err := openExisting(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	if !setMode {
		return report.Accounts(st, stdout)
	}
	if key == "" {
		return usageError{"accounts set needs an account key"}
	}
	var lp, pp *string
	var sp *bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "label":
			lp = label
		case "plan":
			pp = plan
		case "switch-full":
			sp = switchFull
		}
	})
	if lp == nil && pp == nil && sp == nil {
		return usageError{"accounts set needs --label, --plan and/or --switch-full"}
	}
	full, err := st.SetAccountPolicy(key, lp, pp, sp)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "updated account", full)
	return nil
}

func cmdStatusline(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		hook(stdin, stdout)
		return nil
	}
	settingsPath, err := paths.ClaudeSettings()
	if err != nil {
		return err
	}
	chain, err := paths.Chain()
	if err != nil {
		return err
	}
	switch args[0] {
	case "install":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if exe, err = filepath.EvalSymlinks(exe); err != nil {
			return err
		}
		if strings.Contains(exe, "go-build") {
			return fmt.Errorf("refusing to install a temporary binary (%s); build limit-ledger first", exe)
		}
		backup, err := settings.Install(settingsPath, chain, exe, time.Now())
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "statusline set to %s in %s\n", settings.HookCommand(exe), settingsPath)
		if backup != "" {
			fmt.Fprintln(stdout, "backup of the previous file:", backup)
		}
		if prev := sample.LoadChain(chain).Previous; len(prev) > 0 {
			fmt.Fprintln(stdout, "your previous statusline is kept and still shown")
		}
		return nil
	case "uninstall":
		backup, err := settings.Uninstall(settingsPath, chain, time.Now())
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, "statusline restored in", settingsPath)
		fmt.Fprintln(stdout, "backup of the previous file:", backup)
		return nil
	case "status":
		b, err := os.ReadFile(settingsPath)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		o, err := settings.Parse(b)
		if err != nil {
			return err
		}
		cur, ok := o.Get("statusLine")
		switch {
		case ok && settings.IsOurs(cur):
			fmt.Fprintln(stdout, "installed:", string(cur))
		case ok:
			fmt.Fprintln(stdout, "not installed; another statusline is configured:", string(cur))
		default:
			fmt.Fprintln(stdout, "not installed")
		}
		if spool, err := paths.Spool(); err == nil {
			if fi, err := os.Stat(spool); err == nil {
				fmt.Fprintf(stdout, "spool: %s (%d bytes, last write %s)\n", spool, fi.Size(), fi.ModTime().Format(time.RFC3339))
			} else {
				fmt.Fprintln(stdout, "spool: no samples recorded yet")
			}
		}
		return nil
	}
	return usageError{"statusline takes install, uninstall or status"}
}

// hook is what Claude Code runs on every statusline refresh. It records the figures,
// then shows the previous statusline if there was one, or limit-ledger's own line.
// It never fails: a broken statusline is worse than a missed sample.
func hook(stdin io.Reader, stdout io.Writer) {
	data, _ := io.ReadAll(io.LimitReader(stdin, 4<<20))
	var p sample.Payload
	_ = json.Unmarshal(data, &p)
	s, ok := sample.FromPayload(p, time.Now())
	if ok {
		if spool, err := paths.Spool(); err == nil {
			_ = sample.Append(spool, s)
		}
	}
	if chain, err := paths.Chain(); err == nil {
		if out, ran := sample.RunPrevious(sample.LoadChain(chain).Previous, data, 2*time.Second); ran {
			fmt.Fprint(stdout, out)
			return
		}
	}
	fmt.Fprintln(stdout, sample.Render(s, ok, time.Local))
}

// cmdSample records a utilization reading by hand, for clients that do not run the
// statusline command (the Claude desktop app), from the figures its usage card shows.
func cmdSample(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "add" {
		return usageError{"sample takes add"}
	}
	fs := flag.NewFlagSet("sample add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	weekly := fs.Float64("weekly", -1, "weekly percent used, for example 86")
	resets := fs.String("resets", "", "when the weekly limit resets, RFC 3339, for example 2026-11-08T22:00:00Z")
	five := fs.Float64("five", -1, "5-hour percent used, if the plan has a 5-hour limit")
	fiveResets := fs.String("five-resets", "", "when the 5-hour limit resets, RFC 3339")
	at := fs.String("at", "", "when the reading was taken, RFC 3339 (default now)")
	if err := fs.Parse(args[1:]); err != nil {
		return usageError{err.Error()}
	}
	s, err := manualSample(*weekly, *resets, *five, *fiveResets, *at, time.Now())
	if err != nil {
		return usageError{err.Error()}
	}
	spool, err := paths.Spool()
	if err != nil {
		return err
	}
	if err := sample.Append(spool, s); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "recorded: %s; run limit-ledger ingest to add it to the ledger\n", sample.Render(s, true, time.Local))
	return nil
}

func manualSample(weekly float64, resets string, five float64, fiveResets, at string, now time.Time) (sample.Sample, error) {
	s := sample.Sample{TS: now.UnixMilli(), SessionID: "manual"}
	if at != "" {
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return s, fmt.Errorf("--at: %v", err)
		}
		s.TS = t.UnixMilli()
	}
	// Reset times are whole seconds, as the statusline delivers them, so manual
	// readings fall into the same window as hook readings.
	parseReset := func(name, v string) (*int64, error) {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		ms := t.Truncate(time.Second).UnixMilli()
		return &ms, nil
	}
	if weekly < 0 || weekly > 100 || resets == "" {
		return s, fmt.Errorf("--weekly (0-100) and --resets are required")
	}
	w := weekly
	s.SevenPct = &w
	r, err := parseReset("--resets", resets)
	if err != nil {
		return s, err
	}
	s.SevenReset = r
	if five >= 0 {
		if five > 100 || fiveResets == "" {
			return s, fmt.Errorf("--five needs a value of 0-100 and --five-resets")
		}
		f := five
		s.FivePct = &f
		if s.FiveReset, err = parseReset("--five-resets", fiveResets); err != nil {
			return s, err
		}
	}
	return s, nil
}

type compareArgs struct {
	from, to, vsFrom, vsTo, account *string
	vsWeeks                         *int
	threshold                       *float64
}

func compareFlags(fs *flag.FlagSet) compareArgs {
	return compareArgs{
		from:      fs.String("from", "", "compare: start date (YYYY-MM-DD, local time) or RFC 3339 time"),
		to:        fs.String("to", "", "compare: end date, inclusive (YYYY-MM-DD) or RFC 3339 time; default now"),
		vsFrom:    fs.String("vs-from", "", "compare: baseline start"),
		vsTo:      fs.String("vs-to", "", "compare: baseline end, inclusive"),
		vsWeeks:   fs.Int("vs-weeks", 0, "compare: baseline is the N weeks right before --from (default 4)"),
		account:   fs.String("account", "", "compare: one account, by label or key (default: all usage)"),
		threshold: fs.Float64("threshold", 20, "compare: flag changes of at least this many percent"),
	}
}

// parseWhen reads a date (local midnight) or an RFC 3339 time. With end set, a bare
// date means the end of that day, so --to 2026-11-05 includes the 5th.
func parseWhen(v string, end bool, loc *time.Location) (time.Time, error) {
	if t, err := time.ParseInLocation("2006-01-02", v, loc); err == nil {
		if end {
			t = t.AddDate(0, 0, 1)
		}
		return t, nil
	}
	return time.Parse(time.RFC3339, v)
}

func comparePeriods(a compareArgs, now time.Time, loc *time.Location) (cur, base report.Period, err error) {
	if *a.from == "" {
		return cur, base, fmt.Errorf("compare needs --from")
	}
	if cur.From, err = parseWhen(*a.from, false, loc); err != nil {
		return cur, base, fmt.Errorf("--from: %v", err)
	}
	cur.To = now
	if *a.to != "" {
		if cur.To, err = parseWhen(*a.to, true, loc); err != nil {
			return cur, base, fmt.Errorf("--to: %v", err)
		}
	}
	if !cur.To.After(cur.From) {
		return cur, base, fmt.Errorf("--to must be after --from")
	}
	switch {
	case *a.vsFrom != "" || *a.vsTo != "":
		if *a.vsWeeks != 0 {
			return cur, base, fmt.Errorf("give either --vs-weeks or --vs-from/--vs-to")
		}
		if *a.vsFrom == "" || *a.vsTo == "" {
			return cur, base, fmt.Errorf("--vs-from and --vs-to go together")
		}
		if base.From, err = parseWhen(*a.vsFrom, false, loc); err != nil {
			return cur, base, fmt.Errorf("--vs-from: %v", err)
		}
		if base.To, err = parseWhen(*a.vsTo, true, loc); err != nil {
			return cur, base, fmt.Errorf("--vs-to: %v", err)
		}
		if !base.To.After(base.From) {
			return cur, base, fmt.Errorf("--vs-to must be after --vs-from")
		}
	default:
		n := *a.vsWeeks
		if n == 0 {
			n = 4
		}
		if n < 0 {
			return cur, base, fmt.Errorf("--vs-weeks must be positive")
		}
		base = report.Period{From: cur.From.AddDate(0, 0, -7*n), To: cur.From}
	}
	return cur, base, nil
}

func cmdCompare(st *store.Store, a compareArgs, stdout io.Writer) error {
	now := time.Now()
	cur, base, err := comparePeriods(a, now, time.Local)
	if err != nil {
		return usageError{err.Error()}
	}
	opt := report.CompareOptions{Period: cur, Base: base, Threshold: *a.threshold, Now: now, Loc: time.Local}
	if *a.account != "" {
		key, err := report.ResolveAccount(st, *a.account)
		if err != nil {
			return err
		}
		opt.Account, opt.ByAccount = key, true
	}
	return report.Compare(st, stdout, opt)
}

func cmdCalibrate(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := dbFlag(fs)
	weeks := fs.Int("weeks", 2, "use weekly windows that reset in the last N weeks (0 = all)")
	save := fs.Bool("save", false, "save the best fit of each plan")
	top := fs.Int("top", 5, "how many best fits to show")
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	st, err := openExisting(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	now := time.Now()
	since := int64(0)
	if *weeks > 0 {
		since = now.AddDate(0, 0, -7**weeks).UnixMilli()
	}
	return report.Calibrate(st, stdout, report.CalibrateOptions{
		Options: calibrate.Options{Since: since, Now: now.UnixMilli()},
		Save:    *save, Top: *top, Loc: time.Local,
	})
}

func cmdDetect(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("detect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := dbFlag(fs)
	weeks := fs.Int("weeks", 6, "compare weekly windows that reset in the last N weeks (0 = all)")
	threshold := fs.Float64("threshold", 15, "a deviation of at least this many percent is a change")
	notifyFlag := fs.Bool("notify", false, "show a desktop notification for each new alert")
	quiet := fs.Bool("quiet", false, "print only new alerts")
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	st, err := openExisting(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	now := time.Now()
	since := int64(0)
	if *weeks > 0 {
		since = now.AddDate(0, 0, -7**weeks).UnixMilli()
	}
	opt := report.DetectOptions{
		Options:   calibrate.Options{Since: since, Now: now.UnixMilli()},
		Threshold: *threshold / 100, Quiet: *quiet, Loc: time.Local,
	}
	if *notifyFlag {
		opt.Notify = notify.Send
	}
	return report.Detect(st, stdout, opt)
}
