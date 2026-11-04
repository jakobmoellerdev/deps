package precommit

import (
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

const (
	sha  = "1af3b93b6815bc44a9784bd300feb67ff0d1eeb3"
	sha2 = "2bf3b93b6815bc44a9784bd300feb67ff0d1eeb4"
)

const fixture = `# hooks
repos:
  - repo: https://github.com/pre-commit/pre-commit-hooks
    rev: v4.5.0  # keep
    hooks:
      - id: trailing-whitespace
  - repo: https://github.com/psf/black.git
    rev: "24.1.0"
  - repo: git@github.com:astral-sh/ruff-pre-commit.git
    rev: '` + sha + `'  # frozen: v0.3.0
  - repo: https://gitlab.com/group/sub/proj
    rev: ` + sha + ` # v1.0.0
  - repo: https://github.com/a/b
    rev: ` + sha + `
  - repo: https://github.com/c/d
    rev: main
  - repo: local
    hooks:
      - id: x
  - repo: meta
    hooks:
      - id: check-hooks-apply
  - repo: https://github.com/e/f
  - repo: file:///tmp/x
    rev: v1
`

func TestMatch(t *testing.T) {
	for p, want := range map[string]bool{
		".pre-commit-config.yaml":     true,
		"sub/.pre-commit-config.yml":  true,
		"pre-commit-config.yaml":      false,
		".pre-commit-config.yaml.bak": false,
		"x.pre-commit-config.yaml":    false,
	} {
		if got := New().Match(p); got != want {
			t.Errorf("Match(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestExtract(t *testing.T) {
	for name, src := range map[string]string{"lf": fixture, "crlf": strings.ReplaceAll(fixture, "\n", "\r\n")} {
		t.Run(name, func(t *testing.T) {
			b := []byte(src)
			deps, err := New().Extract(".pre-commit-config.yaml", b, manager.Env{})
			if err != nil {
				t.Fatal(err)
			}
			type want struct{ name, source, version, digest, ref, vspan, skip, afterInsert string }
			wants := []want{
				{"pre-commit/pre-commit-hooks", "https://github.com/pre-commit/pre-commit-hooks", "v4.5.0", "", "v4.5.0", "", "", "  # keep"},
				{"psf/black", "https://github.com/psf/black", "24.1.0", "", "24.1.0", "", "", ""},
				{"astral-sh/ruff-pre-commit", "https://github.com/astral-sh/ruff-pre-commit", "v0.3.0", sha, sha, "v0.3.0", "", "  # frozen"},
				{"group/sub/proj", "https://gitlab.com/group/sub/proj", "v1.0.0", sha, sha, "v1.0.0", "", " # v1.0.0"},
				{"a/b", "https://github.com/a/b", "", sha, sha, "", "pinned without version comment", ""},
				{"c/d", "https://github.com/c/d", "", "", "main", "", "non-version ref", ""},
				{"local", "", "", "", "", "", "local repository", ""},
				{"meta", "", "", "", "", "", "meta repository", ""},
				{"e/f", "https://github.com/e/f", "", "", "", "", "no rev", ""},
				{"file:///tmp/x", "", "", "", "", "", "unrecognized repo", ""},
			}
			if len(deps) != len(wants) {
				t.Fatalf("got %d deps, want %d: %+v", len(deps), len(wants), deps)
			}
			for i, w := range wants {
				d := deps[i]
				if d.Manager != Name || d.Name != w.name || d.Source != w.source || d.Version != w.version || d.Digest != w.digest || d.Skip != w.skip ||
					d.Datasource != "git-tags" || d.Versioning != "tag" || d.DepType != "repository" {
					t.Errorf("dep %d = %+v, want %+v", i, d, w)
					continue
				}
				if got := string(b[d.RefSpan.Start:d.RefSpan.End]); got != w.ref {
					t.Errorf("dep %d ref span = %q, want %q", i, got, w.ref)
				}
				if got := string(b[d.VersionSpan.Start:d.VersionSpan.End]); got != w.vspan {
					t.Errorf("dep %d version span = %q, want %q", i, got, w.vspan)
				}
				if w.afterInsert != "" && !strings.HasPrefix(string(b[d.InsertAt:]), w.afterInsert) {
					t.Errorf("dep %d insert at %q", i, b[d.InsertAt:min(d.InsertAt+12, len(b))])
				}
			}
		})
	}
}

func TestExtractEmpty(t *testing.T) {
	for _, src := range []string{"", "repos: []\n", "- a\n", "repos: x\n"} {
		deps, err := New().Extract(".pre-commit-config.yaml", []byte(src), manager.Env{})
		if err != nil || len(deps) != 0 {
			t.Errorf("%q: %v %v", src, deps, err)
		}
	}
	if _, err := New().Extract(".pre-commit-config.yaml", []byte("repos: [\n"), manager.Env{}); err == nil {
		t.Error("want yaml error")
	}
}

func TestEdits(t *testing.T) {
	src := []byte(fixture)
	deps, err := New().Extract(".pre-commit-config.yaml", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		u    dep.Update
		old  string
		new  string
	}{
		{"tag", dep.Update{Dep: deps[0], NewVersion: "v4.6.0"}, "rev: v4.5.0  # keep", "rev: v4.6.0  # keep"},
		{"quoted tag", dep.Update{Dep: deps[1], NewVersion: "24.2.0"}, `rev: "24.1.0"`, `rev: "24.2.0"`},
		{"frozen", dep.Update{Dep: deps[2], NewVersion: "v0.4.0", NewDigest: sha2}, "rev: '" + sha + "'  # frozen: v0.3.0", "rev: '" + sha2 + "'  # frozen: v0.4.0"},
		{"frozen digest only", dep.Update{Dep: deps[3], NewVersion: "v1.0.0", NewDigest: sha2}, "rev: " + sha + " # v1.0.0", "rev: " + sha2 + " # v1.0.0"},
		{"pin quoted", dep.Update{Dep: deps[1], NewVersion: "24.1.0", NewDigest: sha2}, `rev: "24.1.0"`, `rev: "` + sha2 + `" # frozen: 24.1.0`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			edits, err := New().Edits(tt.u)
			if err != nil {
				t.Fatal(err)
			}
			out, err := patch.Apply(src, edits)
			if err != nil {
				t.Fatal(err)
			}
			if want := strings.Replace(fixture, tt.old, tt.new, 1); string(out) != want {
				t.Errorf("got:\n%s\nwant:\n%s", out, want)
			}
		})
	}
	if _, err := New().Edits(dep.Update{Dep: deps[2], NewVersion: "v1"}); err == nil {
		t.Error("want error unpinning a frozen rev")
	}
}
