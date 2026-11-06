package npm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// specText renders a dep for the table tests: datasource, source, version (with the text its
// RefSpan covers when it differs), digest, range and skip.
func specText(b []byte, d dep.Dep) string {
	s := d.Datasource + " " + d.Name
	if d.Source != "" {
		s += " " + d.Source
	}
	if d.Skip != "" {
		return s + " skip=" + d.Skip
	}
	s += " " + d.Versioning + " " + d.Version
	if d.RefSpan != (dep.Span{}) {
		if t := string(b[d.RefSpan.Start:d.RefSpan.End]); t != d.Version {
			s += " span=" + t
		}
	}
	if d.Digest != "" {
		s += " digest=" + string(b[d.DigestSpan.Start:d.DigestSpan.End])
	}
	if d.Range != "" {
		s += " range=" + d.Range
	}
	return s
}

func extractOne(t *testing.T, field, value string) (dep.Dep, []byte) {
	t.Helper()
	b := []byte(`{"` + field + `": {"key": "` + value + `"}}`)
	deps, err := New().Extract("package.json", b, manager.Env{})
	if err != nil || len(deps) != 1 {
		t.Fatalf("%s: %v, %+v", value, err, deps)
	}
	return deps[0], b
}

// Values from argo-cd (argo-ui), the npm docs' git URL forms and Renovate's npm extract tests.
func TestGitDependencies(t *testing.T) {
	const sha = "28927a57027a361505ff91d0498a4bc9670bd882"
	for _, c := range []struct{ value, want string }{
		{"git+https://github.com/argoproj/argo-ui.git#" + sha, "git-refs key https://github.com/argoproj/argo-ui tag HEAD digest=" + sha},
		{"git+https://github.com/argoproj/argo-ui.git#28927a5", "git-refs key https://github.com/argoproj/argo-ui tag HEAD digest=28927a5"},
		{"github:owner/repo#v1.2.3", "git-tags key https://github.com/owner/repo semver v1.2.3"},
		{"owner/repo#1.2.3-beta.1", "git-tags key https://github.com/owner/repo semver 1.2.3-beta.1"},
		{"owner/repo#v1", "git-tags key https://github.com/owner/repo tag v1"},
		{"owner/repo#1234567", "git-tags key https://github.com/owner/repo tag 1234567"},
		{"gitlab:group/sub/project#v2.0.0", "git-tags key https://gitlab.com/group/sub/project semver v2.0.0"},
		{"bitbucket:team/repo.git#v3.0.0", "git-tags key https://bitbucket.org/team/repo semver v3.0.0"},
		{"git+ssh://git@github.com:npm/cli.git#v1.0.27", "git-tags key https://github.com/npm/cli semver v1.0.27"},
		{"git+ssh://git@github.com/npm/cli.git#v1.0.27", "git-tags key https://github.com/npm/cli semver v1.0.27"},
		{"git+ssh://git@git.example.com:2222/team/repo.git#v1.0.0", "git-tags key https://git.example.com/team/repo semver v1.0.0"},
		{"git://github.com/npm/cli.git#v1.0.27", "git-tags key https://github.com/npm/cli semver v1.0.27"},
		{"git@github.com:npm/cli.git#v1.0.27", "git-tags key https://github.com/npm/cli semver v1.0.27"},
		{"https://github.com/npm/cli#v1.0.27", "git-tags key https://github.com/npm/cli semver v1.0.27"},
		{"https://git.example.com/team/repo.git#v1.0.0", "git-tags key https://git.example.com/team/repo semver v1.0.0"},
		{"github:owner/repo#semver:^5.0.0", "git-tags key https://github.com/owner/repo semver 5.0.0 span=^5.0.0 range=^5.0.0"},
		{"github:owner/repo#semver:1.x", "git-tags key https://github.com/owner/repo semver 1.0.0 span=1.x range=1.x"},
		{"github:owner/repo#semver:latest", "git-tags key https://github.com/owner/repo skip=" + skipGitUnversion},
		{"github:owner/repo#main", "git-tags key https://github.com/owner/repo skip=" + skipGitUnversion},
		{"github:owner/repo", "git-tags key https://github.com/owner/repo skip=" + skipGitNoRef},
		{"github:owner/repo#", "git-tags key https://github.com/owner/repo skip=" + skipGitNoRef},
		{"git+https://github.com/apache/airflow.git", "git-tags key https://github.com/apache/airflow skip=" + skipGitNoRef},
		{"github:repo#v1.0.0", "npm key skip=" + skipGit},
		{"gist:11081aaa281#v1", "npm key skip=" + skipGit},
		{"git+file:///srv/repo.git#v1.0.0", "npm key skip=" + skipGit},
		{"https://github.com/owner/repo/archive/v1.0.0.tar.gz", "npm key skip=url dependency"},
		{"https://example.com/x.tgz", "npm key skip=url dependency"},
		{"../lib", "npm key skip=local dependency"},
		{"file:../lib#v1", "npm key skip=local dependency"},
	} {
		d, b := extractOne(t, "dependencies", c.value)
		if got := specText(b, d); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.value, got, c.want)
		}
		if d.Skip == "" && d.Renamable() != true {
			t.Errorf("%s: key not renamable", c.value)
		}
	}
}

// Aliases from backstage and grafana.
func TestAliases(t *testing.T) {
	for _, c := range []struct{ value, want string }{
		{"npm:react-router@^6.3.0", "npm react-router semver 6.3.0 span=^6.3.0 range=^6.3.0"},
		{"npm:react-router-dom@6.0.0-beta.0", "npm react-router-dom semver 6.0.0-beta.0 range=6.0.0-beta.0"},
		{"npm:@typescript/typescript6@^6.0.2", "npm @typescript/typescript6 semver 6.0.2 span=^6.0.2 range=^6.0.2"},
		{"npm:@siedlerchr/types-ol-ext@3.6.8", "npm @siedlerchr/types-ol-ext semver 3.6.8 range=3.6.8"},
		{"npm:^1.2.3", "npm key semver 1.2.3 span=^1.2.3 range=^1.2.3"},
		{"npm:other@latest", "npm key skip=tag"},
		{"npm:@scope/name", "npm key skip=unsupported range"},
		{"npm:Bad Name@1.0.0", "npm key skip=" + skipAlias},
	} {
		d, b := extractOne(t, "dependencies", c.value)
		if got := specText(b, d); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.value, got, c.want)
		}
	}
	// A replacement rewrites the aliased package and keeps the alias; the range moves.
	d, b := extractOne(t, "dependencies", "npm:react-router@^6.3.0")
	if !isAlias(d) {
		t.Fatal("not an alias")
	}
	edits, err := New().Edits(dep.Update{Dep: d, NewVersion: "7.1.0", NewName: "react-router-dom", RangeStrategy: "bump"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply(b, edits)
	if err != nil || string(out) != `{"dependencies": {"key": "npm:react-router-dom@^7.1.0"}}` {
		t.Errorf("patched = %s, %v", out, err)
	}
}

// Overrides from airflow (npm, pnpm) and backstage (yarn berry resolutions).
func TestOverrides(t *testing.T) {
	src := `{
  "overrides": {
    "tar@<7.5.11": ">=7.5.11",
    "foo": {".": "1.0.0", "bar": "2.0.0", "baz": {"qux": "^3.0.0"}},
    "react": "$react",
    "@scope/pkg": "npm:@scope/fork@1.2.3",
    "git": "github:owner/repo#v1.0.0",
    "number": 1
  },
  "resolutions": {
    "@inquirer/type@npm:^4.0.5": "4.0.7",
    "@material-ui/pickers@^3.2.10": "patch:@material-ui/pickers@npm%3A3.3.11#./.yarn/patches/x.patch",
    "**/@types/react": "^18.0.0",
    "parent/child": "1.0.0",
    "@s/parent/@t/child": "2.0.0"
  },
  "pnpm": {
    "minimumReleaseAge": 5760,
    "onlyBuiltDependencies": ["esbuild"],
    "overrides": {
      "minimatch@>=10.0.0 <10.2.3": ">=10.2.3",
      "parent@1>child": "1.2.3",
      "plain": "1.0.0",
      "removed": "-"
    }
  }
}`
	b := []byte(src)
	deps, err := New().Extract("package.json", b, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range deps {
		got = append(got, fmt.Sprintf("%d %s %s renamable=%v", d.Line, d.DepType, specText(b, d), d.Renamable()))
	}
	want := []string{
		"3 overrides npm tar semver 7.5.11 span=>=7.5.11 range=>=7.5.11 renamable=false",
		"4 overrides npm foo semver 1.0.0 range=1.0.0 renamable=false",
		"4 overrides npm bar semver 2.0.0 range=2.0.0 renamable=true",
		"4 overrides npm qux semver 3.0.0 span=^3.0.0 range=^3.0.0 renamable=true",
		"5 overrides npm react skip=override reference renamable=true",
		"6 overrides npm @scope/fork semver 1.2.3 range=1.2.3 renamable=true",
		"7 overrides git-tags git https://github.com/owner/repo semver v1.0.0 renamable=true",
		"11 resolutions npm @inquirer/type semver 4.0.7 range=4.0.7 renamable=false",
		"12 resolutions npm @material-ui/pickers skip=unsupported range renamable=false",
		"13 resolutions npm @types/react semver 18.0.0 span=^18.0.0 range=^18.0.0 renamable=false",
		"14 resolutions npm child semver 1.0.0 range=1.0.0 renamable=false",
		"15 resolutions npm @t/child semver 2.0.0 range=2.0.0 renamable=false",
		"21 pnpm.overrides npm minimatch semver 10.2.3 span=>=10.2.3 range=>=10.2.3 renamable=false",
		"22 pnpm.overrides npm child semver 1.2.3 range=1.2.3 renamable=false",
		"23 pnpm.overrides npm plain semver 1.0.0 range=1.0.0 renamable=true",
		"24 pnpm.overrides npm removed skip=unsupported range renamable=true",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("deps =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// An exact override moves to the new version; the nested `.` keeps its key.
	edits, err := New().Edits(dep.Update{Dep: deps[1], NewVersion: "1.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply(b, edits)
	if err != nil || !strings.Contains(string(out), `"foo": {".": "1.1.0", "bar"`) {
		t.Errorf("patched = %s, %v", out, err)
	}
	for _, bad := range []string{`{"overrides": {"a": {"b": `, `{"resolutions": {"a": `, `{"pnpm": {"overrides": {"a": `, `{"pnpm": {"x": `, `{"overrides": {"a": {`} {
		if _, err := New().Extract("package.json", []byte(bad), manager.Env{}); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
	for _, empty := range []string{`{"overrides": []}`, `{"pnpm": "x"}`, `{"pnpm": {"overrides": null}}`} {
		if deps, err := New().Extract("package.json", []byte(empty), manager.Env{}); err != nil || len(deps) != 0 {
			t.Errorf("%s: %+v, %v", empty, deps, err)
		}
	}
}

// The packageManager Corepack hash of openclaw (pnpm 12.15.0) is its tarball's sha512.
func TestPackageManagerHash(t *testing.T) {
	const h = "b3951d6d6ff4a9f13dc7cf56cd28d2b8eb37dbcc9c68e52e1e1191873ea9f82a017287ab50baadc5ccc494f036c67c0e47eb13608c2fa36f47711b61ea128e61"
	for _, c := range []struct{ value, version, checksum, artifact, skip string }{
		{"pnpm@12.15.0+sha512." + h, "12.15.0", h, "https://registry.npmjs.org/pnpm/-/pnpm-{{version}}.tgz", ""},
		{"yarn@4.5.0+sha224." + h[:56], "4.5.0", h[:56], "https://repo.yarnpkg.com/{{version}}/packages/yarnpkg-cli/bin/yarn.js", ""},
		{"yarn@1.22.22+sha256." + h[:64], "1.22.22", h[:64], "https://registry.npmjs.org/yarn/-/yarn-{{version}}.tgz", ""},
		{"npm@12.2.0+sha1." + h[:40], "", "", "", "packageManager with hash"},
		{"npm@12.2.0+sha512." + h[:64], "", "", "", "packageManager with hash"},
		{"bun@1.2.3+sha512." + h, "", "", "", "packageManager with hash"},
		{"pnpm@12+sha512." + h, "", "", "", "unsupported range"},
	} {
		b := []byte(`{"packageManager": "` + c.value + `"}`)
		deps, err := New().Extract("package.json", b, manager.Env{})
		if err != nil || len(deps) != 1 {
			t.Fatalf("%s: %v %+v", c.value, err, deps)
		}
		d := deps[0]
		if d.Skip != c.skip {
			t.Errorf("%s: skip %q, want %q", c.value, d.Skip, c.skip)
			continue
		}
		if c.skip != "" {
			continue
		}
		if d.Version != c.version || d.Checksum != c.checksum || d.ChecksumSource == nil || d.ChecksumSource.Artifact != c.artifact ||
			string(b[d.ChecksumSpan.Start:d.ChecksumSpan.End]) != c.checksum || string(b[d.RefSpan.Start:d.RefSpan.End]) != c.version {
			t.Errorf("%s: %+v", c.value, d)
		}
		nh := strings.Repeat("c", len(c.checksum))
		edits, err := New().Edits(dep.Update{Dep: d, NewVersion: "12.16.0", NewChecksum: nh})
		if err != nil {
			t.Fatal(err)
		}
		out, _ := patch.Apply(b, edits)
		if want := strings.NewReplacer(c.version, "12.16.0", c.checksum, nh).Replace(string(b)); string(out) != want {
			t.Errorf("patched = %s, want %s", out, want)
		}
		if _, err := New().Edits(dep.Update{Dep: d, NewVersion: "12.16.0"}); err == nil {
			t.Errorf("%s: version without hash accepted", c.value)
		}
	}
}

func TestGitEdits(t *testing.T) {
	d, b := extractOne(t, "dependencies", "git+https://github.com/argoproj/argo-ui.git#28927a5")
	edits, err := New().Edits(dep.Update{Dep: d, Type: dep.Digest, NewVersion: d.Version, NewDigest: "1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := patch.Apply(b, edits); !strings.Contains(string(out), "argo-ui.git#1111111\"") {
		t.Errorf("patched = %s", out)
	}
	if edits, err := New().Edits(dep.Update{Dep: d, NewVersion: d.Version, NewDigest: "28927A5ffff"}); err != nil || len(edits) != 0 {
		t.Errorf("same commit: %+v, %v", edits, err)
	}
	tag, b := extractOne(t, "dependencies", "github:owner/repo#v1.2.3")
	edits, err = New().Edits(dep.Update{Dep: tag, NewVersion: "v1.3.0"})
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := patch.Apply(b, edits); string(out) != `{"dependencies": {"key": "github:owner/repo#v1.3.0"}}` {
		t.Errorf("patched = %s", out)
	}
	if _, err := New().Edits(dep.Update{Dep: tag, NewVersion: "v1.2.3", NewDigest: "abc"}); err == nil {
		t.Error("pinning a tag dep succeeded")
	}
	rng, b := extractOne(t, "devDependencies", "github:owner/repo#semver:^5.0.0")
	edits, err = New().Edits(dep.Update{Dep: rng, NewVersion: "6.1.0", RangeStrategy: "replace"})
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := patch.Apply(b, edits); string(out) != `{"devDependencies": {"key": "github:owner/repo#semver:^6.1.0"}}` {
		t.Errorf("patched = %s", out)
	}
}

// Aliases, git dependencies and overrides are not looked up in the lockfile.
func TestReadLocksSkipsUnkeyed(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"package.json": `{"dependencies": {"a": "^1.0.0", "alias": "npm:b@^1.0.0", "g": "github:o/r#semver:^1.0.0"}, "overrides": {"a": "^1.0.0"}}`,
		"package-lock.json": `{"lockfileVersion": 3, "packages": {"node_modules/a": {"version": "1.4.0"}, "node_modules/alias": {"name": "b", "version": "1.2.0"},` +
			` "node_modules/b": {"version": "1.3.0"}, "node_modules/g": {"version": "1.1.0"}}}`,
	}
	for p, c := range files {
		if err := os.WriteFile(filepath.Join(root, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	deps, err := New().Extract("package.json", []byte(files["package.json"]), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if errs := New().(manager.LockReader).ReadLocks(root, [][]dep.Dep{deps}); errs != nil {
		t.Fatal(errs)
	}
	var got []string
	for _, d := range deps {
		got = append(got, d.DepType+" "+d.Name+"="+d.Locked)
	}
	if want := "dependencies a=1.4.0|dependencies b=|dependencies g=|overrides a="; strings.Join(got, "|") != want {
		t.Errorf("locked = %s, want %s", strings.Join(got, "|"), want)
	}
}

func TestYarnBerry(t *testing.T) {
	for v, want := range map[string]bool{"4.5.0": true, "2.0.0": true, "^4": true, ">=3.1": true, "v4.0.0": true, "1.22.22": false, "^1": false, "": false, "x": false} {
		if YarnBerry(v) != want {
			t.Errorf("YarnBerry(%q) = %v", v, !want)
		}
	}
}

func TestSelectorNames(t *testing.T) {
	for key, want := range map[string]string{"a": "a", "a@1": "a", "@s/a@^1": "@s/a", "@s/a": "a", "": ""} {
		if key == "@s/a" {
			want = "@s/a"
		}
		if got := specName(key); got != want {
			t.Errorf("specName(%q) = %q, want %q", key, got, want)
		}
	}
	for key, want := range map[string]string{"**/a": "a", "p/@s/c@npm:^1": "@s/c", "@s/p/c": "c", "a": "a"} {
		if got := resolutionName(key); got != want {
			t.Errorf("resolutionName(%q) = %q, want %q", key, got, want)
		}
	}
	for key, want := range map[string]string{"a>b@1": "b", "@s/p@1>@s/c": "@s/c", "a": "a", "a@>=1 <2": "a", "p@>=1>c@<2": "c", "p@1 || >2>c": "c"} {
		if got := pnpmName(key); got != want {
			t.Errorf("pnpmName(%q) = %q, want %q", key, got, want)
		}
	}
}
