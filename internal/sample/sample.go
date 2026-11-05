// Package sample records the utilization figures that Claude Code hands to its
// statusline command. It makes no request of its own: Claude Code already received
// the figures with its last API response.
//
// The statusline hook must be fast and must never break the user's statusline, so
// every function here degrades quietly.
package sample

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Limit is one entry of the statusline's rate_limits object.
type Limit struct {
	UsedPercentage *float64 `json:"used_percentage"` // percent, one decimal place
	ResetsAt       *float64 `json:"resets_at"`       // Unix seconds
}

// Payload is the part of the statusline input that limit-ledger reads.
type Payload struct {
	SessionID  string `json:"session_id"`
	Version    string `json:"version"`
	RateLimits *struct {
		FiveHour *Limit `json:"five_hour"`
		SevenDay *Limit `json:"seven_day"`
	} `json:"rate_limits"`
}

// Sample is one recorded reading. Times are Unix milliseconds.
type Sample struct {
	TS         int64    `json:"ts"`
	SessionID  string   `json:"session_id,omitempty"`
	FivePct    *float64 `json:"five_pct,omitempty"`
	FiveReset  *int64   `json:"five_reset,omitempty"`
	SevenPct   *float64 `json:"seven_pct,omitempty"`
	SevenReset *int64   `json:"seven_reset,omitempty"`
	Version    string   `json:"version,omitempty"`
}

// FromPayload turns statusline input into a sample. ok is false when the input
// carries no utilization figures, for example when not signed in with a plan.
func FromPayload(p Payload, now time.Time) (Sample, bool) {
	s := Sample{TS: now.UnixMilli(), SessionID: p.SessionID, Version: p.Version}
	if p.RateLimits == nil {
		return s, false
	}
	read := func(l *Limit) (*float64, *int64) {
		if l == nil {
			return nil, nil
		}
		var reset *int64
		if l.ResetsAt != nil && *l.ResetsAt > 0 {
			ms := int64(*l.ResetsAt * 1000)
			reset = &ms
		}
		return l.UsedPercentage, reset
	}
	s.FivePct, s.FiveReset = read(p.RateLimits.FiveHour)
	s.SevenPct, s.SevenReset = read(p.RateLimits.SevenDay)
	return s, s.FivePct != nil || s.SevenPct != nil
}

// Append adds one sample to the spool file. A single short write with O_APPEND is
// safe when several sessions run their statuslines at the same time.
func Append(path string, s Sample) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// Render is limit-ledger's own statusline text.
func Render(s Sample, ok bool, loc *time.Location) string {
	if !ok {
		return "limit-ledger: no limit data yet"
	}
	var parts []string
	if s.SevenPct != nil {
		p := fmt.Sprintf("week %.1f%%", *s.SevenPct)
		if s.SevenReset != nil {
			p += " (resets " + time.UnixMilli(*s.SevenReset).In(loc).Format("Mon 15:04") + ")"
		}
		parts = append(parts, p)
	}
	if s.FivePct != nil {
		parts = append(parts, fmt.Sprintf("5h %.1f%%", *s.FivePct))
	}
	return strings.Join(parts, " · ")
}

// Chain remembers the statusline that was configured before limit-ledger's, so the
// hook can keep showing it.
type Chain struct {
	Previous json.RawMessage `json:"previous,omitempty"`
}

// LoadChain reads the chain file. A missing or broken file means no previous statusline.
func LoadChain(path string) Chain {
	var c Chain
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

// SaveChain writes the chain file.
func SaveChain(path string, c Chain) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// RunPrevious runs the previous statusline command with the same input and returns
// its output. ok is false when there is none or it failed.
func RunPrevious(prev json.RawMessage, stdin []byte, timeout time.Duration) (string, bool) {
	if len(prev) == 0 {
		return "", false
	}
	var sl struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(prev, &sl); err != nil || sl.Type != "command" || strings.TrimSpace(sl.Command) == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", sl.Command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", sl.Command)
	}
	cmd.Stdin = bytes.NewReader(stdin)
	// A child of the command can keep stdout open after the timeout kills the shell;
	// stop waiting for it shortly after, so the statusline never hangs.
	cmd.WaitDelay = 100 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}
