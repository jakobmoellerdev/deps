// Package schedule parses cron expressions used as time windows.
package schedule

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Set is a union of cron windows evaluated in one time zone. A nil *Set
// always contains every time.
type Set struct {
	exprs []expr
	src   []string
	loc   *time.Location
}

// expr holds one parsed expression as bitsets per field.
type expr struct {
	hour, dom, month, dow uint64
	domStar, dowStar      bool
}

type field struct {
	name     string
	min, max int
}

var fields = [...]field{
	{"minute", 0, 59},
	{"hour", 0, 23},
	{"day of month", 1, 31},
	{"month", 1, 12},
	{"day of week", 0, 7},
}

// Parse parses 5-field cron expressions. The minute field must be "*" because
// schedules are windows, not trigger times. Each other field accepts "*", n,
// a-b, comma lists and "/step" on "*" or ranges; day of week is 0-7 with 0 and 7
// both Sunday. tz "" means UTC, otherwise an IANA zone name. No expressions
// yield a nil Set (always).
func Parse(exprs []string, tz string) (*Set, error) {
	loc := time.UTC
	if tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil {
			return nil, fmt.Errorf("unknown time zone %q", tz)
		}
		loc = l
	}
	if len(exprs) == 0 {
		return nil, nil
	}
	s := &Set{exprs: make([]expr, 0, len(exprs)), src: slices.Clone(exprs), loc: loc}
	var errs []error
	for _, e := range exprs {
		x, err := parseExpr(e)
		if err != nil {
			errs = append(errs, fmt.Errorf("%q: %w", e, err))
			continue
		}
		s.exprs = append(s.exprs, x)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return s, nil
}

func parseExpr(s string) (expr, error) {
	f := strings.Fields(s)
	if len(f) != len(fields) {
		return expr{}, fmt.Errorf("want %d fields, got %d", len(fields), len(f))
	}
	if f[0] != "*" {
		return expr{}, errors.New("minute field must be * (schedules are windows)")
	}
	var bits [len(fields)]uint64
	for i := 1; i < len(fields); i++ {
		b, err := parseField(f[i], fields[i])
		if err != nil {
			return expr{}, fmt.Errorf("%s: %w", fields[i].name, err)
		}
		bits[i] = b
	}
	dow := bits[4]
	if dow&(1<<7) != 0 {
		dow |= 1 // 7 is Sunday
	}
	return expr{hour: bits[1], dom: bits[2], month: bits[3], dow: dow, domStar: isStar(f[2]), dowStar: isStar(f[4])}, nil
}

func isStar(s string) bool { return s == "*" || strings.HasPrefix(s, "*/") }

func parseField(s string, f field) (uint64, error) {
	var bits uint64
	for part := range strings.SplitSeq(s, ",") {
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("invalid step %q", stepStr)
			}
			step = n
		}
		lo, hi := f.min, f.max
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			var err error
			if lo, err = num(a, f); err != nil {
				return 0, err
			}
			if hi, err = num(b, f); err != nil {
				return 0, err
			}
			if lo > hi {
				return 0, fmt.Errorf("invalid range %q", rng)
			}
		default:
			if hasStep {
				return 0, fmt.Errorf("step needs * or a range, got %q", part)
			}
			n, err := num(rng, f)
			if err != nil {
				return 0, err
			}
			lo, hi = n, n
		}
		for v := lo; v <= hi; v += step {
			bits |= 1 << v
		}
	}
	return bits, nil
}

func num(s string, f field) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", s)
	}
	if n < f.min || n > f.max {
		return 0, fmt.Errorf("value %d out of range %d-%d", n, f.min, f.max)
	}
	return n, nil
}

// Contains reports whether t falls in any window, evaluated in the Set's zone.
// Like cron, when both day of month and day of week are restricted, either may match.
func (s *Set) Contains(t time.Time) bool {
	if s == nil {
		return true
	}
	t = t.In(s.loc)
	for _, e := range s.exprs {
		if e.match(t) {
			return true
		}
	}
	return false
}

func (e expr) match(t time.Time) bool {
	if e.hour&(1<<t.Hour()) == 0 || e.month&(1<<int(t.Month())) == 0 {
		return false
	}
	dom := e.dom&(1<<t.Day()) != 0
	dow := e.dow&(1<<int(t.Weekday())) != 0
	if !e.domStar && !e.dowStar {
		return dom || dow
	}
	return dom && dow
}

// String renders the expressions and zone, e.g. `* 0-5 * * 1-5 (UTC)`; nil is "always".
func (s *Set) String() string {
	if s == nil {
		return "always"
	}
	return strings.Join(s.src, "; ") + " (" + s.loc.String() + ")"
}
