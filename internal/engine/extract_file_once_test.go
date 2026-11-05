package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

const sharedWorkflow = "on: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    container: node:20.11\n" +
	"    services:\n      db:\n        image: postgres:16\n    steps:\n" +
	"      - uses: actions/checkout@v4\n      - uses: actions/setup-go@v5\n      - uses: docker://alpine:3.19\n"

// srcRecorder records the src slice its Extract got, to check that a file's managers share one.
type srcRecorder struct{ got *[]byte }

func (srcRecorder) Name() string                           { return "rec" }
func (srcRecorder) Match(string) bool                      { return true }
func (srcRecorder) Edits(dep.Update) ([]patch.Edit, error) { return nil, nil }
func (r srcRecorder) Extract(_ string, src []byte, env manager.Env) ([]dep.Dep, error) {
	*r.got = src
	if env.YAML == nil {
		return nil, fmt.Errorf("no shared parse")
	}
	return nil, nil
}

// TestExtractAllPerFile checks that the managers of one file get the same bytes and a shared
// parse, that the results keep job order, and that a read error reaches every job of the file.
func TestExtractAllPerFile(t *testing.T) {
	root := t.TempDir()
	wf := ".github/workflows/ci.yml"
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, wf), []byte(sharedWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}
	var a, b []byte
	jobs := []job{{wf, actions.New()}, {wf, srcRecorder{&a}}, {"missing.yml", docker.New()}, {wf, docker.New()}, {wf, srcRecorder{&b}}, {"missing.yml", actions.New()}}
	ex := extractAll(root, jobs, manager.Env{}, noProgress{})
	if len(a) == 0 || &a[0] != &b[0] {
		t.Error("managers of one file got different src slices")
	}
	for _, i := range []int{0, 3} {
		want, err := jobs[i].m.Extract(wf, []byte(sharedWorkflow), manager.Env{})
		if err != nil || len(want) == 0 || ex[i].err != nil || !reflect.DeepEqual(ex[i].deps, want) {
			t.Errorf("%s: shared parse %+v, %v; own parse %+v, %v", jobs[i].m.Name(), ex[i].deps, ex[i].err, want, err)
		}
	}
	if ex[1].err != nil || ex[4].err != nil {
		t.Errorf("recorders: %v, %v", ex[1].err, ex[4].err)
	}
	if !os.IsNotExist(ex[2].err) || !os.IsNotExist(ex[5].err) {
		t.Errorf("missing file: %v, %v; want not-exist for both jobs", ex[2].err, ex[5].err)
	}
}

// BenchmarkExtractAllWorkflows extracts 300 workflows with images, each matched by the
// github-actions and the docker manager.
func BenchmarkExtractAllWorkflows(b *testing.B) {
	root := b.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	steps := strings.Repeat("      - uses: actions/cache@v4\n        with:\n          path: ~/.cache\n          key: k\n", 20)
	var jobs []job
	for i := range 300 {
		name := fmt.Sprintf("wf%d.yml", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(sharedWorkflow+steps), 0o644); err != nil {
			b.Fatal(err)
		}
		jobs = append(jobs, job{".github/workflows/" + name, actions.New()})
	}
	for i := range 300 {
		jobs = append(jobs, job{jobs[i].file, docker.New()})
	}
	b.ReportAllocs()
	for b.Loop() {
		extractAll(root, jobs, manager.Env{}, noProgress{})
	}
}
