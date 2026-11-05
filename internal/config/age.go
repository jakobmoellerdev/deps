package config

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// ageRe matches Renovate-style ages: an integer and a unit ("28 days", "1 day", "3d").
var ageRe = regexp.MustCompile(`^(\d+)\s*(m|mins?|minutes?|h|hours?|d|days?|w|weeks?)$`)

var ageUnits = map[byte]time.Duration{'m': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}

// UnknownAge is the age of a current version whose release time is unknown: no currentAge
// rule matches it.
const UnknownAge time.Duration = -1

// ageRangeRe splits a currentAge into its operator and age ("> 1 year", "<=30 days").
var ageRangeRe = regexp.MustCompile(`^\s*(<=|>=|<|>)\s*(.+?)\s*$`)

// longAgeRe matches the units only currentAge accepts: months (30 days) and years (365 days).
var longAgeRe = regexp.MustCompile(`^(\d+)\s*(months?|years?|y)$`)

// ageRange is a parsed currentAge (Renovate's matchCurrentAge): the age of the current
// version compared with an operator, so "> 1 year" matches versions released over a year ago.
type ageRange struct {
	op  string
	age time.Duration
}

// parseAgeRange parses a currentAge: an operator <, <=, > or >= and an age as for
// minimumReleaseAge, or in months (30 days) or years (365 days).
func parseAgeRange(s string) (*ageRange, error) {
	m := ageRangeRe.FindStringSubmatch(s)
	if m == nil {
		return nil, fmt.Errorf(`invalid age range %q: want an operator <, <=, > or >= and an age, e.g. "> 1 year" or "< 30 days"`, s)
	}
	d, err := parseLongAge(m[2])
	if err != nil {
		return nil, fmt.Errorf("invalid age range %q: %w", s, err)
	}
	return &ageRange{op: m[1], age: d}, nil
}

// parseLongAge parses an age as ParseAge does, or in months (30 days) or years (365 days):
// currentAge and abandonmentThreshold.
func parseLongAge(s string) (time.Duration, error) {
	if l := longAgeRe.FindStringSubmatch(s); l != nil {
		unit := 30 * 24 * time.Hour
		if l[2][0] == 'y' {
			unit = 365 * 24 * time.Hour
		}
		n, err := strconv.ParseInt(l[1], 10, 64)
		if err != nil || n > int64(1<<63-1)/int64(unit) {
			return 0, fmt.Errorf("age %q out of range", s)
		}
		return time.Duration(n) * unit, nil
	}
	return ParseAge(s)
}

// matches reports whether age (>= 0) satisfies the range.
func (a *ageRange) matches(age time.Duration) bool {
	switch a.op {
	case ">":
		return age > a.age
	case ">=":
		return age >= a.age
	case "<":
		return age < a.age
	}
	return age <= a.age
}

// ParseAge parses a minimumReleaseAge: "<n> <unit>" with unit minutes, hours, days or weeks
// (singular, plural or the first letter, optionally without the space), "0", or a Go
// duration ("1h30m"). Negative ages are invalid.
func ParseAge(s string) (time.Duration, error) {
	if s == "0" {
		return 0, nil
	}
	if m := ageRe.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseInt(m[1], 10, 64)
		unit := ageUnits[m[2][0]]
		if err != nil || n > int64(1<<63-1)/int64(unit) {
			return 0, fmt.Errorf("age %q out of range", s)
		}
		return time.Duration(n) * unit, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf(`invalid age %q: want e.g. "3 days", "12h", "90m" or "0"`, s)
	}
	return d, nil
}

// FormatAge renders an age in the largest unit that divides it ("28 days", "12h", "90m"), or
// as a Go duration.
func FormatAge(d time.Duration) string {
	switch day := 24 * time.Hour; {
	case d == 0:
		return "0"
	case d == day:
		return "1 day"
	case d%day == 0:
		return strconv.FormatInt(int64(d/day), 10) + " days"
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	default:
		return d.String()
	}
}
