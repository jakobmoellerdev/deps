package engine

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
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
