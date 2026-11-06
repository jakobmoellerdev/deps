package actions

import (
	"os"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/githubrunners"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func runnerDeps(t *testing.T, file string, src []byte) []dep.Dep {
	t.Helper()
	deps, err := New().Extract(file, src, env)
	if err != nil {
		t.Fatal(err)
	}
	var out []dep.Dep
	for _, d := range deps {
		if d.DepType == DepTypeRunner {
			out = append(out, d)
		}
	}
	return out
}

func TestExtractRunners(t *testing.T) {
	src, err := os.ReadFile("testdata/runners.yml")
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"lf": src, "crlf": []byte(strings.ReplaceAll(string(src), "\n", "\r\n"))} {
		t.Run(name, func(t *testing.T) {
			got := runnerDeps(t, ".github/workflows/ci.yml", b)
			type want struct {
				name, version, label, skip string
				line                       int
			}
			wants := []want{
				{"ubuntu", "latest", "", "non-version value", 11},
				{"ubuntu", "24.04", "ubuntu-24.04", "", 15},
				{"ubuntu", "24.04-arm", "ubuntu-24.04-arm", "", 21},
				{"ubuntu", "22.04", "ubuntu-22.04", "", 23},
				{"windows", "2022", "windows-2022", "", 27},
				{"macos", "14-large", "macos-14-large", "", 29},
				{"macos", "13", "macos-13", "", 32},
				{"xcode", "27-xlarge", "xcode-27-xlarge", "", 34},
			}
			if len(got) != len(wants) {
				t.Fatalf("got %d runner deps, want %d: %+v", len(got), len(wants), got)
			}
			for i, w := range wants {
				d := got[i]
				if d.Name != w.name || d.Version != w.version || d.Skip != w.skip || d.Line != w.line {
					t.Errorf("dep %d = %s %s skip %q line %d, want %+v", i, d.Name, d.Version, d.Skip, d.Line, w)
				}
				if d.Manager != Name || d.Datasource != githubrunners.Name || d.Versioning != "docker" || d.Source != githubrunners.SourceURL {
					t.Errorf("dep %d = %+v", i, d)
				}
				if w.skip != "" {
					continue
				}
				// The span covers the version right after "<name>-".
				if s := d.RefSpan.Start - len(w.name) - 1; s < 0 || string(b[s:d.RefSpan.End]) != w.label {
					t.Errorf("dep %d span %+v does not cover %q", i, d.RefSpan, w.label)
				}
			}
		})
	}
}

func TestExtractRunnersForge(t *testing.T) {
	src := []byte("jobs:\n  a:\n    runs-on: ubuntu-22.04\n")
	for _, f := range []string{".gitea/workflows/ci.yml", ".forgejo/workflows/ci.yml", "sub/.gitea/workflows/ci.yml"} {
		if got := runnerDeps(t, f, src); len(got) != 0 {
			t.Errorf("%s: got %+v, want no runner deps", f, got)
		}
	}
	for _, f := range []string{".github/workflows/ci.yml", "workflow-templates/ci.yml"} {
		if got := runnerDeps(t, f, src); len(got) != 1 {
			t.Errorf("%s: got %+v, want one runner dep", f, got)
		}
	}
}

func TestExtractRunnersNoJobs(t *testing.T) {
	for _, src := range []string{"", "runs:\n  using: composite\n", "jobs: [a]\n", "- a\n", "jobs:\n  a: x\n"} {
		if got := runnerDeps(t, "action.yml", []byte(src)); len(got) != 0 {
			t.Errorf("%q: got %+v", src, got)
		}
	}
}

func TestRunnerEdits(t *testing.T) {
	src, err := os.ReadFile("testdata/runners.yml")
	if err != nil {
		t.Fatal(err)
	}
	next := map[string]string{"24.04": "26.04", "24.04-arm": "26.04-arm", "22.04": "24.04", "2022": "2025", "14-large": "15-large", "13": "26", "27-xlarge": "28-xlarge"}
	var edits []patch.Edit
	for _, d := range runnerDeps(t, ".github/workflows/ci.yml", src) {
		if d.Skip != "" {
			continue
		}
		e, err := New().Edits(dep.Update{Dep: d, NewVersion: next[d.Version]})
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"runs-on: &linux_runner_ubuntu ubuntu-26.04\n",
		"runs-on: *linux_runner_ubuntu\n",
		"runs-on: ubuntu-26.04-arm\n",
		`runs-on: ["ubuntu-24.04"]` + "\n",
		"runs-on: 'windows-2025'\n",
		`runs-on: [self-hosted, "macos-15-large"]` + "\n",
		"runs-on:\n      macos-26\n",
		"runs-on: xcode-28-xlarge\n",
		"runs-on: ubuntu-latest\n",
		"labels: ubuntu-22.04\n",
		"default: ubuntu-22.04\n",
	} {
		if !strings.Contains(string(out), line) {
			t.Errorf("edited workflow lacks %q:\n%s", line, out)
		}
	}
}
