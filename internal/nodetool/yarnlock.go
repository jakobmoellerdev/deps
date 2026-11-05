package nodetool

import (
	"bufio"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// yarnVersions reads the locked versions of ws from a yarn.lock, classic or berry, streamed
// line by line: an entry starts with an unindented header listing its descriptors
// (`ms@^2.1.0, "@s/a@~1.0.0":` classic, `"ms@npm:^2.1.0, ms@npm:^2.1.1":` berry) and holds its
// version on a `  version "2.1.2"` (classic) or `  version: 2.1.2` (berry) line. A want's
// descriptor is `<name>@<range>`, or `<name>@npm:<range>` in berry, which adds the default
// protocol; wants without a range are not locked. The descriptors are shared by the whole
// workspace, so the member does not matter.
func yarnVersions(r io.Reader, ws []lockWant) ([]string, error) {
	want := map[string][]int{}
	for i, w := range ws {
		if w.rng == "" {
			continue
		}
		want[w.name+"@"+w.rng] = append(want[w.name+"@"+w.rng], i)
		want[w.name+"@npm:"+w.rng] = append(want[w.name+"@npm:"+w.rng], i)
	}
	out := make([]string, len(ws))
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 16<<20)
	var cur []int // the wants of the entry being read
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "" || line[0] == '#':
			cur = nil
		case line[0] != ' ':
			cur = cur[:0]
			for _, d := range yarnDescriptors(line) {
				cur = append(cur, want[d]...)
			}
		case len(cur) > 0:
			rest, ok := strings.CutPrefix(line, "  version")
			if !ok || rest == "" || (rest[0] != ' ' && rest[0] != ':') {
				continue
			}
			v := strings.Trim(strings.TrimSpace(strings.TrimPrefix(rest, ":")), `"`)
			for _, i := range cur {
				out[i] = v
			}
			cur = cur[:0]
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// yarnDescriptors returns the descriptors of a yarn.lock entry header: comma-separated, each
// quoted (classic) or all of them quoted together (berry), ending with a colon.
func yarnDescriptors(header string) []string {
	header = strings.TrimSuffix(strings.TrimRight(header, " \r"), ":")
	ds := strings.Split(header, ", ")
	for i, d := range ds {
		ds[i] = strings.Trim(d, `"`)
	}
	return ds
}

// yarnEntry is an entry of a classic yarn.lock: its header line, descriptors and the indented
// lines below the header, each with its newline. changed is set when the descriptors no longer
// match the header.
type yarnEntry struct {
	header  string
	descs   []string
	body    string
	changed bool
}

// moveDescriptors edits the classic yarn.lock installed from pinned manifests (pinManifests):
// the descriptor `<name>@<range>` of each lockfile-only update leaves its entry (which goes
// away when no descriptor is left) and joins the entry of the pinned `<name>@<version>`, so the
// next install from the unpinned manifests keeps the new version. Changed headers and the
// entry order follow yarn's own output (descriptors sorted, quoted where yarn quotes them;
// entries ordered by their first descriptor), since yarn keeps a lockfile that already matches
// its resolution as it is; the other headers are kept as written.
func moveDescriptors(lock []byte, locks map[string][]dep.Update) ([]byte, error) {
	var moves [][2]string
	for _, ups := range locks {
		for _, u := range ups {
			moves = append(moves, [2]string{u.Dep.Name + "@" + u.Dep.Range, u.Dep.Name + "@" + u.NewVersion})
		}
	}
	slices.SortFunc(moves, func(a, b [2]string) int { return strings.Compare(a[0]+"\x00"+a[1], b[0]+"\x00"+b[1]) })
	moves = slices.Compact(moves)

	preamble, entries := parseYarnLock(string(lock))
	for _, m := range moves {
		to := slices.IndexFunc(entries, func(e yarnEntry) bool { return slices.Contains(e.descs, m[1]) })
		if to < 0 {
			return nil, fmt.Errorf("%s has no entry for %s after installing it", YarnLock, m[1])
		}
		if slices.Contains(entries[to].descs, m[0]) {
			continue
		}
		entries[to].descs = append(entries[to].descs, m[0])
		entries[to].changed = true
		for i := range entries {
			if n := len(entries[i].descs); i != to {
				entries[i].descs = slices.DeleteFunc(entries[i].descs, func(d string) bool { return d == m[0] })
				entries[i].changed = entries[i].changed || len(entries[i].descs) != n
			}
		}
	}
	entries = slices.DeleteFunc(entries, func(e yarnEntry) bool { return len(e.descs) == 0 })
	for i := range entries {
		slices.Sort(entries[i].descs)
	}
	slices.SortStableFunc(entries, func(a, b yarnEntry) int { return strings.Compare(a.descs[0], b.descs[0]) })

	var b strings.Builder
	b.WriteString(preamble)
	for i, e := range entries {
		if i > 0 {
			b.WriteByte('\n')
		}
		if !e.changed {
			b.WriteString(e.header)
			b.WriteString(e.body)
			continue
		}
		for j, d := range e.descs {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString(yarnKey(d))
		}
		b.WriteString(":\n")
		b.WriteString(e.body)
	}
	return []byte(b.String()), nil
}

// parseYarnLock splits a classic yarn.lock into the comments and blank lines before the first
// entry and the entries.
func parseYarnLock(lock string) (preamble string, entries []yarnEntry) {
	for line := range strings.Lines(lock) {
		switch {
		case line[0] == ' ':
			if n := len(entries); n > 0 {
				entries[n-1].body += line
			}
		case strings.TrimSpace(line) == "" || line[0] == '#':
			if len(entries) == 0 {
				preamble += line
			}
		default:
			entries = append(entries, yarnEntry{header: line, descs: yarnDescriptors(strings.TrimRight(line, "\n"))})
		}
	}
	return preamble, entries
}

// yarnKey writes a descriptor as yarn classic does in a header: JSON-quoted when it starts
// with true or false or with anything but a letter, or holds a colon, whitespace, a backslash,
// a quote, a comma or a bracket.
func yarnKey(d string) string {
	if d != "" && !strings.HasPrefix(d, "true") && !strings.HasPrefix(d, "false") &&
		(d[0] >= 'a' && d[0] <= 'z' || d[0] >= 'A' && d[0] <= 'Z') && !strings.ContainsAny(d, ":\\\",[] \t\n\r\v\f") {
		return d
	}
	q, _ := jsontext.AppendQuote(nil, d) // descriptors are valid UTF-8
	return string(q)
}
