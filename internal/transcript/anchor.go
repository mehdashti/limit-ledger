package transcript

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// You've hit your weekly limit · resets Aug 10, 1am (Europe/Istanbul)
	reLimit = regexp.MustCompile(`You(?:'|’)ve hit your (.+?) limit\s*·\s*resets\s+(.+?)\s*\(([^()]+)\)`)
	// Older format: Claude AI usage limit reached|1752345600
	reLegacy = regexp.MustCompile(`usage limit reached\|(\d{9,11})`)
	// Aug 10, 1am | 10am | 12:10am
	reSpec = regexp.MustCompile(`(?i)^(?:([a-z]{3,9})\.?\s+(\d{1,2}),?\s+(?:at\s+)?)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)$`)
)

var months = map[string]time.Month{
	"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April,
	"may": time.May, "jun": time.June, "jul": time.July, "aug": time.August,
	"sep": time.September, "oct": time.October, "nov": time.November, "dec": time.December,
}

// ParseLimitMessage recognises a limit-hit message and resolves its reset time.
func ParseLimitMessage(hit time.Time, text string) (*Anchor, bool) {
	if m := reLimit.FindStringSubmatch(text); m != nil {
		resets, err := ParseResets(hit, m[2], m[3])
		if err != nil {
			return nil, false
		}
		return &Anchor{
			Kind:   strings.ToLower(strings.TrimSpace(m[1])),
			Hit:    hit.UnixMilli(),
			Resets: resets.UnixMilli(),
		}, true
	}
	if m := reLegacy.FindStringSubmatch(text); m != nil {
		sec, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return nil, false
		}
		return &Anchor{Kind: "usage", Hit: hit.UnixMilli(), Resets: sec * 1000}, true
	}
	return nil, false
}

// ParseResets turns a reset time as written in a limit message ("Aug 10, 1am",
// "10am", "12:10am") in the named time zone into an absolute time. A time without
// a date is the first such local time after hit. A date without a year is in the
// year of hit, or the next year if that would put it more than a day before hit.
func ParseResets(hit time.Time, spec, tz string) (time.Time, error) {
	loc, err := time.LoadLocation(strings.TrimSpace(tz))
	if err != nil {
		return time.Time{}, fmt.Errorf("time zone %q: %w", tz, err)
	}
	m := reSpec.FindStringSubmatch(strings.TrimSpace(spec))
	if m == nil {
		return time.Time{}, fmt.Errorf("unrecognised reset time %q", spec)
	}
	hour, _ := strconv.Atoi(m[3])
	if hour < 1 || hour > 12 {
		return time.Time{}, fmt.Errorf("hour out of range in %q", spec)
	}
	minute := 0
	if m[4] != "" {
		minute, _ = strconv.Atoi(m[4])
		if minute > 59 {
			return time.Time{}, fmt.Errorf("minute out of range in %q", spec)
		}
	}
	hour %= 12
	if strings.EqualFold(m[5], "pm") {
		hour += 12
	}
	local := hit.In(loc)
	if m[1] != "" {
		mon, ok := months[strings.ToLower(m[1])[:3]]
		if !ok {
			return time.Time{}, fmt.Errorf("unrecognised month in %q", spec)
		}
		day, _ := strconv.Atoi(m[2])
		t := time.Date(local.Year(), mon, day, hour, minute, 0, 0, loc)
		if t.Before(local.Add(-24 * time.Hour)) {
			t = time.Date(local.Year()+1, mon, day, hour, minute, 0, 0, loc)
		}
		return t, nil
	}
	t := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if !t.After(local) {
		t = time.Date(local.Year(), local.Month(), local.Day()+1, hour, minute, 0, 0, loc)
	}
	return t, nil
}
