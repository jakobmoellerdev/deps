package docgen

import (
	"maps"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// ManagersInput are the facts of the managers and datasources reference.
type ManagersInput struct {
	Intro          string        // markdown before the contents
	Skips          []refdoc.Skip // skip reasons shared by every manager
	Managers       []refdoc.Manager
	Datasources    []refdoc.Datasource
	Aliases        map[string]string // Renovate datasource name → deps datasource name
	Shared         refdoc.Section    // what every datasource shares
	Notes          []refdoc.Section  // further sections after the datasources (OSV, checksums, …)
	IgnoredDefault func(depType string) bool
}

// Managers renders the managers and datasources reference: per manager its summary, files,
// dep types (with whether each is updated or ignored by default), skip reasons and sections,
// then the datasource table, aliases and notes.
func Managers(in ManagersInput) ([]byte, error) {
	d := newDoc("Managers and datasources")
	d.para(in.Intro)
	d.heading(2, "Contents")
	toc := make([]string, 0, len(in.Managers)+2)
	if len(in.Skips) > 0 {
		toc = append(toc, "[Skip reasons of every manager](#skip-reasons-of-every-manager)")
	}
	for _, m := range in.Managers {
		toc = append(toc, "["+codeSpan(m.Name)+"](#"+Anchor(m.Name)+")")
	}
	toc = append(toc, "[Datasources](#datasources)")
	d.list(toc)
	if len(in.Skips) > 0 {
		d.heading(2, "Skip reasons of every manager")
		skipTable(d, in.Skips)
	}
	for _, m := range in.Managers {
		d.heading(2, m.Name)
		d.para(m.Summary)
		d.para("**Files:**")
		d.list(m.Files)
		rows := make([][]string, 0, len(m.DepTypes))
		for _, t := range m.DepTypes {
			def := "updated"
			if in.IgnoredDefault != nil && in.IgnoredDefault(t.Name) {
				def = "ignored (opt in with a rule setting `ignore: false`)"
			}
			rows = append(rows, []string{codeSpan(t.Name), t.What, datasourceColumn(t), t.Versioning, def})
		}
		d.table([]string{"Dep type", "Extracted", "Datasource", "Versioning", "Default"}, rows)
		if len(m.Skips) > 0 {
			d.para("**Skips:**")
			skipTable(d, m.Skips)
		}
		// Titles repeat across managers (Edits, Versioning), so they are labels, not headings.
		for _, s := range m.Sections {
			d.para("**" + s.Title + ".**")
			d.para(s.Body)
			d.list(s.Items)
		}
	}
	d.heading(2, "Datasources")
	d.para(in.Shared.Body)
	d.list(in.Shared.Items)
	rows := make([][]string, 0, len(in.Datasources))
	for _, ds := range in.Datasources {
		rows = append(rows, []string{codeSpan(ds.Name), ds.NameForm, ds.Endpoints, ds.Auth, ds.ReleaseTimes})
	}
	d.table([]string{"Datasource", "Dep name", "Endpoints", "Auth", "Release times"}, rows)
	if len(in.Aliases) > 0 {
		as := make([]string, 0, len(in.Aliases))
		for _, k := range slices.Sorted(maps.Keys(in.Aliases)) {
			as = append(as, codeSpan(k)+" = "+codeSpan(in.Aliases[k]))
		}
		d.para("Renovate datasource names are accepted as aliases (`custom[].datasource`, `rules[].datasources`, markers): " + strings.Join(as, ", ") + ".")
	}
	for _, ds := range in.Datasources {
		for _, n := range ds.Notes {
			d.section(3, n)
		}
	}
	for _, n := range in.Notes {
		d.section(3, n)
	}
	return d.bytes()
}

func skipTable(d *doc, skips []refdoc.Skip) {
	rows := make([][]string, 0, len(skips))
	for _, s := range skips {
		rows = append(rows, []string{codeSpan(s.Reason), s.When})
	}
	d.table([]string{"Reason", "When"}, rows)
}

// datasourceColumn is the datasource column of dep type t: its Datasource text, else its
// Datasources as code spans.
func datasourceColumn(t refdoc.DepType) string {
	if t.Datasource != "" {
		return t.Datasource
	}
	spans := make([]string, len(t.Datasources))
	for i, ds := range t.Datasources {
		spans[i] = codeSpan(ds)
	}
	return strings.Join(spans, ", ")
}
