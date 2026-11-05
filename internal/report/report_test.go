package report

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mehdashti/limit-ledger/internal/store"
	"github.com/mehdashti/limit-ledger/internal/transcript"
)

func ms(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UnixMilli()
}

func TestAnchorsWindow(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	b, err := st.Begin()
	if err != nil {
		t.Fatal(err)
	}
	put := func(id, ts string, out int64) {
		r := &transcript.Response{MsgID: id, TS: ms(ts), Model: "claude-opus-5-5", Kind: "main",
			Tokens: transcript.Tokens{Output: out}}
		if err := b.PutResponse(r); err != nil {
			t.Fatal(err)
		}
	}
	// Window: resets 2026-10-13T07:00Z, so it starts 2026-10-06T07:00Z; hit at 2026-10-12T16:23Z.
	put("before", "2026-10-06T06:59:00Z", 1_000_000)
	put("in1", "2026-10-06T07:00:00Z", 1_000_000)
	put("in2", "2026-10-12T16:00:00Z", 1_000_000)
	put("after", "2026-10-12T16:30:00Z", 1_000_000)
	for _, a := range []transcript.Anchor{
		{Kind: "weekly", Hit: ms("2026-10-12T16:23:00Z"), Resets: ms("2026-10-13T07:00:00Z")},
		{Kind: "weekly", Hit: ms("2026-10-12T16:25:00Z"), Resets: ms("2026-10-13T07:00:00Z")}, // repeat of the same hit
	} {
		if err := b.PutAnchor(&a); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Anchors(st, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if n := strings.Count(text, "2026-10-12 16:2"); n != 1 {
		t.Errorf("want one row for the repeated hit, got %d:\n%s", n, text)
	}
	// Two responses in the window, 2M output tokens, $40 at Opus 5.5 output price.
	if !strings.Contains(text, "2.0M") || !strings.Contains(text, "opus-5-5 $40") {
		t.Errorf("window sums wrong:\n%s", text)
	}
}

func TestFormatting(t *testing.T) {
	for in, want := range map[int64]string{950: "950", 12_345: "12.3K", 108_000_000: "108.0M", 12_152_000_000: "12.15B"} {
		if got := Tokens(in); got != want {
			t.Errorf("Tokens(%d) = %s want %s", in, got, want)
		}
	}
	for in, want := range map[float64]string{0: "0", 999.6: "1,000", 1234567: "1,234,567", -2500: "-2,500"} {
		if got := Thousands(in); got != want {
			t.Errorf("Thousands(%v) = %s want %s", in, got, want)
		}
	}
	if got := isoWeekStart(time.Date(2026, 11, 5, 15, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("iso week start %v", got)
	}
}
