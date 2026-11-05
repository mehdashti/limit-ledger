// Command limit-ledger measures Claude Code usage against the subscription limits.
// It is unofficial and not affiliated with Anthropic.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	_ "time/tzdata" // reset times name IANA zones; embed them for systems without zoneinfo

	"github.com/mehdashti/limit-ledger/internal/ingest"
	"github.com/mehdashti/limit-ledger/internal/paths"
	"github.com/mehdashti/limit-ledger/internal/report"
	"github.com/mehdashti/limit-ledger/internal/store"
)

var version = "dev"

const usage = `limit-ledger measures Claude Code usage against the subscription limits (unofficial).

Usage:
  limit-ledger ingest          [--db PATH] [--projects DIR] [--workers N]
  limit-ledger report weekly   [--db PATH] [--weeks N] [--by-account]
  limit-ledger report anchors  [--db PATH]
  limit-ledger accounts        [--db PATH]
  limit-ledger accounts set KEY [--label NAME] [--plan PLAN] [--db PATH]
  limit-ledger version

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
	resp, anchors, files, cerr := st.Counts()
	fmt.Fprintf(stdout, "read %d of %d files, %.1f MB, %d lines in %s\n",
		stats.FilesRead, stats.Files, float64(stats.Bytes)/1e6, stats.Lines, stats.Elapsed.Round(time.Millisecond))
	fmt.Fprintf(stdout, "parsed %d response records, %d limit anchors, %d account records, %d unparsable lines\n",
		stats.Responses, stats.Anchors, stats.OrgEvents, stats.ParseErrors)
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
