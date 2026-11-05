package merge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/mehdashti/limit-ledger/internal/store"
)

// SSH is the ssh program; tests replace it.
var SSH = "ssh"

// ErrUnreachable means the other machine could not be reached (off, asleep, away).
var ErrUnreachable = errors.New("host unreachable")

// PullOptions configures a pull.
type PullOptions struct {
	Host      string        // an ssh destination, e.g. 192.168.1.21 or a Host from ~/.ssh/config
	RemoteBin string        // limit-ledger on the other machine
	Days      int           // how much history to fetch
	Timeout   time.Duration // for the whole transfer
}

// Pull runs ingest and usage-export on the other machine over ssh, without a
// password prompt, and imports the result.
func Pull(st *store.Store, opt PullOptions, nowMs int64) (ImportStats, error) {
	if opt.RemoteBin == "" {
		opt.RemoteBin = "~/.local/bin/limit-ledger"
	}
	if opt.Days <= 0 {
		opt.Days = 14
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 2 * time.Minute
	}
	if strings.ContainsAny(opt.RemoteBin, ";|&`$()<>\"'\n") {
		return ImportStats{}, fmt.Errorf("remote binary path %q has shell characters", opt.RemoteBin)
	}
	remote := fmt.Sprintf("%s ingest >/dev/null && %s usage-export --days %d", opt.RemoteBin, opt.RemoteBin, opt.Days)
	ctx, cancel := context.WithTimeout(context.Background(), opt.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, SSH, "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", opt.Host, remote)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 255 {
			return ImportStats{}, fmt.Errorf("%w: %s", ErrUnreachable, lastLine(errb.String()))
		}
		return ImportStats{}, fmt.Errorf("on %s: %v: %s", opt.Host, err, lastLine(errb.String()))
	}
	var b Bundle
	dec := json.NewDecoder(&out)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return ImportStats{}, fmt.Errorf("reading the bundle from %s: %v", opt.Host, err)
	}
	return Import(st, b, nowMs)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
