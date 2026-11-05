package renovate

import "testing"

func TestTriggerCron(t *testing.T) {
	tests := []struct {
		entry, want string
	}{
		{"* * * * 0", "0 0 * * 0"},
		{"* 0-3 * * 1", "0 0 * * 1"},
		{"* 0-4,22-23 * * 1-5", "0 0 * * 1-5"},
		{"* * 1 */3 *", "0 0 1 */3 *"},
		{"* */2 * * *", "0 0 * * *"},
		{"15 5 * * *", "15 5 * * *"},
		{"* 9/2 * * *", "0 9 * * *"},
		{"* 22-23 * * *", "0 22 * * *"},
		{"before 6am on monday", "0 0 * * 1"},
		{"after 10pm every weekday", "0 22 * * 1-5"},
		{"before 4am on the first day of the month", "0 0 1 * *"},
		{"after 9:30pm and before 5am on saturday and sunday", "30 21 * * 0,6"},
		{"every weekend", "0 0 * * 0,6"},
		{"before 4am on monday", "0 0 * * 1"},
		{"every 3 months on the first day of the month", "0 0 1 */3 *"},
		{"  Before   4AM on Monday ", "0 0 * * 1"},
		{"on saturday, sunday", "0 0 * * 0,6"},
		{"on sunday and monday and sun", "0 0 * * 0,1"},
		{"every monday", "0 0 * * 1"},
		{"on mondays", "0 0 * * 1"},
		{"on fri", "0 0 * * 5"},
		{"after 12am", "0 0 * * *"},
		{"after 12pm", "0 12 * * *"},
		{"after 13:05", "5 13 * * *"},
		{"on the 15th day of the month", "0 0 15 * *"},
		{"every month", "0 0 * * *"},
	}
	for _, tt := range tests {
		got, anyTime, err := triggerCron(tt.entry)
		if err != nil || anyTime || got != tt.want {
			t.Errorf("triggerCron(%q) = %q, %v, %v; want %q", tt.entry, got, anyTime, err, tt.want)
		}
	}
}

func TestTriggerCronAnyTime(t *testing.T) {
	got, anyTime, err := triggerCron("At any  time")
	if err != nil || !anyTime || got != "" {
		t.Errorf("triggerCron(at any time) = %q, %v, %v", got, anyTime, err)
	}
}

func TestTriggerCronErrors(t *testing.T) {
	tests := []struct{ entry, want string }{
		{"every 2 weeks", `unsupported schedule "every 2 weeks"`},
		{"before 25am", `unsupported schedule "before 25am"`},
		{"on monday on tuesday", `unsupported schedule "on monday on tuesday"`},
		{"first day of week", `unsupported schedule "first day of week"`},
		{"before 4am after 5am", `unsupported schedule "before 4am after 5am"`},
		{"after 24:00", `unsupported schedule "after 24:00"`},
		{"on the 32nd day of the month", `unsupported schedule "on the 32nd day of the month"`},
		{"on monday and", `unsupported schedule "on monday and"`},
		{"", `unsupported schedule ""`},
		{"* 24 * * *", `unsupported schedule "* 24 * * *": hour: value 24 out of range 0-23`},
		{"* * * 13 *", `unsupported schedule "* * * 13 *": month: value 13 out of range 1-12`},
	}
	for _, tt := range tests {
		_, _, err := triggerCron(tt.entry)
		if err == nil || err.Error() != tt.want {
			t.Errorf("triggerCron(%q) err = %v, want %q", tt.entry, err, tt.want)
		}
	}
}
