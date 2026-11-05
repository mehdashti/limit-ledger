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

	"github.com/mehdashti/limit-ledger/internal/ingest"
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
  limit-ledger accounts        [--db PATH]
  limit-ledger accounts set KEY [--label NAME] [--plan PLAN] [--db PATH]
  limit-ledger statusline install | uninstall | status
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
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "label":
			lp = label
		case "plan":
			pp = plan
		}
	})
	if lp == nil && pp == nil {
		return usageError{"accounts set needs --label and/or --plan"}
	}
	full, err := st.SetAccount(key, lp, pp)
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
