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
