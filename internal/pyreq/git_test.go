package pyreq

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
)

func TestSetGitURL(t *testing.T) {
	for u, w := range map[string]struct{ source, ver, skip string }{
		"git+https://github.com/org/repo.git@v1.2.0":                          {"https://github.com/org/repo", "v1.2.0", ""},
		"git+https://tok@gitlab.com/g/sub/repo@2.0#subdirectory=pkg":          {"https://gitlab.com/g/sub/repo", "2.0", ""},
		"git+http://host/r/@1.0":                                              {"http://host/r", "1.0", ""},
		"git+https://github.com/org/repo@main":                                {"", "", SkipGit},
		"git+https://github.com/org/repo":                                     {"", "", SkipGit},
		"git+https://user@github.com/org/repo":                                {"", "", SkipGit},
		"git+https://github.com/o/r@0123456789abcdef0123456789abcdef01234567": {"", "", SkipGit},
		"git+ssh://git@github.com/org/repo@v1":                                {"", "", SkipGit},
		"git+file:///tmp/repo@v1":                                             {"", "", SkipGit},
		"git+nourl@v1":                                                        {"", "", SkipGit},
		"https://example.com/x.whl":                                           {"", "", SkipURL},
		"hg+https://example.com/x@1.0":                                        {"", "", SkipGit},
	} {
		d := NewDep("m", "t", "f", "n", 1)
		SetGitURL(&d, u, 10, true)
		if d.Source != w.source || d.Version != w.ver || d.Skip != w.skip {
			t.Errorf("%s: source %q version %q skip %q", u, d.Source, d.Version, d.Skip)
		}
		if w.skip == "" && (d.Datasource != GitTags || d.Versioning != "tag" || d.RefSpan.End-d.RefSpan.Start != len(w.ver) || u[d.RefSpan.Start-10:d.RefSpan.End-10] != w.ver) {
			t.Errorf("%s: %+v", u, d)
		}
	}
	d := NewDep("m", "t", "f", "n", 1)
	SetGitURL(&d, "git+https://h/o/r@v1", 0, false)
	if d.Skip != SkipLocate {
		t.Errorf("inexact: %q", d.Skip)
	}
}

func TestGitTable(t *testing.T) {
	doc, err := tomlspan.Parse([]byte("a = { git = \"https://h/o/a\", tag = \"v1\" }\nb = { git = 1 }\nc = { git = \"https://h/o/c\" }\nd = { version = \"1\" }\n"))
	if err != nil {
		t.Fatal(err)
	}
	for k, w := range map[string]struct {
		git       bool
		skip, ver string
	}{"a": {true, "", "v1"}, "b": {true, SkipGit, ""}, "c": {true, SkipGit, ""}, "d": {false, "", ""}} {
		var d dep.Dep
		if got := GitTable(&d, doc.Get(k), "tag"); got != w.git || d.Skip != w.skip || d.Version != w.ver {
			t.Errorf("%s: %v %+v", k, got, d)
		}
	}
}

func TestGitName(t *testing.T) {
	for in, want := range map[string]string{
		"git+https://github.com/o/r.git@v1#egg=name&subdirectory=x": "name",
		"git+https://user@github.com/o/repo.git@v1":                 "repo",
		"git+https://github.com/o/repo":                             "repo",
		"git+https://github.com/o/repo#subdirectory=x":              "repo",
	} {
		if got := GitName(in); got != want {
			t.Errorf("GitName(%q) = %q, want %q", in, got, want)
		}
	}
}
