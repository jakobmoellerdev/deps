package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		{"* * * 13 *", "month: value 13 out of range 1-12"},
		{"* * * * 8", "day of week: value 8 out of range 0-7"},
	}
	for _, tt := range tests {
		_, err := Parse([]string{tt.expr})
		if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), `"`+tt.expr+`"`) {
			t.Errorf("Parse(%q) err = %v, want %q", tt.expr, err, tt.want)
		}
	}
}

func TestNilSet(t *testing.T) {
	s, err := Parse(nil)
	if err != nil || s != nil {
		t.Fatalf("Parse(nil) = %v, %v", s, err)
	}
	if !s.Matches(Trigger{Event: EventSchedule, Cron: "0 5 * * *"}) || !s.Matches(Trigger{Event: "push"}) || s.String() != "always" || s.Crons() != nil {
		t.Errorf("nil set: string=%q", s.String())
	}
}

func TestMatches(t *testing.T) {
	s, err := Parse([]string{"0 0 * * *", "  0\t0 *  * 0 "})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		t    Trigger
		want bool
	}{
		{"exact cron", Trigger{EventSchedule, "0 0 * * *"}, true},
		{"normalized whitespace", Trigger{EventSchedule, "0 0 * *   0"}, true},
		{"other cron", Trigger{EventSchedule, "0 5 * * *"}, false},
		{"no time evaluation", Trigger{EventSchedule, "0 0 * * 7"}, false},
		{"schedule without cron", Trigger{EventSchedule, ""}, true},
		{"workflow_dispatch", Trigger{EventWorkflowDispatch, ""}, true},
		{"push is outside every schedule", Trigger{"push", ""}, false},
		{"issues (dashboard ticked) is maintenance", Trigger{"issues", ""}, false},
		{"workflow_run is maintenance", Trigger{"workflow_run", ""}, false},
		{"repository_dispatch", Trigger{EventRepositoryDispatch, ""}, true},
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
	if got := s.Crons(); len(got) != 2 || got[1] != "0 0 * * 0" {
		t.Errorf("Crons() = %q", got)
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
		{"github schedule", env("GITHUB_EVENT_NAME", "schedule", "GITHUB_EVENT_PATH", github), "", Trigger{EventSchedule, "0 5 * * *"}, false},
		{"gitea schedule", env("GITHUB_EVENT_NAME", "schedule", "GITHUB_EVENT_PATH", gitea), "", Trigger{EventSchedule, "0 0 * * 0"}, false},
		{"payload without cron", env("GITHUB_EVENT_NAME", "schedule", "GITHUB_EVENT_PATH", empty), "", Trigger{EventSchedule, ""}, false},
		{"unreadable payload", env("GITHUB_EVENT_NAME", "schedule", "GITHUB_EVENT_PATH", broken), "", Trigger{EventSchedule, ""}, true},
		{"missing payload path", env("GITHUB_EVENT_NAME", "schedule"), "", Trigger{EventSchedule, ""}, true},
		{"dispatch", env("GITHUB_EVENT_NAME", "workflow_dispatch", "GITHUB_EVENT_PATH", github), "", Trigger{"workflow_dispatch", ""}, false},
		{"push", env("GITHUB_EVENT_NAME", "push"), "", Trigger{"push", ""}, false},
		{"no CI", env(), "", Trigger{}, false},
		{"override", env("GITHUB_EVENT_NAME", "push"), " 0 0 * * 0", Trigger{EventSchedule, "0 0 * * 0"}, false},
	}
	for _, tt := range tests {
		got, err := Detect(tt.getenv, tt.override)
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("%s: Detect = %+v, %v; want %+v, err %v", tt.name, got, err, tt.want, tt.wantErr)
		}
	}
}
