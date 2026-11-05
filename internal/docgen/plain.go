package docgen

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// linkRe finds an inline markdown link [text](target).
var linkRe = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]*)\)`)

// tableRuleRe matches the delimiter row of a markdown table (|---|:---:|).
var tableRuleRe = regexp.MustCompile(`^\s*\|[-:| ]+\|\s*$`)

// listRe matches the marker of a list item ("- ", "* ", "1. ") with its indentation.
var listRe = regexp.MustCompile(`^(\s*)([-*]|\d+\.) `)

// MarkdownToPlain renders markdown (help texts, schema.md and PlainToMarkdown output) as plain
// text for a terminal. Inline markup is removed: code spans lose their backticks, bold and
// emphasis their markers, escapes their backslash, and links keep only their text. The lines of
// a paragraph or list item are joined and wrapped at width columns (runes), list items with a
// hanging indent; headings lose their '#'. Fenced code blocks are indented by four spaces. A
// table loses its header row: each row becomes its first cell on a line, followed by the other
// cells indented below ("[middle; cells] last cell").
func MarkdownToPlain(md string, width int) string {
	var b strings.Builder
	var para []string
	flush := func() {
		if len(para) > 0 {
			b.WriteString(wrap(inlinePlain(strings.Join(para, " ")), "", width) + "\n")
			para = para[:0]
		}
	}
	fence := false
	lines := strings.Split(md, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```"):
			flush()
			fence = !fence
		case fence:
			b.WriteString(strings.TrimRight("    "+line, " ") + "\n")
		case strings.HasPrefix(trimmed, "|"):
			flush()
			if i+1 < len(lines) && tableRuleRe.MatchString(lines[i+1]) {
				i++ // the header row and the delimiter row
				continue
			}
			if tableRuleRe.MatchString(line) {
				continue
			}
			cells := tableCells(trimmed)
			if len(cells) == 0 {
				continue
			}
			b.WriteString(inlinePlain(cells[0]) + "\n")
			if len(cells) > 1 {
				desc := cells[len(cells)-1]
				if len(cells) > 2 {
					desc = "[" + strings.Join(cells[1:len(cells)-1], "; ") + "] " + desc
				}
				b.WriteString(wrap(inlinePlain(desc), "    ", width) + "\n")
			}
		case trimmed == "":
			flush()
			b.WriteString("\n")
		case strings.HasPrefix(trimmed, "#"):
			flush()
			b.WriteString(strings.TrimSpace(strings.TrimLeft(trimmed, "#")) + "\n")
		case listRe.MatchString(line):
			flush()
			para = append(para, strings.TrimRight(line, " "))
		case len(para) > 0:
			para = append(para, trimmed)
		default:
			para = append(para, strings.TrimRight(line, " "))
		}
	}
	flush()
	return strings.TrimRight(b.String(), "\n")
}

// tableCells splits a table row at the '|' outside code spans; `\|` is a literal '|'.
func tableCells(row string) []string {
	var cells []string
	var cell strings.Builder
	code := false
	for i := 0; i < len(row); i++ {
		switch c := row[i]; {
		case c == '\\' && i+1 < len(row) && row[i+1] == '|':
			cell.WriteByte('|')
			i++
		case c == '`':
			code = !code
			cell.WriteByte(c)
		case c == '|' && !code:
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
		default:
			cell.WriteByte(c)
		}
	}
	cells = append(cells, strings.TrimSpace(cell.String()))
	// The leading and trailing '|' leave empty outer cells.
	if len(cells) > 0 && cells[0] == "" {
		cells = cells[1:]
	}
	if len(cells) > 0 && cells[len(cells)-1] == "" {
		cells = cells[:len(cells)-1]
	}
	return cells
}

// inlinePlain removes the inline markdown of one line (see MarkdownToPlain).
func inlinePlain(s string) string {
	s = linkRe.ReplaceAllString(s, "$1")
	var b strings.Builder
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == '`':
			n := 1
			for i+n < len(s) && s[i+n] == '`' {
				n++
			}
			fence := strings.Repeat("`", n)
			end := strings.Index(s[i+n:], fence)
			if end < 0 { // unbalanced: literal backticks
				b.WriteString(fence)
				i += n
				continue
			}
			code := s[i+n : i+n+end]
			if n > 1 {
				code = strings.TrimPrefix(strings.TrimSuffix(code, " "), " ")
			}
			if strings.TrimSpace(code) != code { // significant spaces stay visible
				code = "'" + code + "'"
			}
			b.WriteString(code)
			i += n + end + n
		case c == '\\' && i+1 < len(s) && isASCIIPunct(s[i+1]):
			b.WriteByte(s[i+1])
			i += 2
		case c == '*' && strings.HasPrefix(s[i:], "**"):
			i += 2
		case c == '*' && emphasis(s, i):
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func isASCIIPunct(c byte) bool {
	return strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", c) >= 0
}

// emphasis reports whether the '*' at s[i] delimits emphasis: it touches a non-space and is no
// list marker.
func emphasis(s string, i int) bool {
	if strings.TrimSpace(s[:i]) == "" && strings.HasPrefix(s[i:], "* ") {
		return false
	}
	before := i > 0 && s[i-1] != ' '
	after := i+1 < len(s) && s[i+1] != ' '
	return before || after
}

// wrap breaks line at spaces so lines stay within width columns (runes) where words allow.
// indent prefixes every line; a list item continues under its text, other lines under their
// first word.
func wrap(line, indent string, width int) string {
	line = indent + line
	if utf8.RuneCountInString(line) <= width {
		return line
	}
	lead := len(line) - len(strings.TrimLeft(line, " "))
	hang := lead
	if m := listRe.FindStringSubmatch(line); m != nil {
		hang = len(m[0])
	}
	var b strings.Builder
	col := 0
	for i, w := range strings.Fields(line) {
		n := utf8.RuneCountInString(w)
		switch {
		case i == 0:
			b.WriteString(strings.Repeat(" ", lead) + w)
			col = lead + n
		case col+1+n > width:
			b.WriteString("\n" + strings.Repeat(" ", hang) + w)
			col = hang + n
		default:
			b.WriteString(" " + w)
			col += 1 + n
		}
	}
	return b.String()
}
