package transcript

import (
	"testing"
	"time"
)

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// The message formats below are the ones seen in real transcripts.
func TestParseLimitMessage(t *testing.T) {
	cases := []struct {
		hit, text, kind, resets string
	}{
		{"2026-08-05T17:58:54Z", "You've hit your weekly limit · resets Aug 10, 1am (Europe/Istanbul)", "weekly", "2026-08-09T22:00:00Z"},
		{"2026-08-25T06:59:34Z", "You've hit your weekly limit · resets 10am (Europe/Istanbul)", "weekly", "2026-08-25T07:00:00Z"},
		{"2026-10-12T16:23:09Z", "You've hit your weekly limit · resets 7am (UTC)", "weekly", "2026-10-13T07:00:00Z"},
		{"2026-10-18T10:06:18Z", "You've hit your weekly limit · resets 1am (Europe/Istanbul)", "weekly", "2026-10-18T22:00:00Z"},
		{"2026-08-25T21:09:01Z", "You've hit your session limit · resets 12:10am (Europe/Istanbul)", "session", "2026-08-25T21:10:00Z"},
		{"2026-07-11T10:17:28Z", "You’ve hit your session limit · resets 3pm (Europe/Istanbul)", "session", "2026-07-11T12:00:00Z"},
		{"2026-12-30T12:00:00Z", "You've hit your weekly limit · resets Jan 2, 9am (UTC)", "weekly", "2027-01-02T09:00:00Z"},
		{"2026-03-01T12:00:00Z", "You've hit your Opus limit · resets Mar 3, 12pm (America/New_York)", "opus", "2026-03-03T17:00:00Z"},
		{"2026-07-01T00:00:00Z", "Claude AI usage limit reached|1751346000", "usage", "2025-07-01T05:00:00Z"},
	}
	for _, c := range cases {
		a, ok := ParseLimitMessage(utc(c.hit), c.text)
		if !ok {
			t.Errorf("%q: not recognised", c.text)
			continue
		}
		if a.Kind != c.kind {
			t.Errorf("%q: kind %q want %q", c.text, a.Kind, c.kind)
		}
		if got := time.UnixMilli(a.Resets).UTC(); !got.Equal(utc(c.resets)) {
			t.Errorf("%q: resets %v want %s", c.text, got, c.resets)
		}
		if a.Hit != utc(c.hit).UnixMilli() {
			t.Errorf("%q: hit time not kept", c.text)
		}
	}
}

func TestParseLimitMessageRejects(t *testing.T) {
	hit := utc("2026-10-01T00:00:00Z")
	for _, text := range []string{
		"API Error: Server is temporarily limiting requests (not your usage limit) · Rate limited",
		"You've hit your weekly limit · resets soon (UTC)",
		"You've hit your weekly limit · resets 13pm (UTC)",
		"You've hit your weekly limit · resets 7am (Mars/Olympus)",
	} {
		if a, ok := ParseLimitMessage(hit, text); ok {
			t.Errorf("%q: unexpectedly parsed as %+v", text, a)
		}
	}
}
