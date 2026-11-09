package copier

import (
	"os"
	"path/filepath"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func TestMatch(t *testing.T) {
	m := New()
	for p, want := range map[string]bool{
		".copier-answers.yml": true, ".copier-answers.yaml": true, "sub/.copier-answers.yml": true,
		".copier-answers.ci.yml": true, "a/.copier-answers.docs.base.yaml": true,
		"copier-answers.yml": false, ".copier-answers.yml.bak": false, ".copier-answers.json": false,
		"x.copier-answers.yml": false, ".copier-answers..yml": false, "copier.yml": false,
	} {
		if m.Match(p) != want {
			t.Errorf("Match(%q) = %v", p, !want)
		}
	}
}

// TestExtractReal reads the answers files of mkdocstrings/python (a `gh:` alias),
// OCA/server-tools 18.0 (`git+https://`) and jupyterlab/jupyterlab-git (https), fetched
// 2026-11-09.
func TestExtractReal(t *testing.T) {
	for file, want := range map[string]struct{ name, source, version string }{
		"mkdocstrings-python/.copier-answers.yml": {"mkdocstrings/handler-template", "https://github.com/mkdocstrings/handler-template", "1.6.1"},
		"oca-server-tools/.copier-answers.yml":    {"OCA/oca-addons-repo-template", "https://github.com/OCA/oca-addons-repo-template", "v1.48"},
		"jupyterlab-git/.copier-answers.yml":      {"jupyterlab/extension-template", "https://github.com/jupyterlab/extension-template", "v4.2.1"},
	} {
		src, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		ds, err := New().Extract(file, src, manager.Env{})
		if err != nil || len(ds) != 1 {
			t.Fatalf("%s: %+v, %v", file, ds, err)
		}
		d := ds[0]
		if d.Name != want.name || d.Source != want.source || d.Version != want.version || d.Skip != "" ||
			d.Datasource != "git-tags" || d.Versioning != "pep440" || d.DepType != "template" || d.Line != 2 ||
			string(src[d.RefSpan.Start:d.RefSpan.End]) != want.version {
			t.Errorf("%s: %+v", file, d)
		}
		edits, err := New().Edits(dep.Update{Dep: d, NewVersion: "v9.9.9"})
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply(src, edits)
		if err != nil {
			t.Fatal(err)
		}
		if ds, _ := New().Extract(file, out, manager.Env{}); len(ds) != 1 || ds[0].Version != "v9.9.9" {
			t.Errorf("%s: after edit %s", file, out)
		}
	}
}

func TestExtract(t *testing.T) {
	for src, want := range map[string]struct{ name, source, version, skip string }{
		"_commit: v1.2.0\n_src_path: https://gitlab.com/g/sub/tpl.git\n":      {"g/sub/tpl", "https://gitlab.com/g/sub/tpl", "v1.2.0", ""},
		"_src_path: gl:g/tpl\n_commit: '2.0'\n":                               {"g/tpl", "https://gitlab.com/g/tpl", "2.0", ""},
		"_src_path: gh:/o/tpl.git\n_commit: \"1.0.0\"\n":                      {"o/tpl", "https://github.com/o/tpl", "1.0.0", ""},
		"_src_path: git@github.com:o/tpl.git\n_commit: 1.0.0\n":               {"o/tpl", "https://github.com/o/tpl", "1.0.0", ""},
		"_src_path: git+ssh://git@example.com:2222/o/tpl\n_commit: 1.0.0\n":   {"o/tpl", "https://example.com/o/tpl", "1.0.0", ""},
		"_src_path: ../template\n_commit: 1.0.0\n":                            {"../template", "", "1.0.0", skipLocal},
		"_src_path: /srv/tpl.bundle\n_commit: 1.0.0\n":                        {"/srv/tpl.bundle", "", "1.0.0", skipLocal},
		"_src_path: ~/tpl\n_commit: 1.0.0\n":                                  {"~/tpl", "", "1.0.0", skipLocal},
		"_src_path: file:///srv/tpl\n_commit: 1.0.0\n":                        {"file:///srv/tpl", "", "1.0.0", skipLocal},
		"_src_path: hg+https://x/y\n_commit: 1.0.0\n":                         {"hg+https://x/y", "", "1.0.0", skipUnsupported},
		"_src_path: gh:o/tpl\n_commit: v1.2.3-4-gabc1234\n":                   {"o/tpl", "https://github.com/o/tpl", "v1.2.3-4-gabc1234", skipCommit},
		"_src_path: gh:o/tpl\n_commit: 3f2a1bc\n":                             {"o/tpl", "https://github.com/o/tpl", "3f2a1bc", skipCommit},
		"_src_path: gh:o/tpl\n_commit: main\n":                                {"o/tpl", "https://github.com/o/tpl", "main", skipVersion},
		"_src_path: gh:o/tpl\n_commit: \"v1.\\x30\"\n":                        {"o/tpl", "https://github.com/o/tpl", "v1.0", skipLocate},
		"_src_path: gh:o/tpl\n_commit: |-\n  1.0.0\n":                         {"o/tpl", "https://github.com/o/tpl", "1.0.0", skipLocate},
		"# c\n_src_path: https://github.com/o/tpl\n_commit: 1.0.0 # pinned\n": {"o/tpl", "https://github.com/o/tpl", "1.0.0", ""},
	} {
		ds, err := New().Extract(".copier-answers.yml", []byte(src), manager.Env{})
		if err != nil || len(ds) != 1 {
			t.Fatalf("%q: %+v, %v", src, ds, err)
		}
		d := ds[0]
		if d.Name != want.name || d.Source != want.source || d.Version != want.version || d.Skip != want.skip {
			t.Errorf("%q: %+v", src, d)
		}
		if d.Skip == "" && src[d.RefSpan.Start:d.RefSpan.End] != d.Version {
			t.Errorf("%q: span %+v", src, d.RefSpan)
		}
	}
	for _, src := range []string{"", "[]\n", "_src_path: gh:o/tpl\n", "_commit: 1.0.0\n", "_src_path: [a]\n_commit: 1.0.0\n", "_src_path: gh:o/t\n_commit: ''\n"} {
		if ds, err := New().Extract(".copier-answers.yml", []byte(src), manager.Env{}); err != nil || len(ds) != 0 {
			t.Errorf("%q: %+v, %v", src, ds, err)
		}
	}
	if _, err := New().Extract(".copier-answers.yml", []byte("_commit: [\n"), manager.Env{}); err == nil {
		t.Error("invalid YAML: no error")
	}
}
