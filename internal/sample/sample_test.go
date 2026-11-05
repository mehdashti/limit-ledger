package sample

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const payload = `{"session_id":"s1","version":"2.1.296","model":{"id":"claude-opus-5-5"},
"rate_limits":{"five_hour":{"used_percentage":23.4,"resets_at":1762340400},
"seven_day":{"used_percentage":61.3,"resets_at":1762639200}}}`

func TestFromPayload(t *testing.T) {
	var p Payload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	s, ok := FromPayload(p, now)
	if !ok || s.TS != now.UnixMilli() || s.SessionID != "s1" || s.Version != "2.1.296" {
		t.Fatalf("got %+v %v", s, ok)
	}
	if *s.SevenPct != 61.3 || *s.SevenReset != 1762639200000 || *s.FivePct != 23.4 || *s.FiveReset != 1762340400000 {
		t.Errorf("figures %+v", s)
	}
	if _, ok := FromPayload(Payload{SessionID: "x"}, now); ok {
		t.Error("no rate_limits must not give a sample")
	}
}

func TestAppendAndRender(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "samples.jsonl")
	pct, reset := 61.3, int64(1762639200000)
	s := Sample{TS: 1, SevenPct: &pct, SevenReset: &reset}
	for i := 0; i < 2; i++ {
		if err := Append(path, s); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(path)
	if strings.Count(string(b), "\n") != 2 || !strings.Contains(string(b), `"seven_pct":61.3`) {
		t.Errorf("spool content %q", b)
	}
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("spool mode %v", fi.Mode().Perm())
	}
	got := Render(s, true, time.UTC)
	if got != "week 61.3% (resets Sat 22:00)" {
		t.Errorf("render %q", got)
	}
	if Render(Sample{}, false, time.UTC) == "" {
		t.Error("render without data must still print something")
	}
}

func TestChain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	path := filepath.Join(t.TempDir(), "chain.json")
	if c := LoadChain(path); len(c.Previous) != 0 {
		t.Fatal("missing chain file must mean no previous statusline")
	}
	prev := json.RawMessage(`{"type":"command","command":"read x; printf 'prev:%s' \"$x\""}`)
	if err := SaveChain(path, Chain{Previous: prev}); err != nil {
		t.Fatal(err)
	}
	out, ok := RunPrevious(LoadChain(path).Previous, []byte("hello\n"), 2*time.Second)
	if !ok || out != "prev:hello" {
		t.Errorf("chained output %q %v", out, ok)
	}
	if _, ok := RunPrevious(json.RawMessage(`{"type":"command","command":"exit 3"}`), nil, time.Second); ok {
		t.Error("a failing previous command must report not ok")
	}
	if _, ok := RunPrevious(json.RawMessage(`{"type":"command","command":"sleep 5"}`), nil, 100*time.Millisecond); ok {
		t.Error("a slow previous command must time out")
	}
}
