// Package schedule decides whether a run is in an update's schedule.
//
// A schedule lists entries of two kinds:
//
//   - Triggers: 5-field cron expressions with a fixed minute (`0 5 * * 1`). They equal cron
//     triggers of the workflow running deps, and a run is in schedule when it was started
//     by one of them. Triggers are matched against the CI trigger, never against the clock.
//   - Windows: Renovate's time windows, evaluated against the run time in the configured
//     time zone. Either a cron whose minute is `*` (`* 0-5 * * 1`, Renovate's cron
//     schedules: every listed hour, day and month) or text in a subset of Renovate's
//     later syntax (`before 6am on monday`).
//
// An entry starting with a letter is text, anything else a cron.
package schedule

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // time zones work without the system's zoneinfo (scratch images, Windows)
	"unicode"
	"unicode/utf8"
)

// EventSchedule is the event name of cron-triggered workflow runs.
const EventSchedule = "schedule"

// Set is a union of triggers and windows. A nil *Set matches every run.
type Set struct {
	src      []string // as written
	norm     []string // whitespace-normalized
	triggers []string // normalized trigger crons
	windows  []entry
	loc      *time.Location // of the windows
}

// Trigger describes what started the run and when.
type Trigger struct {
	Event string    // GITHUB_EVENT_NAME; "" outside CI
	Cron  string    // the triggering cron of a schedule event; "" if unknown
	Time  time.Time // run time windows are evaluated at; zero = time.Now()
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

// window is a time window, one of later's schedules. A run time is inside when its hour,
// day of month, month, day of week and ISO week are in the masks, its minute of the day
// is within after/before and, with times, one of them.
type window struct {
	hours  uint32 // bit h: hour h
	doms   uint32 // bit d: day of month d (1-31)
	last   bool   // also the last day of the month
	months uint16 // bit m: month m (1-12)
	dows   uint8  // bit d: weekday d (0 = Sunday)
	weeks  uint64 // bit w: ISO week of the year w (1-53)
	times  []int  // `at`: minutes of the day; nil = any
	// after and before bound the minute of the day: after <= m < before, wrapping past
	// midnight when after >= before (`after 10pm and before 5am`); -1 = unbounded.
	after, before int
}

const (
	allHours  = 1<<24 - 1
	allDoms   = (1<<31 - 1) << 1
	allMonths = (1<<12 - 1) << 1
	allDows   = 1<<7 - 1
	allWeeks  = (1<<53 - 1) << 1
)

func anyTime() window {
	return window{hours: allHours, doms: allDoms, months: allMonths, dows: allDows, weeks: allWeeks, after: -1, before: -1}
}

func (w *window) contains(t time.Time) bool {
	if w.hours&(1<<t.Hour()) == 0 || w.months&(1<<t.Month()) == 0 || w.dows&(1<<t.Weekday()) == 0 {
		return false
	}
	if w.doms&(1<<t.Day()) == 0 && (!w.last || t.AddDate(0, 0, 1).Day() != 1) {
		return false
	}
	if w.weeks != allWeeks {
		if _, wk := t.ISOWeek(); w.weeks&(1<<wk) == 0 {
			return false
		}
	}
	m := t.Hour()*60 + t.Minute()
	if w.times != nil && !slices.Contains(w.times, m) {
		return false
	}
	switch {
	case w.after >= 0 && w.before >= 0 && w.after >= w.before:
		return m >= w.after || m < w.before
	case w.after >= 0 && m < w.after:
		return false
	case w.before >= 0 && m >= w.before:
		return false
	}
	return true
}

// entry is a window entry: a run time is inside when one of its windows (later's
// schedules, joined by `also`) contains it and none of its exceptions (after `except`)
// does.
type entry struct {
	windows, except []window
}

func (e *entry) contains(t time.Time) bool {
	in := false
	for i := range e.windows {
		if e.windows[i].contains(t) {
			in = true
			break
		}
	}
	if !in {
		return false
	}
	for i := range e.except {
		if e.except[i].contains(t) {
			return false
		}
	}
	return true
}

// Parse validates schedule entries and returns them as a Set whose windows are evaluated
// in loc (nil = UTC). Cron fields accept "*", n, a-b, comma lists and "/step" on "*" or
// ranges; day of week is 0-7. No entries yield a nil Set (always).
func Parse(exprs []string, loc *time.Location) (*Set, error) {
	if len(exprs) == 0 {
		return nil, nil
	}
	if loc == nil {
		loc = time.UTC
	}
	s := &Set{src: slices.Clone(exprs), norm: make([]string, len(exprs)), loc: loc}
	var errs []error
	for i, e := range exprs {
		s.norm[i] = Normalize(e)
		w, trigger, err := parseEntry(e)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%q: %w", e, err))
		case trigger:
			s.triggers = append(s.triggers, s.norm[i])
		default:
			s.windows = append(s.windows, w)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return s, nil
}

// Validate reports whether s is a valid schedule entry: a cron or a text window.
func Validate(s string) error {
	_, _, err := parseEntry(s)
	return err
}

// ValidateCron reports whether s is a valid 5-field cron expression.
func ValidateCron(s string) error {
	_, err := parseCron(s)
	return err
}

// parseEntry classifies and parses one entry: text (first rune a letter) is a window,
// a cron is a window when its minute field is "*" and a trigger otherwise.
func parseEntry(s string) (e entry, trigger bool, err error) {
	s = strings.TrimSpace(s)
	if r, _ := utf8.DecodeRuneInString(s); unicode.IsLetter(r) {
		e, err = parseText(s)
		return e, false, err
	}
	masks, err := parseCron(s)
	if err != nil {
		return entry{}, false, err
	}
	if strings.Fields(s)[0] != "*" {
		return entry{}, true, nil
	}
	return entry{windows: []window{{
		hours: uint32(masks[1]), doms: uint32(masks[2]), months: uint16(masks[3]),
		dows: uint8(masks[4] | masks[4]>>7), weeks: allWeeks, after: -1, before: -1, // 7 = Sunday
	}}}, false, nil
}

func parseCron(s string) ([len(fields)]uint64, error) {
	var masks [len(fields)]uint64
	f := strings.Fields(s)
	if len(f) != len(fields) {
		return masks, fmt.Errorf("want %d fields, got %d", len(fields), len(f))
	}
	for i, fl := range fields {
		m, err := parseField(f[i], fl)
		if err != nil {
			return masks, fmt.Errorf("%s: %w", fl.name, err)
		}
		masks[i] = m
	}
	return masks, nil
}

// Normalize collapses whitespace so crons compare as written by humans.
func Normalize(s string) string { return strings.Join(strings.Fields(s), " ") }

// parseField returns the values of a cron field as a bit mask.
func parseField(s string, f field) (uint64, error) {
	var mask uint64
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
		if lo > hi {
			return 0, fmt.Errorf("invalid range %q", rng)
		}
		for v := lo; v <= hi; v += step {
			mask |= 1 << v
		}
	}
	return mask, nil
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

var weekdays = map[string]time.Weekday{
	"sunday": time.Sunday, "sun": time.Sunday,
	"monday": time.Monday, "mon": time.Monday,
	"tuesday": time.Tuesday, "tue": time.Tuesday, "tues": time.Tuesday,
	"wednesday": time.Wednesday, "wed": time.Wednesday,
	"thursday": time.Thursday, "thu": time.Thursday, "thurs": time.Thursday,
	"friday": time.Friday, "fri": time.Friday,
	"saturday": time.Saturday, "sat": time.Saturday,
}

const (
	weekdayMask = 1<<time.Monday | 1<<time.Tuesday | 1<<time.Wednesday | 1<<time.Thursday | 1<<time.Friday
	weekendMask = 1<<time.Saturday | 1<<time.Sunday
)

// parseText parses an entry in Renovate's later syntax subset: `at any time`, or windows
// joined by "also", optionally followed by "except" and exceptions joined by "also"
// (`before 6am also after 10pm except on sunday`). A window is clauses in any order,
// joined by spaces or "and":
//
//	before <time>, after <time>                  time of day; both: a range, wrapping past midnight
//	at <time>[ and <time>...]                    those minutes of the day
//	every weekday, every weekend, every <day>    days of the week
//	on <day>[ and|, <day>...], on <day> through <day>
//	on the <first|last|Nth> day of the month     day of the month
//	on the <first|last|Nth> day of the week      day of the week, first = Sunday
//	every <n> weeks [of the year]                ISO weeks 1, 1+n, ...
//	every <n> months, every month, every day     months 1, 1+n, ...; no-ops
//
// Times are 6am, 6:30pm, 6 am or 24-hour 18:00.
func parseText(s string) (entry, error) {
	toks := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return unicode.IsSpace(r) || r == ',' })
	if strings.Join(toks, " ") == "at any time" {
		return entry{windows: []window{anyTime()}}, nil
	}
	var e entry
	sep, start, except := "", 0, false
	for i := 0; i <= len(toks); i++ {
		if i < len(toks) && toks[i] != "also" && toks[i] != "except" {
			continue
		}
		if i == start {
			if sep != "" {
				return e, fmt.Errorf("%q needs a clause after it", sep)
			}
			return e, fmt.Errorf("%q needs a clause before it", toks[i])
		}
		w, err := parseClauses(toks[start:i])
		if err != nil {
			return e, err
		}
		if except {
			e.except = append(e.except, w)
		} else {
			e.windows = append(e.windows, w)
		}
		if i < len(toks) {
			if toks[i] == "except" {
				if except {
					return e, errors.New(`"except" given twice (join exceptions with "also")`)
				}
				except = true
			}
			sep, start = toks[i], i+1
		}
	}
	return e, nil
}

// parseClauses parses the clauses of one window (see parseText).
func parseClauses(toks []string) (window, error) {
	w := anyTime()
	seen := map[string]bool{}
	once := func(k string) error {
		if seen[k] {
			return fmt.Errorf("%s given twice", k)
		}
		seen[k] = true
		return nil
	}
	for i := 0; i < len(toks); i++ {
		tok := toks[i]
		next := func() string {
			if i+1 < len(toks) {
				i++
				return toks[i]
			}
			return ""
		}
		switch tok {
		case "and":
		case "before", "after":
			if err := once(tok); err != nil {
				return w, err
			}
			m, n, err := parseClock(toks[i+1:])
			if err != nil {
				return w, fmt.Errorf("%s: %w", tok, err)
			}
			i += n
			if tok == "before" {
				w.before = m
			} else {
				w.after = m
			}
		case "at":
			if err := once(tok); err != nil {
				return w, err
			}
			for {
				m, n, err := parseClock(toks[i+1:])
				if err != nil {
					return w, fmt.Errorf("at: %w", err)
				}
				w.times = append(w.times, m)
				i += n
				// "and" continues the list only when a time follows (`at 5am and 6pm`).
				if i+2 >= len(toks) || toks[i+1] != "and" {
					break
				}
				if _, _, err := parseClock(toks[i+2:]); err != nil {
					break
				}
				i++
			}
		case "every":
			switch arg := next(); arg {
			case "weekday", "weekdays":
				if err := once("day of week"); err != nil {
					return w, err
				}
				w.dows = weekdayMask
			case "weekend", "weekends":
				if err := once("day of week"); err != nil {
					return w, err
				}
				w.dows = weekendMask
			case "day", "month":
			default:
				if d, ok := weekday(arg); ok {
					if err := once("day of week"); err != nil {
						return w, err
					}
					w.dows = 1 << d
					continue
				}
				if err := parseEvery(toks[i:], &w, once); err != nil {
					return w, err
				}
				i++
				if (toks[i] == "week" || toks[i] == "weeks") && i+3 < len(toks) && toks[i+1] == "of" && toks[i+2] == "the" && toks[i+3] == "year" {
					i += 3
				}
			}
		case "on":
			if i+1 < len(toks) && toks[i+1] == "the" {
				i++
				d, last, err := ordinal(next())
				if err != nil {
					return w, err
				}
				unit := ""
				if i+4 < len(toks) && toks[i+1] == "day" && toks[i+2] == "of" && toks[i+3] == "the" {
					unit, i = toks[i+4], i+4
				}
				if unit != "month" && unit != "week" {
					return w, fmt.Errorf(`want "on the <n> day of the month" or "of the week", got %q`, strings.Join(toks, " "))
				}
				if err := onThe(unit == "week", d, last, &w, once); err != nil {
					return w, err
				}
				continue
			}
			if err := once("day of week"); err != nil {
				return w, err
			}
			mask, n, err := parseDays(toks[i+1:])
			if err != nil {
				return w, err
			}
			w.dows, i = mask, i+n
		default:
			return w, fmt.Errorf("unknown word %q (want before, after, at, every, on, also, except or a cron)", tok)
		}
	}
	return w, nil
}

// parseEvery reads `<n> weeks` or `<n> months` from toks into w: the ISO weeks of the
// year or months 1, 1+n, 1+2n, ..., as later counts them (`every 2 weeks` = weeks 1, 3,
// 5, ...).
func parseEvery(toks []string, w *window, once func(string) error) error {
	n, err := strconv.Atoi(toks[0])
	unit, maxN := "", 0
	if len(toks) > 1 {
		switch toks[1] {
		case "week", "weeks":
			unit, maxN = "weeks", 53
		case "month", "months":
			unit, maxN = "months", 12
		}
	}
	if err != nil || unit == "" || n < 1 || n > maxN {
		return fmt.Errorf(`"every" needs weekday, weekend, a day name, <n> weeks or <n> months, got %q`, strings.Join(toks, " "))
	}
	if err := once(unit); err != nil {
		return err
	}
	if unit == "weeks" {
		w.weeks = 0
		for wk := 1; wk <= 53; wk += n {
			w.weeks |= 1 << wk
		}
		return nil
	}
	w.months = 0
	for m := 1; m <= 12; m += n {
		w.months |= 1 << m
	}
	return nil
}

// onThe sets the day of `on the <ordinal> day of the month|week` in w: a day of the month,
// or with week a day of the week counted from Sunday, as in later (first = Sunday, last =
// Saturday).
func onThe(week bool, d int, last bool, w *window, once func(string) error) error {
	if !week {
		if err := once("day of month"); err != nil {
			return err
		}
		w.doms, w.last = 0, last
		if !last {
			w.doms = 1 << d
		}
		return nil
	}
	if err := once("day of week"); err != nil {
		return err
	}
	switch {
	case last:
		d = 7
	case d > 7:
		return fmt.Errorf("want a day of the week from first to 7th, got day %d", d)
	}
	w.dows = 1 << (d - 1)
	return nil
}

func weekday(s string) (time.Weekday, bool) {
	d, ok := weekdays[s]
	if !ok && strings.HasSuffix(s, "s") {
		d, ok = weekdays[strings.TrimSuffix(s, "s")]
	}
	return d, ok
}

// parseDays reads `monday`, `monday and friday`, `mon, wed`, `monday through friday`
// (wrapping: `friday through monday`) and weekday/weekend from toks, returning the
// mask and the number of tokens read.
func parseDays(toks []string) (uint8, int, error) {
	var mask uint8
	n := 0
	for n < len(toks) {
		tok := toks[n]
		switch tok {
		case "weekday", "weekdays":
			mask |= weekdayMask
		case "weekend", "weekends":
			mask |= weekendMask
		default:
			d, ok := weekday(tok)
			if !ok {
				if mask == 0 {
					return 0, 0, fmt.Errorf(`"on" needs a day name or "the <n> day of the month", got %q`, tok)
				}
				return mask, n, nil
			}
			if n+2 < len(toks) && (toks[n+1] == "through" || toks[n+1] == "to") {
				e, ok := weekday(toks[n+2])
				if !ok {
					return 0, 0, fmt.Errorf("%s: unknown day %q", toks[n+1], toks[n+2])
				}
				for ; d != e; d = (d + 1) % 7 {
					mask |= 1 << d
				}
				n += 2
			}
			mask |= 1 << d
		}
		n++
		// Another day follows only after "and" or a comma (dropped by the tokenizer).
		if n+1 < len(toks) && toks[n] == "and" {
			if _, ok := weekday(toks[n+1]); ok || strings.HasPrefix(toks[n+1], "week") {
				n++
			}
		}
	}
	if mask == 0 {
		return 0, 0, errors.New(`"on" needs a day name or "the <n> day of the month", got ""`)
	}
	return mask, n, nil
}

// ordinal parses first, last, 1st, 2nd, 3rd, 4th, ..., 31st.
func ordinal(s string) (day int, last bool, err error) {
	switch s {
	case "first":
		return 1, false, nil
	case "last":
		return 0, true, nil
	}
	digits := strings.TrimRight(s, "stndrh")
	if n, e := strconv.Atoi(digits); e == nil && n >= 1 && n <= 31 && len(s)-len(digits) == 2 {
		return n, false, nil
	}
	return 0, false, fmt.Errorf(`want a day like first, last or 15th, got %q`, s)
}

// parseClock reads a time of day from toks (6am, 6:30pm, 6 am, 18:00), returning the
// minute of the day and the number of tokens read.
func parseClock(toks []string) (int, int, error) {
	if len(toks) == 0 {
		return 0, 0, errors.New("want a time like 6am or 18:30")
	}
	s, n := toks[0], 1
	if len(toks) > 1 && (toks[1] == "am" || toks[1] == "pm") {
		s, n = s+toks[1], 2
	}
	bad := fmt.Errorf("want a time like 6am or 18:30, got %q", s)
	suffix := ""
	if strings.HasSuffix(s, "am") || strings.HasSuffix(s, "pm") {
		s, suffix = s[:len(s)-2], s[len(s)-2:]
	}
	hs, ms, hasMin := strings.Cut(s, ":")
	h, err := strconv.Atoi(hs)
	if err != nil || len(hs) > 2 {
		return 0, 0, bad
	}
	m := 0
	if hasMin {
		if m, err = strconv.Atoi(ms); err != nil || len(ms) != 2 || m > 59 {
			return 0, 0, bad
		}
	}
	switch {
	case suffix == "" && !hasMin:
		return 0, 0, bad
	case suffix == "" && h > 23, suffix != "" && (h < 1 || h > 12):
		return 0, 0, bad
	case suffix == "am" && h == 12:
		h = 0
	case suffix == "pm" && h != 12:
		h += 12
	}
	return h*60 + m, n, nil
}

// Event names that make a run act immediately: a manual dispatch from the UI or API, and an
// API dispatch. A local run without CI has no event and acts immediately too.
const (
	EventWorkflowDispatch   = "workflow_dispatch"
	EventRepositoryDispatch = "repository_dispatch"
)

// Matches reports whether a run started by t is in schedule: a window contains the run
// time, or a trigger matches. A nil Set (no schedule) always matches.
//
// Windows only look at t.Time, in the Set's time zone, whatever the event.
//
// Triggers: runs someone asked for (workflow_dispatch, repository_dispatch, a local run
// without an event) act immediately; so does a schedule event whose cron is unknown. A
// schedule event's cron must equal one of the triggers. Every other event (push after a
// merge, issues when the dashboard is ticked, workflow_run after CI, …) is follow-up
// maintenance, so it is outside every trigger: only unscheduled groups get new PRs, open
// PRs are still refreshed (updateNotScheduled).
func (s *Set) Matches(t Trigger) bool {
	if s == nil {
		return true
	}
	if len(s.windows) > 0 {
		now := t.Time
		if now.IsZero() {
			now = time.Now()
		}
		now = now.In(s.loc)
		for i := range s.windows {
			if s.windows[i].contains(now) {
				return true
			}
		}
	}
	switch {
	case len(s.triggers) == 0:
		return false
	case t.Event == "" || t.Event == EventWorkflowDispatch || t.Event == EventRepositoryDispatch:
		return true
	case t.Event != EventSchedule:
		return false
	case t.Cron == "":
		return true
	}
	return slices.Contains(s.triggers, Normalize(t.Cron))
}

// Entries returns the whitespace-normalized entries, triggers and windows; nil for a
// nil Set.
func (s *Set) Entries() []string {
	if s == nil {
		return nil
	}
	return s.norm
}

// String renders the entries, e.g. `0 0 * * *; before 6am on monday`, followed by the
// time zone when the Set has windows outside UTC; nil is "always".
func (s *Set) String() string {
	if s == nil {
		return "always"
	}
	str := strings.Join(s.src, "; ")
	if len(s.windows) > 0 && s.loc != time.UTC {
		str += " (" + s.loc.String() + ")"
	}
	return str
}

// Detect determines the run trigger. A non-empty override (--trigger-schedule) is a
// schedule event with that cron. Otherwise the event comes from GITHUB_EVENT_NAME
// (GitHub, Gitea and Forgejo set it); for schedule events the cron is the `schedule`
// field of the payload at GITHUB_EVENT_PATH, which GitHub and Gitea (verified
// in 28.3: services/actions/schedule_tasks.go) both fill. The error reports an unreadable payload; the trigger is still
// usable and then matches every schedule.
func Detect(getenv func(string) string, override string) (Trigger, error) {
	if override != "" {
		return Trigger{Event: EventSchedule, Cron: Normalize(override)}, nil
	}
	t := Trigger{Event: getenv("GITHUB_EVENT_NAME")}
	if t.Event != EventSchedule {
		return t, nil
	}
	path := getenv("GITHUB_EVENT_PATH")
	if path == "" {
		return t, errors.New("GITHUB_EVENT_PATH is not set")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return t, err
	}
	var payload struct {
		Schedule string `json:"schedule"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return t, fmt.Errorf("%s: %w", path, err)
	}
	t.Cron = Normalize(payload.Schedule)
	return t, nil
}
