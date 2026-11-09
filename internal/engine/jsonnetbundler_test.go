package engine

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/jsonnetbundler"
)

// jsonnet-bundler branches get digest updates that only move jsonnetfile.lock.json, version
// tags tag updates and commits digest updates that rewrite jsonnetfile.json; the
// post-processor gets every update of the file.
func TestJsonnetBundlerDeps(t *testing.T) {
	const (
		libs, pinned = "https://github.com/grafana/jsonnet-libs", "https://github.com/acme/pinned"
		old          = "0000000000000000000000000000000000000000"
		head         = "1111111111111111111111111111111111111111"
	)
	jf := `{
  "version": 1,
  "dependencies": [
    {"source": {"git": {"remote": "https://github.com/grafana/jsonnet-libs.git", "subdir": "mixin-utils"}}, "version": "master"},
    {"source": {"git": {"remote": "https://github.com/acme/mixin", "subdir": "mixin"}}, "version": "v1.0.0"},
    {"source": {"git": {"remote": "https://github.com/acme/pinned"}}, "version": "` + old + `"}
  ]
}
`
	lock := `{"version": 1, "dependencies": [{"source": {"git": {"remote": "https://github.com/grafana/jsonnet-libs.git", "subdir": "mixin-utils"}}, "version": "` + old + `", "sum": "x"}]}`
	ms := []manager.Manager{jsonnetbundler.New()}
	opts := Options{Managers: ms, Datasources: map[string]Datasource{
		"git-refs": fakeBranches{libs + "#master": head, pinned + "#HEAD": head},
		"git-tags": fakeTags{"https://github.com/acme/mixin": {"v1.0.0", "v1.1.0"}},
	}}
	root, cfg := setup(t, "", map[string]string{"jsonnetfile.json": jf, "jsonnetfile.lock.json": lock})
	res, err := Resolve(t.Context(), root, cfg, env, opts)
	if err != nil || len(res.Errors) > 0 || len(res.Skipped) > 0 {
		t.Fatalf("err = %v, errors = %v, skipped = %+v", err, res.Errors, res.Skipped)
	}
	type up struct {
		name, version, digest string
		kind                  dep.UpdateType
	}
	var got []up
	for _, u := range res.Updates {
		got = append(got, up{u.Dep.Name, u.NewVersion, u.NewDigest, u.Type})
	}
	want := []up{
		{"github.com/grafana/jsonnet-libs/mixin-utils", "master", head, dep.Digest},
		{"github.com/acme/mixin/mixin", "v1.1.0", "", dep.Minor},
		{"github.com/acme/pinned", "HEAD", head, dep.Digest},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("updates = %+v, want %+v", got, want)
	}

	var names []string
	post := map[string]PostProcessor{jsonnetbundler.Name: pinPost{
		postFunc: postFunc{siblings: []string{"jsonnetfile.lock.json"}},
		pinned: func(ups, locks map[string][]dep.Update) (map[string][]byte, error) {
			for _, u := range ups["jsonnetfile.json"] {
				names = append(names, u.Dep.Name)
			}
			return map[string][]byte{"jsonnetfile.lock.json": []byte("new lock\n")}, nil
		},
	}}
	changes, errs := Build(t.Context(), root, ms, res.Updates, post)
	wantJF := strings.NewReplacer(`"v1.0.0"`, `"v1.1.0"`, old, head).Replace(jf)
	if len(errs) > 0 || len(changes) != 2 || changes[0].Path != "jsonnetfile.json" || string(changes[0].New) != wantJF ||
		changes[1].Path != "jsonnetfile.lock.json" || string(changes[1].New) != "new lock\n" {
		t.Fatalf("changes = %+v, %v", changes, errs)
	}
	if !slices.Equal(names, []string{"github.com/grafana/jsonnet-libs/mixin-utils", "github.com/acme/mixin/mixin", "github.com/acme/pinned"}) {
		t.Errorf("post-processed deps = %v", names)
	}
	// plan (no post-processors): the branch's update has no diff.
	if changes, errs := Build(t.Context(), root, ms, res.Updates[:1], nil); len(changes) != 0 || len(errs) != 0 {
		t.Errorf("plan: changes = %+v, %v", changes, errs)
	}
}
