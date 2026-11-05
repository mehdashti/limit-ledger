package price

import (
	"math"
	"testing"

	"github.com/mehdashti/limit-ledger/internal/transcript"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCost(t *testing.T) {
	tok := transcript.Tokens{Input: 1e6, Cache5m: 1e6, Cache1h: 1e6, CacheRead: 1e6, Output: 1e6}
	// Opus 5.5: 4 + 4*1.25 + 4*2 + 0.20 + 20
	if d, ok := Cost("claude-opus-5-5", "standard", tok); !ok || !near(d, 37.2) {
		t.Errorf("opus 5.5: %v %v", d, ok)
	}
	if d, ok := Cost("claude-opus-5-5", "fast", tok); !ok || !near(d, 74.4) {
		t.Errorf("opus 5.5 fast: %v %v", d, ok)
	}
	// Longest prefix wins: claude-opus-5 must not catch claude-opus-5-5, nor claude-fable-5 catch 5-1.
	if r, _ := Lookup("claude-opus-5"); r.Input != 5 {
		t.Errorf("opus 5 rate %+v", r)
	}
	if r, _ := Lookup("claude-fable-5-1"); r.CacheRead != 0.25 {
		t.Errorf("fable 5.1 rate %+v", r)
	}
	if r, _ := Lookup("claude-opus-4-1-20250805"); r.Input != 15 {
		t.Errorf("opus 4.1 rate %+v", r)
	}
	if _, ok := Cost("some-other-model", "", tok); ok {
		t.Error("unknown model should not be priced")
	}
}
