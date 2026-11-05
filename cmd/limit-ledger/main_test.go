package main

import (
	"testing"
	"time"
)

func TestManualSample(t *testing.T) {
	now := time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC)
	s, err := manualSample(86, "2026-11-08T22:00:00.216Z", -1, "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if *s.SevenPct != 86 || *s.SevenReset != time.Date(2026, 11, 8, 22, 0, 0, 0, time.UTC).UnixMilli() ||
		s.TS != now.UnixMilli() || s.FivePct != nil || s.SessionID != "manual" {
		t.Errorf("sample %+v", s)
	}
	for _, c := range []struct {
		w      float64
		resets string
		five   float64
		fr     string
	}{
		{-1, "2026-11-08T22:00:00Z", -1, ""},
		{101, "2026-11-08T22:00:00Z", -1, ""},
		{50, "", -1, ""},
		{50, "next sunday", -1, ""},
		{50, "2026-11-08T22:00:00Z", 30, ""},
	} {
		if _, err := manualSample(c.w, c.resets, c.five, c.fr, "", now); err == nil {
			t.Errorf("%+v should be rejected", c)
		}
	}
	if s, err := manualSample(50, "2026-11-08T22:00:00Z", 30, "2026-11-05T15:00:00Z", "2026-11-05T10:00:00Z", now); err != nil ||
		*s.FivePct != 30 || s.TS != time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC).UnixMilli() {
		t.Errorf("with five-hour and --at: %+v %v", s, err)
	}
}
