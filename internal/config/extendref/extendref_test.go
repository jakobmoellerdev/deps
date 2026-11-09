package extendref

import (
	"strings"
	"testing"
)

func TestParseExtend(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want Extend
		err  string
	}{
		{in: "org/deps-config", want: Extend{Repo: "org/deps-config", Path: ".deps.yaml"}},
		{in: "org/cfg//go.yaml#v1", want: Extend{Repo: "org/cfg", Path: "go.yaml", Ref: "v1"}},
		{in: "group/sub/cfg//presets/base.yaml#release/1.x", want: Extend{Repo: "group/sub/cfg", Path: "presets/base.yaml", Ref: "release/1.x"}},
		{in: "https://github.com/org/cfg.git//a.yaml#main", want: Extend{Repo: "https://github.com/org/cfg.git", Path: "a.yaml", Ref: "main"}},
		{in: "file:///srv/git/cfg.git", want: Extend{Repo: "file:///srv/git/cfg.git", Path: ".deps.yaml"}},
		{in: "git@github.com:org/cfg.git//x.yaml", want: Extend{Repo: "git@github.com:org/cfg.git", Path: "x.yaml"}},
		{in: "ssh://git@host:2222/org/cfg.git", want: Extend{Repo: "ssh://git@host:2222/org/cfg.git", Path: ".deps.yaml"}},
		{in: "cfg", err: "must be owner/name"},
		{in: "org/cfg#", err: "invalid ref"},
		{in: "org/cfg#-upload-pack=x", err: "invalid ref"},
		{in: "org/cfg#a..b", err: "invalid ref"},
		{in: "org/cfg//", err: "invalid path"},
		{in: "org/cfg//../x.yaml", err: "invalid path"},
		{in: "org/cfg///etc/passwd", err: "invalid path"},
		{in: "org/cfg//a/./b.yaml", err: "invalid path"},
		{in: "ftp://host/org/cfg", err: "scheme must be"},
		{in: "https:///org/cfg", err: "no host"},
		{in: "https://host", err: "no path"},
		{in: "https://u:secret@host/org/cfg", err: "must not contain a password"},
		{in: "-oops/x", err: "must be owner/name"},
	} {
		got, err := Parse(tt.in)
		if tt.err != "" {
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Errorf("Parse(%q) = %+v, %v; want error containing %q", tt.in, got, err, tt.err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", tt.in, got, err, tt.want)
		}
	}
	if s := (Extend{Repo: "org/cfg", Path: ".deps.yaml", Ref: "v1"}).String(); s != "org/cfg//.deps.yaml#v1" {
		t.Errorf("String = %q", s)
	}
}
