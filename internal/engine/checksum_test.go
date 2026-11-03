package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

type fakeReleases []string

func (f fakeReleases) Releases(context.Context, dep.Dep, string) ([]Release, error) {
	rels := make([]Release, len(f))
	for i, v := range f {
		rels[i] = Release{Version: v}
	}
	return rels, nil
}

func (fakeReleases) Digest(context.Context, dep.Dep, string) (string, error) { return "", nil }

// fakeChecksums returns sums[version] (missing = error) and records the asked versions.
type fakeChecksums struct {
	sums map[string]string
	mu   sync.Mutex
	got  []string
}

func (f *fakeChecksums) Checksum(_ context.Context, d dep.Dep, v string) (string, error) {
	f.mu.Lock()
	f.got = append(f.got, d.Name+"@"+v)
	f.mu.Unlock()
	if s, ok := f.sums[v]; ok {
		return s, nil
	}
	return "", errors.New("no checksum for x in y")
}

func TestResolveChecksum(t *testing.T) {
	h1, h2 := strings.Repeat("a", 64), strings.Repeat("b", 64)
	const cfgYAML = `custom:
  - files: [Dockerfile]
    match: 'ARG (?P<name>[\w/]+)_VERSION=(?P<version>\S+)\nARG \w+_SHA256=(?P<checksum>[0-9a-fA-F]+)'
    datasource: github-releases
    checksum: {url: 'https://x/{{version}}/sums'}
rules: [{names: [o/fail], allow: '< 2'}]
`
	src := "ARG cli/cli_VERSION=2.60.0\nARG X_SHA256=" + h1 + "\n" +
		"ARG o/fail_VERSION=1.0.0\nARG X_SHA256=" + h1 + "\n" +
		"ARG o/up_VERSION=2.62.0\nARG X_SHA256=" + h1 + "\n" +
		"ARG o/upper_VERSION=2.60.0\nARG X_SHA256=" + strings.ToUpper(h1) + "\n"
	for _, c := range []struct {
		name    string
		sums    map[string]string
		nilSrc  bool
		updates []string // name@checksum[:1]
		errs    []string
	}{
		{name: "fetched for chosen version", sums: map[string]string{"2.62.0": h2, "1.1.0": h2},
			updates: []string{"cli/cli 2.62.0 " + h2, "o/fail 1.1.0 " + h2, "o/upper 2.62.0 " + strings.ToUpper(h2)}},
		{name: "failure isolated", sums: map[string]string{"2.62.0": h2},
			updates: []string{"cli/cli 2.62.0 " + h2, "o/upper 2.62.0 " + strings.ToUpper(h2)},
			errs:    []string{"Dockerfile:3: o/fail: checksum of 1.1.0: no checksum for x in y"}},
		{name: "length mismatch", sums: map[string]string{"2.62.0": h2 + h2, "1.1.0": h2},
			updates: []string{"o/fail 1.1.0 " + h2},
			errs: []string{"Dockerfile:1: cli/cli: checksum of 2.62.0: checksum length mismatch: have 64 hex digits, got 128",
				"Dockerfile:7: o/upper: checksum of 2.62.0: checksum length mismatch: have 64 hex digits, got 128"}},
		{name: "no source", nilSrc: true,
			errs: []string{"Dockerfile:1: cli/cli: no checksum source", "Dockerfile:3: o/fail: no checksum source", "Dockerfile:7: o/upper: no checksum source"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, cfg := setup(t, cfgYAML, map[string]string{"Dockerfile": src})
			ms, err := Managers(cfg)
			if err != nil {
				t.Fatal(err)
			}
			fc := &fakeChecksums{sums: c.sums}
			o := Options{Managers: ms, Datasources: map[string]Datasource{"github-releases": fakeReleases{"2.62.0", "1.1.0", "1.0.0", "2.60.0"}}, Checksums: fc}
			if c.nilSrc {
				o.Checksums = nil
			}
			var traced []string
			o.Trace = func(tr Trace) {
				for _, tu := range tr.Updates {
					if tu.Update.NewChecksum != "" {
						traced = append(traced, tu.Update.NewChecksum)
					}
				}
			}
			res, err := Resolve(t.Context(), root, cfg, env, o)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, u := range res.Updates {
				got = append(got, u.Dep.Name+" "+u.NewVersion+" "+u.NewChecksum)
			}
			if strings.Join(got, "|") != strings.Join(c.updates, "|") || len(traced) != len(c.updates) {
				t.Errorf("updates = %q (traced %d), want %q", got, len(traced), c.updates)
			}
			var errs []string
			for _, e := range res.Errors {
				errs = append(errs, e.Error())
			}
			if strings.Join(errs, "|") != strings.Join(c.errs, "|") {
				t.Errorf("errors = %q, want %q", errs, c.errs)
			}
			if c.nilSrc {
				return
			}
			for _, v := range fc.got {
				if v == "o/up@2.62.0" {
					t.Error("checksum fetched for a dep without update")
				}
			}
			changes, errs2 := Build(t.Context(), root, ms, res.Updates, nil)
			if len(errs2) != 0 || len(changes) != 1 {
				t.Fatalf("Build = %v, %v", changes, errs2)
			}
			if c.name == "fetched for chosen version" {
				want := "ARG cli/cli_VERSION=2.62.0\nARG X_SHA256=" + h2 + "\n" +
					"ARG o/fail_VERSION=1.1.0\nARG X_SHA256=" + h2 + "\n" +
					"ARG o/up_VERSION=2.62.0\nARG X_SHA256=" + h1 + "\n" +
					"ARG o/upper_VERSION=2.62.0\nARG X_SHA256=" + strings.ToUpper(h2) + "\n"
				if string(changes[0].New) != want {
					t.Errorf("Build:\n%s\nwant:\n%s", changes[0].New, want)
				}
			}
		})
	}
}
