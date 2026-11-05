package engine

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestResultSkipDep(t *testing.T) {
	a := dep.Dep{File: "go.mod", Line: 3, Name: "a"}
	b := dep.Dep{File: "go.mod", Line: 4, Name: "b"}
	prior := b
	prior.Skip = "earlier"
	res := &Result{Deps: []dep.Dep{a, b}, Skipped: []dep.Dep{prior}}

	res.SkipDep(a, "cannot be applied")
	res.SkipDep(b, "cannot be applied")

	if res.Deps[0].Skip != "cannot be applied" || res.Deps[1].Skip != "cannot be applied" {
		t.Errorf("Deps = %+v, want both marked", res.Deps)
	}
	if len(res.Skipped) != 2 || res.Skipped[0].Skip != "earlier" || res.Skipped[1].Name != "a" || res.Skipped[1].Skip != "cannot be applied" {
		t.Errorf("Skipped = %+v, want earlier b kept and a appended once", res.Skipped)
	}
	if a.Skip != "" {
		t.Error("SkipDep modified the caller's dep")
	}
}
