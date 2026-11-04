package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// planner records Plan calls and fails lookups that come before them.
type planner struct {
	planned []dep.Dep
	calls   int
}

func (p *planner) Plan(deps []dep.Dep) { p.planned, p.calls = append(p.planned, deps...), p.calls+1 }

func (p *planner) Releases(_ context.Context, d dep.Dep, _ string) ([]Release, error) {
	if p.calls == 0 {
		return nil, errNotPlanned
	}
	return []Release{{Version: "1.1.0"}}, nil
}

func (*planner) Digest(context.Context, dep.Dep, string) (string, error) { return "", nil }

type planError struct{}

func (planError) Error() string { return "lookup before Plan" }

var errNotPlanned = planError{}

func TestPlanner(t *testing.T) {
	root := t.TempDir()
	chart := "dependencies:\n  - name: a\n    version: 1.0.0\n    repository: https://example.com/charts\n" +
		"  - name: b\n    version: 1.0.0\n    repository: https://example.com/charts\n  - name: local\n    version: 1.0.0\n"
	if err := os.WriteFile(filepath.Join(root, "Chart.yaml"), []byte(chart), 0o644); err != nil {
		t.Fatal(err)
	}
	ms, err := Managers(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	p := &planner{}
	res, err := Resolve(t.Context(), root, config.Default(), manager.Env{}, Options{
		Managers:    ms,
		Datasources: map[string]Datasource{"helm": p, "docker": &planner{calls: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 || len(res.Updates) != 2 {
		t.Fatalf("errors %v, updates %+v", res.Errors, res.Updates)
	}
	// One call with the updatable deps only (the local chart is skipped).
	if p.calls != 1 || len(p.planned) != 2 || p.planned[0].Name != "a" || p.planned[1].Name != "b" {
		t.Errorf("Plan calls %d with %+v", p.calls, p.planned)
	}
}
