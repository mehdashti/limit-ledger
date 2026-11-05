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
	"sort"
	"strings"
	"time"

	_ "time/tzdata" // reset times name IANA zones; embed them for systems without zoneinfo

	"github.com/mehdashti/limit-ledger/internal/calibrate"
	"github.com/mehdashti/limit-ledger/internal/ingest"
	"github.com/mehdashti/limit-ledger/internal/merge"
	"github.com/mehdashti/limit-ledger/internal/notify"
	"github.com/mehdashti/limit-ledger/internal/paths"
	"github.com/mehdashti/limit-ledger/internal/report"
	"github.com/mehdashti/limit-ledger/internal/sample"
	"github.com/mehdashti/limit-ledger/internal/settings"
	"github.com/mehdashti/limit-ledger/internal/share"
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
  limit-ledger export          [--weeks N] [--out DIR] [--note TEXT] [--db PATH]
  limit-ledger community pool DIR [--markdown] [--check]
  limit-ledger usage-export     [--days N] [--out FILE|-] [--db PATH]
  limit-ledger usage-import    FILE|- [--db PATH]
  limit-ledger machines        [--db PATH]
  limit-ledger pull HOST       [--remote-bin PATH] [--days N] [--db PATH]

To add another machine's usage of your accounts, for example over SSH:
  ssh other-machine limit-ledger usage-export | limit-ledger usage-import -
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
	case "usage-export":
		return cmdUsageExport(args[1:], stdout, stderr)
	case "usage-import":
		return cmdUsageImport(args[1:], os.Stdin, stdout, stderr)
	case "pull":
		return cmdPull(args[1:], stdout, stderr)
	case "machines":
		return cmdMachines(args[1:], stdout, stderr)
	case "export":
		return cmdExport(args[1:], stdout, stderr)
	case "community":
		return cmdCommunity(args[1:], stdout, stderr)
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

func cmdExport(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := dbFlag(fs)
	weeks := fs.Int("weeks", 4, "include weekly windows that reset in the last N weeks (0 = all)")
	out := fs.String("out", ".", "directory to write the table(s) to")
	note := fs.String("note", "", "a short note to include, for example how you use Claude Code")
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
	tables, err := share.Build(st, calibrate.Options{Since: since, Now: now.UnixMilli()}, version, *note)
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return fmt.Errorf("nothing to export: no observations in the last %d weeks (see limit-ledger calibrate)", *weeks)
	}
	for _, t := range tables {
		if t.Plan == "" {
			fmt.Fprintln(stdout, "skipped observations of accounts with no plan set: limit-ledger accounts set KEY --plan PLAN")
			continue
		}
		if err := t.Validate(); err != nil {
			return fmt.Errorf("built table does not validate: %v", err)
		}
		b, err := json.MarshalIndent(t, "", "  ")
		if err != nil {
			return err
		}
		path := filepath.Join(*out, fmt.Sprintf("limit-ledger-%s-%s.json", strings.ToLower(t.Plan), t.Created))
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "wrote %s: plan %s, %d account-weeks", path, t.Plan, len(t.Observations))
		if t.Fit != nil {
			fmt.Fprintf(stdout, ", own fit 1%% = %.0f units (spread %.1f%%)", t.Fit.PerPercent, t.Fit.Spread*100)
		}
		fmt.Fprintln(stdout)
	}
	fmt.Fprintln(stdout, "The file holds numbers only. Read it before you share it; see community/README.md.")
	return nil
}

func cmdCommunity(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "pool" {
		return usageError{"community takes pool"}
	}
	fs := flag.NewFlagSet("community pool", flag.ContinueOnError)
	fs.SetOutput(stderr)
	markdown := fs.Bool("markdown", false, "print a Markdown page (for community/TABLE.md)")
	check := fs.Bool("check", false, "exit with an error if any file is rejected (for CI)")
	rest := args[1:]
	dir := "community"
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		dir, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return usageError{err.Error()}
	}
	tables, bad, err := share.Load(dir)
	if err != nil {
		return err
	}
	if err := report.Pool(stdout, share.Pool(tables), bad, *markdown); err != nil {
		return err
	}
	if *check && len(bad) > 0 {
		return fmt.Errorf("%d file(s) rejected", len(bad))
	}
	return nil
}

func cmdUsageExport(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("usage-export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := dbFlag(fs)
	days := fs.Int("days", 60, "export the last N days")
	out := fs.String("out", "-", "file to write, or - for standard output")
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	st, err := openExisting(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	now := time.Now()
	b, stats, err := merge.Export(st, now.AddDate(0, 0, -*days).UnixMilli(), now.UnixMilli())
	if err != nil {
		return err
	}
	if stats.NoXID > 0 {
		return fmt.Errorf("%d responses belong to accounts without a cross-machine id; run limit-ledger ingest first", stats.NoXID)
	}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if *out == "-" {
		_, err = stdout.Write(data)
	} else {
		err = os.WriteFile(*out, data, 0o600)
	}
	if err == nil {
		fmt.Fprintf(stderr, "exported %d responses of %d account(s); %d from before account records were left out\n",
			stats.Rows, len(b.Accounts), stats.Unassigned)
	}
	return err
}

func cmdUsageImport(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("usage-import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := dbFlag(fs)
	src := "-"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") || len(args) > 0 && args[0] == "-" {
		src, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	var r io.Reader = stdin
	if src != "-" {
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	var b merge.Bundle
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return fmt.Errorf("reading bundle: %v", err)
	}
	st, err := openExisting(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	stats, err := merge.Import(st, b, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	names := map[string]string{}
	if accts, err := st.Accounts(); err == nil {
		for _, a := range accts {
			names[a.Key] = a.Name()
		}
	}
	fmt.Fprintf(stdout, "imported %d responses from machine %s\n", stats.Rows, b.Machine)
	for k, n := range stats.PerAccount {
		fmt.Fprintf(stdout, "  %s: %d\n", names[k], n)
	}
	for _, k := range stats.NewAccounts {
		fmt.Fprintf(stdout, "  new account %s (used only on the other machine so far)\n", names[k])
	}
	return nil
}

func cmdMachines(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("machines", flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := dbFlag(fs)
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	st, err := openExisting(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	own, err := st.MachineID()
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "this machine:", own)
	ms, err := st.Machines()
	if err != nil {
		return err
	}
	if len(ms) == 0 {
		fmt.Fprintln(stdout, "no other machine imported yet")
	}
	for _, m := range ms {
		fmt.Fprintf(stdout, "%s  %d responses, %s → %s, last import %s\n", m.ID, m.Responses,
			time.UnixMilli(m.First).Format("2006-01-02"), time.UnixMilli(m.Last).Format("2006-01-02"),
			time.UnixMilli(m.ImportedTS).Format("2006-01-02 15:04"))
	}
	return nil
}

func cmdPull(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := dbFlag(fs)
	remoteBin := fs.String("remote-bin", "~/.local/bin/limit-ledger", "limit-ledger on the other machine")
	days := fs.Int("days", 14, "how many days of the other machine's usage to fetch")
	var host string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		host, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if host == "" {
		return usageError{"pull needs a host, e.g. limit-ledger pull 192.168.1.21"}
	}
	st, err := openExisting(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	stats, err := merge.Pull(st, merge.PullOptions{Host: host, RemoteBin: *remoteBin, Days: *days}, time.Now().UnixMilli())
	if errors.Is(err, merge.ErrUnreachable) {
		// Not an error: the other machine is simply off or away.
		fmt.Fprintf(stdout, "skipped: %s is not reachable (%v)\n", host, err)
		return nil
	}
	if err != nil {
		return err
	}
	names := map[string]string{}
	if accts, err := st.Accounts(); err == nil {
		for _, a := range accts {
			names[a.Key] = a.Name()
		}
	}
	parts := []string{}
	for k, n := range stats.PerAccount {
		parts = append(parts, fmt.Sprintf("%s %d", names[k], n))
	}
	sort.Strings(parts)
	fmt.Fprintf(stdout, "pulled %d responses from %s (%s)\n", stats.Rows, host, strings.Join(parts, ", "))
	return nil
}
