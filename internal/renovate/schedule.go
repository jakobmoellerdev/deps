package renovate

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
)

// triggerCron converts one Renovate schedule entry into the workflow trigger cron
// that starts a run inside the Renovate window. anyTime reports "at any time".
func triggerCron(entry string) (cron string, anyTime bool, err error) {
	if f := strings.Fields(entry); len(f) == 5 && (f[0][0] == '*' || isDigit(f[0][0])) {
		return triggerFromCron(entry, f)
	}
	return triggerFromText(entry)
}

func triggerFromCron(entry string, f []string) (string, bool, error) {
	minute := f[0]
	if minute == "*" {
		minute = "0"
	}
	hour := f[1]
	first, _, _ := strings.Cut(hour, ",")
	if strings.HasPrefix(first, "*") {
		hour = "0"
	} else if n := leadingDigits(first); n != "" {
		hour = n
	}
	cron := strings.Join([]string{minute, hour, f[2], f[3], f[4]}, " ")
	if err := schedule.Validate(cron); err != nil {
		return "", false, fmt.Errorf("unsupported schedule %q: %w", entry, err)
	}
	return cron, false, nil
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func leadingDigits(s string) string {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return s[:i]
}

var (
	time12Re   = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?(am|pm)$`)
	time24Re   = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)
	ordinalRe  = regexp.MustCompile(`^(\d{1,2})(st|nd|rd|th)$`)
	weekdayIdx = map[string]int{
		"sunday": 0, "monday": 1, "tuesday": 2, "wednesday": 3, "thursday": 4, "friday": 5, "saturday": 6,
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}
)

// textSchedule parses a later.js-style text schedule clause by clause.
type textSchedule struct {
	toks            []string
	pos             int
	minute, hour    int
	dom, month, dow string
	hasTime         bool
}

func triggerFromText(entry string) (string, bool, error) {
	norm := strings.ToLower(strings.Join(strings.Fields(entry), " "))
	if norm == "at any time" {
		return "", true, nil
	}
	p := &textSchedule{toks: strings.Fields(strings.ReplaceAll(norm, ",", " , "))}
	if len(p.toks) == 0 || !p.parse() {
		return "", false, fmt.Errorf("unsupported schedule %q", entry)
	}
	cron := fmt.Sprintf("%d %d %s %s %s", p.minute, p.hour, or(p.dom), or(p.month), or(p.dow))
	if err := schedule.Validate(cron); err != nil {
		return "", false, fmt.Errorf("unsupported schedule %q: %w", entry, err)
	}
	return cron, false, nil
}

func or(s string) string {
	if s == "" {
		return "*"
	}
	return s
}

func (p *textSchedule) next() string {
	if p.pos >= len(p.toks) {
		return ""
	}
	t := p.toks[p.pos]
	p.pos++
	return t
}

func (p *textSchedule) peek(n int) string {
	if p.pos+n >= len(p.toks) {
		return ""
	}
	return p.toks[p.pos+n]
}

// expect consumes words if they all follow in order.
func (p *textSchedule) expect(words ...string) bool {
	for i, w := range words {
		if p.peek(i) != w {
			return false
		}
	}
	p.pos += len(words)
	return true
}

func (p *textSchedule) parse() bool {
	for p.pos < len(p.toks) {
		var ok bool
		switch p.next() {
		case "before":
			ok = p.timeClause(false)
		case "after":
			ok = p.timeClause(true)
		case "every":
			ok = p.every()
		case "on":
			ok = p.on()
		}
		if !ok {
			return false
		}
	}
	return true
}

func (p *textSchedule) timeClause(after bool) bool {
	if p.hasTime {
		return false
	}
	p.hasTime = true
	h, m, ok := parseTime(p.next())
	if !ok {
		return false
	}
	if !after {
		return true
	}
	p.hour, p.minute = h, m
	if p.expect("and", "before") {
		_, _, ok = parseTime(p.next())
	}
	return ok
}

func (p *textSchedule) every() bool {
	switch t := p.peek(0); t {
	case "weekend", "weekday":
		p.pos++
		return p.setDow(map[string]string{"weekend": "0,6", "weekday": "1-5"}[t])
	case "month":
		p.pos++
		return p.setMonth("*")
	}
	if n, err := strconv.Atoi(p.peek(0)); err == nil && p.peek(1) == "months" {
		p.pos += 2
		return n >= 1 && n <= 12 && p.setMonth("*/"+strconv.Itoa(n))
	}
	return p.days()
}

func (p *textSchedule) on() bool {
	if !p.expect("the") {
		return p.days()
	}
	t := p.next()
	dom := 0
	if t == "first" {
		dom = 1
	} else if m := ordinalRe.FindStringSubmatch(t); m != nil {
		dom, _ = strconv.Atoi(m[1])
	}
	if dom < 1 || dom > 31 || !p.expect("day", "of", "the", "month") || p.dom != "" {
		return false
	}
	p.dom = strconv.Itoa(dom)
	return true
}

// days parses a day list joined by "and" or ",".
func (p *textSchedule) days() bool {
	var set []int
	d, ok := weekday(p.next())
	if !ok {
		return false
	}
	set = append(set, d)
	for p.peek(0) == "and" || p.peek(0) == "," {
		d, ok := weekday(p.peek(1))
		if !ok {
			break
		}
		set = append(set, d)
		p.pos += 2
	}
	slices.Sort(set)
	set = slices.Compact(set)
	parts := make([]string, len(set))
	for i, d := range set {
		parts[i] = strconv.Itoa(d)
	}
	return p.setDow(strings.Join(parts, ","))
}

func (p *textSchedule) setDow(s string) bool {
	if p.dow != "" {
		return false
	}
	p.dow = s
	return true
}

func (p *textSchedule) setMonth(s string) bool {
	if p.month != "" {
		return false
	}
	p.month = s
	return true
}

func weekday(s string) (int, bool) {
	if d, ok := weekdayIdx[s]; ok {
		return d, true
	}
	if base, ok := strings.CutSuffix(s, "s"); ok && len(base) > 3 {
		d, ok := weekdayIdx[base]
		return d, ok
	}
	return 0, false
}

// parseTime parses <h>am|pm, <h>:<mm>am|pm or 24h <hh>:<mm>.
func parseTime(s string) (hour, minute int, ok bool) {
	if m := time12Re.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		if h < 1 || h > 12 || !parseMinute(m[2], &minute) {
			return 0, 0, false
		}
		h %= 12
		if m[3] == "pm" {
			h += 12
		}
		return h, minute, true
	}
	if m := time24Re.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		if h > 23 || !parseMinute(m[2], &minute) {
			return 0, 0, false
		}
		return h, minute, true
	}
	return 0, 0, false
}

func parseMinute(s string, minute *int) bool {
	if s == "" {
		*minute = 0
		return true
	}
	n, _ := strconv.Atoi(s)
	*minute = n
	return n <= 59
}
