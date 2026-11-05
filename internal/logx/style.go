package logx

// SGR parameters of ANSI escape sequences for Style.Paint.
const (
	Bold    = "1"
	Dim     = "2"
	Red     = "31"
	Green   = "32"
	Yellow  = "33"
	Blue    = "34"
	Magenta = "35"
	Cyan    = "36"
	BoldRed = "1;31"
	Accent  = "1;35" // bold magenta
)

// Style paints text with ANSI colors, or leaves it unchanged when colors are off.
type Style struct{ color bool }

// NewStyle returns the style of a ColorMode.Enabled outcome.
func NewStyle(color bool) Style { return Style{color: color} }

// Color reports whether the style paints.
func (s Style) Color() bool { return s.color }

// Paint wraps text in the SGR sequence sgr (Bold, Red, …) and a reset.
func (s Style) Paint(sgr, text string) string {
	if !s.color || sgr == "" || text == "" {
		return text
	}
	return "\x1b[" + sgr + "m" + text + "\x1b[0m"
}

// appendPaint is Paint appending to b.
func appendPaint(b []byte, color bool, sgr, text string) []byte {
	if !color || text == "" {
		return append(b, text...)
	}
	b = append(b, "\x1b["...)
	b = append(b, sgr...)
	b = append(b, 'm')
	b = append(b, text...)
	return append(b, "\x1b[0m"...)
}
