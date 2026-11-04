// Package schedule matches update schedules against the CI trigger of the run.
//
// A schedule lists cron expressions that equal cron triggers of the workflow running
// deps. A run triggered by one of them is in schedule; the expressions are never
// evaluated as time windows. Crons are only parsed to reject typos early.
package schedule

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

// EventSchedule is the event name of cron-triggered workflow runs.
const EventSchedule = "schedule"

// Set is a union of cron triggers. A nil *Set matches every trigger.
type Set struct {
	src  []string // as written
	norm []string // whitespace-normalized
}

// Trigger describes what started the run.
type Trigger struct {
	Event string // GITHUB_EVENT_NAME; "" outside CI
	Cron  string // the triggering cron of a schedule event; "" if unknown
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

// Parse validates 5-field cron expressions and returns them as a Set. Each field
// accepts "*", n, a-b, comma lists and "/step" on "*" or ranges; day of week is
// 0-7. No expressions yield a nil Set (always).
func Parse(exprs []string) (*Set, error) {
	if len(exprs) == 0 {
		return nil, nil
	}
	s := &Set{src: slices.Clone(exprs), norm: make([]string, len(exprs))}
	var errs []error
	for i, e := range exprs {
		if err := Validate(e); err != nil {
			errs = append(errs, fmt.Errorf("%q: %w", e, err))
		}
		s.norm[i] = Normalize(e)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return s, nil
}

// Validate reports whether s is a valid 5-field cron expression.
func Validate(s string) error {
	f := strings.Fields(s)
	if len(f) != len(fields) {
		return fmt.Errorf("want %d fields, got %d", len(fields), len(f))
	}
	for i, fl := range fields {
		if err := parseField(f[i], fl); err != nil {
			return fmt.Errorf("%s: %w", fl.name, err)
		}
	}
	return nil
}

// Normalize collapses whitespace so crons compare as written by humans.
func Normalize(s string) string { return strings.Join(strings.Fields(s), " ") }

func parseField(s string, f field) error {
	for part := range strings.SplitSeq(s, ",") {
		rng, stepStr, hasStep := strings.Cut(part, "/")
		if hasStep {
			if n, err := strconv.Atoi(stepStr); err != nil || n < 1 {
				return fmt.Errorf("invalid step %q", stepStr)
			}
		}
		lo, hi := f.min, f.max
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			var err error
			if lo, err = num(a, f); err != nil {
				return err
			}
			if hi, err = num(b, f); err != nil {
				return err
			}
		default:
			if hasStep {
				return fmt.Errorf("step needs * or a range, got %q", part)
			}
			n, err := num(rng, f)
			if err != nil {
				return err
			}
			lo, hi = n, n
		}
		if lo > hi {
			return fmt.Errorf("invalid range %q", rng)
		}
	}
	return nil
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

// EventPush is the event name of runs triggered by a push.
const EventPush = "push"

// Matches reports whether a run started by t is in schedule. A nil Set (no schedule)
// always matches. A push run (e.g. after merging a PR) is follow-up maintenance, so it
// is outside every schedule: only unscheduled groups get new PRs, open PRs are still
// refreshed (updateNotScheduled). Other runs not triggered by a cron (dispatch, local)
// act immediately; so does a schedule event whose cron is unknown. Otherwise the
// trigger cron must equal one of the Set's crons.
func (s *Set) Matches(t Trigger) bool {
	switch {
	case s == nil:
		return true
	case t.Event == EventPush:
		return false
	case t.Event != EventSchedule || t.Cron == "":
		return true
	}
	return slices.Contains(s.norm, Normalize(t.Cron))
}

// Crons returns the normalized crons; nil for a nil Set.
func (s *Set) Crons() []string {
	if s == nil {
		return nil
	}
	return s.norm
}

// String renders the crons, e.g. `0 0 * * *; 0 0 * * 0`; nil is "always".
func (s *Set) String() string {
	if s == nil {
		return "always"
	}
	return strings.Join(s.src, "; ")
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
