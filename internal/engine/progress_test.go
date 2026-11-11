package engine

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/piprequirements"
)

// recordProgress records the phase events; Done calls are counted per phase.
type recordProgress struct {
	mu     sync.Mutex
	events []string
	done   map[Phase]int
	errs   map[Phase]int
}

func (r *recordProgress) Start(p Phase, total int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fmt.Sprintf("start %s %d", p, total))
}

func (r *recordProgress) Done(p Phase, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done == nil {
		r.done, r.errs = map[Phase]int{}, map[Phase]int{}
	}
	r.done[p]++
	if err != nil {
		r.errs[p]++
	}
}

func (r *recordProgress) End(p Phase) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fmt.Sprintf("end %s", p))
}

func (*recordProgress) RateLimited(string, time.Time) {}

func TestResolveProgress(t *testing.T) {
	wf := "jobs:\n  a:\n    steps:\n" +
		"      - uses: ./local\n" +
		"      - uses: actions/checkout@v4.1.0\n" +
		"      - uses: owner/broken@v1\n"
	root, cfg := setup(t, "", map[string]string{".github/workflows/ci.yml": wf, ".github/workflows/b.yml": "jobs:\n  b:\n    steps:\n      - uses: actions/checkout@v4.1.0\n"})
	o := sources(tags, nil)
	p := &recordProgress{}
	o.Progress = p
	if _, err := Resolve(t.Context(), root, cfg, env, o); err != nil {
		t.Fatal(err)
	}
	// Two files; three updatable deps (./local is skipped at extraction), one failing.
	want := []string{"start discover 0", "end discover", "start extract 2", "end extract", "start resolve 3", "end resolve"}
	if !slices.Equal(p.events, want) {
		t.Errorf("events = %q, want %q", p.events, want)
	}
	if p.done[PhaseExtract] != 2 || p.errs[PhaseExtract] != 0 || p.done[PhaseResolve] != 3 || p.errs[PhaseResolve] != 1 {
		t.Errorf("done = %v, errors = %v", p.done, p.errs)
	}
}

// Deps looked up only when an advisory affects them (securityOnly: the indirect pins of a
// compiled requirements file) report their item of the resolve phase too, so the phase
// reaches the total it started with.
func TestResolveProgressSecurityOnly(t *testing.T) {
	root, cfg := setup(t, "", compiledTree())
	p := &recordProgress{}
	res, err := Resolve(t.Context(), root, cfg, env, Options{
		Managers:    []manager.Manager{piprequirements.New()},
		Datasources: map[string]Datasource{"pypi": compiledReleases},
		Security:    &fakeSec{vulns: map[string][]Vuln{"urllib3": {vuln("GHSA-u", "2.0.7")}}},
		Progress:    p,
	})
	if err != nil || len(res.Errors) > 0 {
		t.Fatal(err, res.Errors)
	}
	total := -1
	for _, e := range p.events {
		if n, ok := strings.CutPrefix(e, "start resolve "); ok {
			if total, err = strconv.Atoi(n); err != nil {
				t.Fatal(err)
			}
		}
	}
	// blinker is an indirect pin without an advisory: it is counted but never resolved.
	if total < 2 || p.done[PhaseResolve] != total {
		t.Errorf("resolve done %d of %d, events %q", p.done[PhaseResolve], total, p.events)
	}
}

// listProgress is a recordProgress that is a Lister.
type listProgress struct {
	recordProgress
	files []FileReport
	deps  []DepReport
}

func (l *listProgress) ListFiles(files []FileReport) {
	l.files = files
	l.events = append(l.events, "list files")
}

func (l *listProgress) ListDeps(deps []DepReport) {
	l.deps = deps
	l.events = append(l.events, "list deps")
}

// A Lister gets the files before the extract phase ends and the deps before the resolve
// phase ends, each sorted.
func TestResolveLister(t *testing.T) {
	wf := "jobs:\n  a:\n    steps:\n" +
		"      - uses: ./local\n" +
		"      - uses: actions/checkout@v4.1.0\n" +
		"      - uses: owner/broken@v1\n"
	root, cfg := setup(t, "", map[string]string{".github/workflows/ci.yml": wf, ".github/workflows/b.yml": "jobs:\n  b:\n    steps:\n      - uses: actions/checkout@v4.1.0\n"})
	o := sources(tags, nil)
	p := &listProgress{}
	o.Progress = p
	if _, err := Resolve(t.Context(), root, cfg, env, o); err != nil {
		t.Fatal(err)
	}
	want := []string{"start discover 0", "end discover", "start extract 2", "list files", "end extract", "start resolve 3", "list deps", "end resolve"}
	if !slices.Equal(p.events, want) {
		t.Errorf("events = %q, want %q", p.events, want)
	}
	files := []FileReport{
		{File: ".github/workflows/b.yml", Managers: []string{"github-actions"}, Deps: 1},
		{File: ".github/workflows/ci.yml", Managers: []string{"github-actions"}, Deps: 3, Skipped: 1},
	}
	if !slices.EqualFunc(p.files, files, func(a, b FileReport) bool {
		return a.File == b.File && slices.Equal(a.Managers, b.Managers) && a.Deps == b.Deps && a.Skipped == b.Skipped && a.Err == nil
	}) {
		t.Errorf("files = %+v", p.files)
	}
	var got []string
	for _, d := range p.deps {
		s := fmt.Sprintf("%s:%d %s", d.File, d.Line, d.Name)
		switch {
		case d.Err != nil:
			s += " error"
		case d.Skip != "":
			s += " skipped"
		case len(d.Updates) > 0:
			s += " -> " + d.Updates[0].NewVersion
		}
		got = append(got, s)
	}
	wantDeps := []string{
		".github/workflows/b.yml:4 actions/checkout -> v4.2.0",
		".github/workflows/ci.yml:5 actions/checkout -> v4.2.0",
		".github/workflows/ci.yml:6 owner/broken error",
	}
	if !slices.Equal(got, wantDeps) {
		t.Errorf("deps = %q, want %q", got, wantDeps)
	}
}
