package engine

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestResultSkipDep(t *testing.T) {
	a := dep.Dep{File: "go.mod", Line: 3, Name: "a"}
	b := dep.Dep{File: "go.mod", Line: 4, Name: "b"}
	// Deps of the same name in another line, and of the same line in another file, are other
	// deps: only the one matching in file, line and name is marked.
	line := dep.Dep{File: "go.mod", Line: 9, Name: "a"}
	file := dep.Dep{File: "sub/go.mod", Line: 3, Name: "a"}
	prior := b
	prior.Skip = "earlier"
	res := &Result{Deps: []dep.Dep{line, file, a, b}, Skipped: []dep.Dep{prior}}

	res.SkipDep(a, "cannot be applied")
	res.SkipDep(b, "cannot be applied")

	if res.Deps[0].Skip != "" || res.Deps[1].Skip != "" {
		t.Errorf("Deps = %+v, want the other line and file untouched", res.Deps[:2])
	}
	if res.Deps[2].Skip != "cannot be applied" || res.Deps[3].Skip != "cannot be applied" {
		t.Errorf("Deps = %+v, want both marked", res.Deps)
	}
	if len(res.Skipped) != 2 || res.Skipped[0].Skip != "earlier" || res.Skipped[1].Name != "a" || res.Skipped[1].Skip != "cannot be applied" {
		t.Errorf("Skipped = %+v, want earlier b kept and a appended once", res.Skipped)
	}
	if a.Skip != "" {
		t.Error("SkipDep modified the caller's dep")
	}
}
