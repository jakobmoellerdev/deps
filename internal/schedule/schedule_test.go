package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseErrors(t *testing.T) {
	tests := []struct{ expr, want string }{
		{"* * * *", "want 5 fields, got 4"},
		{"60 * * * *", "minute: value 60 out of range 0-59"},
		{"* 24 * * *", "hour: value 24 out of range 0-23"},
		{"* x * * *", `hour: invalid value "x"`},
		{"* 5-3 * * *", `hour: invalid range "5-3"`},
		{"* 1-x * * *", `hour: invalid value "x"`},
		{"* x-3 * * *", `hour: invalid value "x"`},
		{"* */0 * * *", `hour: invalid step "0"`},
		{"* 5/2 * * *", `hour: step needs * or a range, got "5/2"`},
		{"* * 0 * *", "day of month: value 0 out of range 1-31"},
		{"* * * * 8", "day of week: value 8 out of range 0-7"},
		{"befor 6am", `unknown word "befor" (want before, after, every, on or a cron)`},
		{"before", "before: want a time like 6am or 18:30"},
		{"before 6", `before: want a time like 6am or 18:30, got "6"`},
		{"before 13am", `got "13am"`},
		{"before 0pm", `got "0pm"`},
		{"before 24:00", `got "24:00"`},
		{"before 6:5am", `got "6:5am"`},
		{"before 6:60", `got "6:60"`},
		{"before x:00", `got "x:00"`},
		{"before 123:00", `got "123:00"`},
		{"before 6am and before 7am", "before given twice"},
		{"every weekday on monday", "day of week given twice"},
		{"every weekend every friday", "day of week given twice"},
		{"every monday every weekend", "day of week given twice"},
		{"every hour", `"every" needs weekday, weekend, a day name or <n> months, got "hour"`},
		{"every 13 months", `"every" needs`},
		{"every 3 weeks", `"every" needs`},
		{"every 3 months every 2 months", "months given twice"},
		{"on", `"on" needs a day name or "the <n> day of the month", got ""`},
		{"on someday", `"on" needs a day name`},
		{"on monday through someday", `through: unknown day "someday"`},
		{"on the first day of the week", `want "on the <n> day of the month"`},
		{"on the 32nd day of the month", `want a day like first, last or 15th, got "32nd"`},
		{"on the first day of the month on the last day of the month", "day of month given twice"},
	}
	for _, tt := range tests {
		_, err := Parse([]string{tt.expr}, nil)
		if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), `"`+tt.expr+`"`) {
			t.Errorf("Parse(%q) err = %v, want %q", tt.expr, err, tt.want)
		}
	}
}

func TestNilSet(t *testing.T) {
	s, err := Parse(nil, nil)
	if err != nil || s != nil {
		t.Fatalf("Parse(nil) = %v, %v", s, err)
	}
	if !s.Matches(Trigger{Event: EventSchedule, Cron: "0 5 * * *"}) || !s.Matches(Trigger{Event: "push"}) || s.String() != "always" || s.Entries() != nil {
		t.Errorf("nil set: string=%q", s.String())
	}
}

func TestMatches(t *testing.T) {
	s, err := Parse([]string{"0 0 * * *", "  0\t0 *  * 0 "}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		t    Trigger
		want bool
	}{
		{"exact cron", Trigger{Event: EventSchedule, Cron: "0 0 * * *"}, true},
		{"normalized whitespace", Trigger{Event: EventSchedule, Cron: "0 0 * *   0"}, true},
		{"other cron", Trigger{Event: EventSchedule, Cron: "0 5 * * *"}, false},
		{"no time evaluation", Trigger{Event: EventSchedule, Cron: "0 0 * * 7", Time: time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)}, false},
		{"schedule without cron", Trigger{Event: EventSchedule}, true},
		{"workflow_dispatch", Trigger{Event: EventWorkflowDispatch}, true},
		{"push is outside every schedule", Trigger{Event: "push"}, false},
		{"issues (dashboard ticked) is maintenance", Trigger{Event: "issues"}, false},
		{"workflow_run is maintenance", Trigger{Event: "workflow_run"}, false},
		{"repository_dispatch", Trigger{Event: EventRepositoryDispatch}, true},
		{"local", Trigger{}, true},
	}
	for _, tt := range tests {
		if got := s.Matches(tt.t); got != tt.want {
			t.Errorf("%s: Matches(%+v) = %v, want %v", tt.name, tt.t, got, tt.want)
		}
	}
	if got := s.String(); got != "0 0 * * *;   0\t0 *  * 0 " {
		t.Errorf("String() = %q", got)
	}
	if got := s.Entries(); len(got) != 2 || got[1] != "0 0 * * 0" {
		t.Errorf("Entries() = %q", got)
	}
}

// Windows are evaluated against the run time in the Set's time zone, whatever the event.
func TestWindows(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-11-09 is a Monday; Berlin is UTC+1 in November.
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 11, day, hour, minute, 0, 0, berlin) }
	tests := []struct {
		entry string
		in    []time.Time
		out   []time.Time
	}{
		{"before 6am", []time.Time{at(9, 0, 0), at(9, 5, 59)}, []time.Time{at(9, 6, 0), at(9, 23, 0)}},
		{"before 6:30am", []time.Time{at(9, 6, 29)}, []time.Time{at(9, 6, 30)}},
		{"before 12am", nil, []time.Time{at(9, 0, 0)}},
		{"after 12pm", []time.Time{at(9, 12, 0)}, []time.Time{at(9, 11, 59)}},
		{"after 10pm", []time.Time{at(9, 22, 0), at(9, 23, 59)}, []time.Time{at(9, 21, 59), at(10, 0, 0)}},
		{"after 10 pm", []time.Time{at(9, 22, 0)}, []time.Time{at(9, 21, 0)}},
		{"after 9:00 and before 17:00", []time.Time{at(9, 9, 0), at(9, 16, 59)}, []time.Time{at(9, 8, 59), at(9, 17, 0)}},
		{"after 10pm and before 5am every weekday", []time.Time{at(9, 23, 0), at(10, 4, 0), at(13, 23, 0)}, []time.Time{at(9, 12, 0), at(14, 3, 0), at(15, 23, 0)}},
		{"every weekend", []time.Time{at(7, 0, 0), at(8, 23, 59)}, []time.Time{at(9, 0, 0), at(6, 23, 59)}},
		{"every weekday", []time.Time{at(9, 0, 0), at(13, 23, 59)}, []time.Time{at(14, 0, 0), at(15, 12, 0)}},
		{"on monday", []time.Time{at(9, 0, 0), at(16, 23, 59)}, []time.Time{at(10, 0, 0), at(8, 23, 59)}},
		{"on Mondays", []time.Time{at(9, 12, 0)}, []time.Time{at(10, 12, 0)}},
		{"every friday", []time.Time{at(13, 12, 0)}, []time.Time{at(12, 12, 0)}},
		{"on friday and saturday", []time.Time{at(13, 1, 0), at(14, 1, 0)}, []time.Time{at(15, 1, 0)}},
		{"on tue, thu", []time.Time{at(10, 1, 0), at(12, 1, 0)}, []time.Time{at(11, 1, 0)}},
		{"on monday through wednesday", []time.Time{at(9, 1, 0), at(11, 1, 0)}, []time.Time{at(12, 1, 0)}},
		{"on friday to monday", []time.Time{at(13, 1, 0), at(8, 1, 0), at(9, 1, 0)}, []time.Time{at(10, 1, 0)}},
		{"on weekends", []time.Time{at(8, 1, 0)}, []time.Time{at(9, 1, 0)}},
		{"before 6am on monday", []time.Time{at(9, 5, 0)}, []time.Time{at(9, 6, 0), at(10, 5, 0)}},
		{"on monday before 6am", []time.Time{at(9, 5, 0)}, []time.Time{at(9, 7, 0)}},
		{"before 6am on monday and wednesday", []time.Time{at(9, 5, 0), at(11, 5, 0)}, []time.Time{at(10, 5, 0)}},
		{"on monday and after 8pm", []time.Time{at(9, 21, 0)}, []time.Time{at(9, 19, 0)}},
		{"on the first day of the month", []time.Time{at(1, 0, 0), at(1, 23, 59)}, []time.Time{at(2, 0, 0), at(30, 12, 0)}},
		{"before 4am on the first day of the month", []time.Time{at(1, 3, 0)}, []time.Time{at(1, 4, 0)}},
		{"on the 15th day of the month", []time.Time{at(15, 3, 0)}, []time.Time{at(16, 3, 0)}},
		{"on the last day of the month", []time.Time{at(30, 3, 0)}, []time.Time{at(29, 3, 0)}},
		{"every 3 months on the first day of the month", []time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, berlin)}, []time.Time{at(1, 0, 0)}},
		{"every day every month", []time.Time{at(9, 12, 0)}, nil},
		{"at any time", []time.Time{at(9, 12, 0)}, nil},
		{"* 0-5 * * 1", []time.Time{at(9, 0, 0), at(9, 5, 59)}, []time.Time{at(9, 6, 0), at(10, 3, 0)}},
		{"* * * * 0,7", []time.Time{at(8, 12, 0)}, []time.Time{at(9, 12, 0)}},
		{"* */6 1 * *", []time.Time{at(1, 6, 30)}, []time.Time{at(1, 7, 0), at(2, 6, 0)}},
		{"* * * 1-10 *", []time.Time{time.Date(2026, 10, 31, 0, 0, 0, 0, berlin)}, []time.Time{at(1, 0, 0)}},
	}
	for _, tt := range tests {
		s, err := Parse([]string{tt.entry}, berlin)
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.entry, err)
			continue
		}
		for _, now := range tt.in {
			// The event does not matter for windows; the time is converted to the Set's zone.
			if !s.Matches(Trigger{Event: "push", Time: now.UTC()}) {
				t.Errorf("%q: %v should be inside", tt.entry, now)
			}
		}
		for _, now := range tt.out {
			if s.Matches(Trigger{Event: EventWorkflowDispatch, Time: now}) {
				t.Errorf("%q: %v should be outside", tt.entry, now)
			}
		}
	}
}

// A Set mixing triggers and windows matches when either does.
func TestMixedSet(t *testing.T) {
	s, err := Parse([]string{"0 5 * * 1", "every weekend"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sat := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	mon := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		t    Trigger
		want bool
	}{
		{Trigger{Event: "push", Time: sat}, true},
		{Trigger{Event: "push", Time: mon}, false},
		{Trigger{Event: EventSchedule, Cron: "0 5 * * 1", Time: mon}, true},
		{Trigger{Event: EventSchedule, Cron: "0 5 * * *", Time: mon}, false},
		{Trigger{Event: EventWorkflowDispatch, Time: mon}, true},
	}
	for _, tt := range tests {
		if got := s.Matches(tt.t); got != tt.want {
			t.Errorf("Matches(%+v) = %v, want %v", tt.t, got, tt.want)
		}
	}
	if got := s.String(); got != "0 5 * * 1; every weekend" {
		t.Errorf("String() = %q", got)
	}
	// Without a time the clock decides; only the window entry is consulted.
	w, _ := Parse([]string{"at any time"}, nil)
	if !w.Matches(Trigger{Event: "push"}) {
		t.Error("at any time does not match now")
	}
}

func TestStringTimezone(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ entries []string }{{[]string{"before 6am"}}, {[]string{"0 5 * * *"}}} {
		s, _ := Parse(tt.entries, berlin)
		want := tt.entries[0]
		if tt.entries[0] == "before 6am" {
			want += " (Europe/Berlin)" // triggers are matched as text, so only windows name the zone
		}
		if got := s.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	if err := Validate("before 6am on monday"); err != nil {
		t.Error(err)
	}
	if err := ValidateCron("before 6am on monday"); err == nil {
		t.Error("ValidateCron accepts a window")
	}
	if err := ValidateCron("0 5 * * 1"); err != nil {
		t.Error(err)
	}
}

func TestDetect(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	github := write("github.json", `{"schedule":"0 5 * * *","repository":{"full_name":"o/r"},"workflow":".github/workflows/deps.yml"}`)
	// Gitea 28.3 (services/actions/schedule_tasks.go withScheduleInEventPayload) adds
	// "schedule" next to repository and sender in the synthesized payload.
	gitea := write("gitea.json", `{"repository":{"full_name":"o/r"},"sender":{"login":"gitea-actions"},"schedule":"0  0 * * 0"}`)
	empty := write("old.json", `null`)
	broken := write("broken.json", `{`)
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	tests := []struct {
		name     string
		getenv   func(string) string
		override string
		want     Trigger
		wantErr  bool
	}{
		{"github schedule", env("GITHUB_EVENT_NAME", "schedule", "GITHUB_EVENT_PATH", github), "", Trigger{Event: EventSchedule, Cron: "0 5 * * *"}, false},
		{"gitea schedule", env("GITHUB_EVENT_NAME", "schedule", "GITHUB_EVENT_PATH", gitea), "", Trigger{Event: EventSchedule, Cron: "0 0 * * 0"}, false},
		{"payload without cron", env("GITHUB_EVENT_NAME", "schedule", "GITHUB_EVENT_PATH", empty), "", Trigger{Event: EventSchedule}, false},
		{"unreadable payload", env("GITHUB_EVENT_NAME", "schedule", "GITHUB_EVENT_PATH", broken), "", Trigger{Event: EventSchedule}, true},
		{"missing payload path", env("GITHUB_EVENT_NAME", "schedule"), "", Trigger{Event: EventSchedule}, true},
		{"dispatch", env("GITHUB_EVENT_NAME", "workflow_dispatch", "GITHUB_EVENT_PATH", github), "", Trigger{Event: "workflow_dispatch"}, false},
		{"push", env("GITHUB_EVENT_NAME", "push"), "", Trigger{Event: "push"}, false},
		{"no CI", env(), "", Trigger{}, false},
		{"override", env("GITHUB_EVENT_NAME", "push"), " 0 0 * * 0", Trigger{Event: EventSchedule, Cron: "0 0 * * 0"}, false},
	}
	for _, tt := range tests {
		got, err := Detect(tt.getenv, tt.override)
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("%s: Detect = %+v, %v; want %+v, err %v", tt.name, got, err, tt.want, tt.wantErr)
		}
	}
}
