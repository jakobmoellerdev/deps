package schedule

import (
	"strings"
	"testing"
	"time"
)

func TestParseErrors(t *testing.T) {
	tests := []struct{ expr, want string }{
		{"* * * *", "want 5 fields, got 4"},
		{"0 * * * *", "minute field must be *"},
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
		_, err := Parse([]string{tt.expr}, "")
		if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), `"`+tt.expr+`"`) {
			t.Errorf("Parse(%q) err = %v, want %q", tt.expr, err, tt.want)
		}
	}
	if _, err := Parse(nil, "Nowhere/City"); err == nil || err.Error() != `unknown time zone "Nowhere/City"` {
		t.Errorf("tz err = %v", err)
	}
}

func TestNilSet(t *testing.T) {
	s, err := Parse(nil, "Europe/Berlin")
	if err != nil || s != nil {
		t.Fatalf("Parse(nil) = %v, %v", s, err)
	}
	if !s.Contains(time.Now()) || s.String() != "always" {
		t.Errorf("nil set: contains=%v string=%q", s.Contains(time.Now()), s.String())
	}
}

func TestContains(t *testing.T) {
	// 2026-11-02 is a Monday.
	at := func(day, hour int) time.Time { return time.Date(2026, 11, day, hour, 30, 0, 0, time.UTC) }
	tests := []struct {
		name  string
		exprs []string
		tz    string
		in    []time.Time
		out   []time.Time
	}{
		{"always", []string{"* * * * *"}, "", []time.Time{at(2, 0), at(8, 23)}, nil},
		{"hours range", []string{"* 0-5 * * *"}, "", []time.Time{at(2, 0), at(2, 5)}, []time.Time{at(2, 6)}},
		{"list and step", []string{"* 1,*/12 * * *"}, "", []time.Time{at(2, 0), at(2, 1), at(2, 12)}, []time.Time{at(2, 2)}},
		{"range step", []string{"* 2-8/3 * * *"}, "", []time.Time{at(2, 2), at(2, 5), at(2, 8)}, []time.Time{at(2, 3)}},
		{"weekdays", []string{"* * * * 1-5"}, "", []time.Time{at(2, 9), at(6, 9)}, []time.Time{at(7, 9), at(8, 9)}},
		{"sunday as 7", []string{"* * * * 7"}, "", []time.Time{at(8, 9)}, []time.Time{at(7, 9)}},
		{"sunday as 0", []string{"* * * * 0"}, "", []time.Time{at(8, 9)}, []time.Time{at(2, 9)}},
		{"month", []string{"* * * 11 *"}, "", []time.Time{at(2, 9)}, []time.Time{time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}},
		{"dom or dow when both restricted", []string{"* * 3 * 0"}, "", []time.Time{at(3, 9), at(8, 9)}, []time.Time{at(4, 9)}},
		{"dom and dow when dow is star", []string{"* * 3 * *"}, "", []time.Time{at(3, 9)}, []time.Time{at(4, 9)}},
		{"union", []string{"* 1 * * *", "* 3 * * *"}, "", []time.Time{at(2, 1), at(2, 3)}, []time.Time{at(2, 2)}},
		// 23:30 UTC on Monday is 00:30 Tuesday in Berlin (CET).
		{"time zone", []string{"* 0 * * 2"}, "Europe/Berlin", []time.Time{at(2, 23)}, []time.Time{at(3, 0)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Parse(tt.exprs, tt.tz)
			if err != nil {
				t.Fatal(err)
			}
			for _, tm := range tt.in {
				if !s.Contains(tm) {
					t.Errorf("%s: want contained", tm)
				}
			}
			for _, tm := range tt.out {
				if s.Contains(tm) {
					t.Errorf("%s: want not contained", tm)
				}
			}
		})
	}
}

func TestString(t *testing.T) {
	exprs := []string{"* 0-5 * * 1-5", "* * * * 0"}
	s, err := Parse(exprs, "Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	exprs[0] = "mutated"
	if got, want := s.String(), "* 0-5 * * 1-5; * * * * 0 (Europe/Berlin)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
