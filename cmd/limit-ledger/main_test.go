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

func TestComparePeriods(t *testing.T) {
	loc := time.FixedZone("IST", 3*3600)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, loc)
	s := func(v string) *string { return &v }
	i := func(v int) *int { return &v }
	f := 20.0
	args := func(from, to, vf, vt string, w int) compareArgs {
		return compareArgs{from: s(from), to: s(to), vsFrom: s(vf), vsTo: s(vt), vsWeeks: i(w), account: s(""), threshold: &f}
	}
	cur, base, err := comparePeriods(args("2026-11-02", "2026-11-05", "", "", 4), now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if !cur.From.Equal(time.Date(2026, 11, 2, 0, 0, 0, 0, loc)) || !cur.To.Equal(time.Date(2026, 11, 6, 0, 0, 0, 0, loc)) {
		t.Errorf("period %v → %v (the --to date must be included)", cur.From, cur.To)
	}
	if !base.From.Equal(time.Date(2026, 10, 5, 0, 0, 0, 0, loc)) || !base.To.Equal(cur.From) {
		t.Errorf("baseline %v → %v", base.From, base.To)
	}
	_, base, err = comparePeriods(args("2026-11-02", "", "2026-10-26", "2026-10-29", 0), now, loc)
	if err != nil || !base.To.Equal(time.Date(2026, 10, 30, 0, 0, 0, 0, loc)) {
		t.Errorf("explicit baseline %v %v", base, err)
	}
	cur, _, _ = comparePeriods(args("2026-11-02", "", "", "", 0), now, loc)
	if !cur.To.Equal(now) {
		t.Errorf("default end should be now, got %v", cur.To)
	}
	for _, bad := range []compareArgs{
		args("", "", "", "", 0),
		args("2026-11-05", "2026-11-01", "", "", 0),
		args("2026-11-02", "", "2026-10-26", "", 0),
		args("2026-11-02", "", "2026-10-26", "2026-10-29", 2),
		args("yesterday", "", "", "", 0),
	} {
		if _, _, err := comparePeriods(bad, now, loc); err == nil {
			t.Errorf("%+v should be rejected", bad)
		}
	}
}
