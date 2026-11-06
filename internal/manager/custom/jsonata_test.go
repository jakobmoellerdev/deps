package custom

import (
	"fmt"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func jsonataEntry(format, query string) config.Custom {
	return config.Custom{Files: []string{"f"}, FileFormat: format, JSONata: query}
}

// Renovate's documented package.json manager (lib/modules/manager/custom/jsonata/readme.md):
// one entry per matchString, as deps translates it. Equal versions in several places get their
// own spans, and $substring cuts the version out of packageManager.
func TestJSONataPackageJSON(t *testing.T) {
	src := `{
  "name": "renovate",
  // comments and trailing commas are JSONC
  "packageManager": "pnpm@10.18.3",
  "dependencies": {
    "@aws-sdk/client-s3": "3.913.0",
    "@aws-sdk/client-ec2": "3.913.0",
    "semver": "7.7.3",
  },
  "devDependencies": {
    "typescript": "5.9.3",
    "semver-stable": "7.7.3"
  }
}
`
	deps := func(q string) []dep.Dep {
		c := jsonataEntry("json", q)
		c.Datasource = "npm"
		return extract(t, one(t, c), src)
	}
	check(t, src, deps(`$each(dependencies, function($v, $k) { {"depName": $k, "currentValue": $v, "depType": "dependencies"} })`), DepTypeJSONata, []want{
		{6, "npm", "@aws-sdk/client-s3", "", "semver", "3.913.0", "", ""},
		{7, "npm", "@aws-sdk/client-ec2", "", "semver", "3.913.0", "", ""},
		{8, "npm", "semver", "", "semver", "7.7.3", "", ""},
	})
	check(t, src, deps(`$each(devDependencies, function($v, $k) { {"depName": $k, "currentValue": $v} })`), DepTypeJSONata, []want{
		{11, "npm", "typescript", "", "semver", "5.9.3", "", ""},
		{12, "npm", "semver-stable", "", "semver", "7.7.3", "", ""},
	})
	check(t, src, deps(`{ "depName": "pnpm", "currentValue": $substring(packageManager, 5) }`), DepTypeJSONata, []want{
		{4, "npm", "pnpm", "", "semver", "10.18.3", "", ""},
	})
}

// Renovate's documented YAML example: a stream is the list of its documents, so a path maps
// over all of them.
func TestJSONataYAMLDocuments(t *testing.T) {
	src := "packages:\n  - version: 1.2.3\n    package: foo\n---\npackages:\n  - version: 1.2.5\n    package: bar\n---\n# empty\n"
	c := jsonataEntry("yaml", `packages.{ "depName": package, "currentValue": version }`)
	c.Datasource = "npm"
	check(t, src, extract(t, one(t, c), src), DepTypeJSONata, []want{
		{2, "npm", "foo", "", "semver", "1.2.3", "", ""},
		{6, "npm", "bar", "", "semver", "1.2.5", "", ""},
	})
	if got := extract(t, one(t, jsonataEntry("yaml", `packages`)), ""); got != nil {
		t.Errorf("empty file: %+v", got)
	}
}

// kubernetes/kubernetes build/dependencies.yaml style: names and versions under one list,
// the datasource chosen by the query, a quoted version and a version in a longer string.
func TestJSONataYAMLComputedFields(t *testing.T) {
	src := `dependencies:
  # etcd
  - name: "etcd"
    version: 3.6.5
    refPaths:
    - path: cluster/gce/manifests/etcd.manifest
  - name: "golang: upstream version"
    version: "1.25.3"
  - name: "registry.k8s.io/pause"
    image: registry.k8s.io/pause:3.10.1
`
	q := `dependencies.(
  name = "etcd" ? {"depName": "etcd-io/etcd", "currentValue": version, "datasource": "github-releases", "extractVersion": "^v(?<version>.+)$"} :
  name = "golang: upstream version" ? {"depName": "go", "currentValue": version, "datasource": "golang-version", "versioning": "go"} :
  image ? {"depName": name, "currentValue": $substringAfter(image, ":"), "datasource": "docker", "versioning": "docker"}
)`
	deps := extract(t, one(t, jsonataEntry("yaml", q)), src)
	check(t, src, deps, DepTypeJSONata, []want{
		{4, "github-releases", "etcd-io/etcd", "https://github.com/etcd-io/etcd", "semver", "3.6.5", "", ""},
		{8, "go-toolchain", "go", "", "go", "1.25.3", "", ""},
		{10, "docker", "registry.k8s.io/pause", "", "docker", "3.10.1", "", ""},
	})
	if deps[0].ExtractVersion != "^v(?<version>.+)$" {
		t.Errorf("extractVersion = %q", deps[0].ExtractVersion)
	}
}

// Renovate's documented TOML example and a mise.toml [tools] table.
func TestJSONataTOML(t *testing.T) {
	src := "[[packages]]\nversion = \"1.2.3\"\npackage = \"foo\"\n\n[[packages]]\nversion = '1.2.2'\npackage = \"bar\"\n"
	c := jsonataEntry("toml", `packages.{ "depName": package, "currentValue": version }`)
	c.Datasource = "npm"
	check(t, src, extract(t, one(t, c), src), DepTypeJSONata, []want{
		{2, "npm", "foo", "", "semver", "1.2.3", "", ""},
		{6, "npm", "bar", "", "semver", "1.2.2", "", ""},
	})

	src = "min_version = \"2024.1.0\"\n[tools]\ngo = \"1.25.3\"\nnode = \"22.21.0\"\n[settings]\njobs = 4\nexperimental = true\nratio = 1.5\nsince = 1979-05-27\n"
	q := `$each(tools, function($v, $k) { {"depName": $k, "currentValue": $v, "datasource": $k = "go" ? "go-toolchain" : "node-version"} })`
	check(t, src, extract(t, one(t, jsonataEntry("toml", q)), src), DepTypeJSONata, []want{
		{3, "go-toolchain", "go", "", "semver", "1.25.3", "", ""},
		{4, "node-version", "node", "", "semver", "22.21.0", "", ""},
	})
	// Scalars keep their JSON types.
	q = `settings.{"depName": $type(jobs) & $type(experimental) & $type(ratio) & $string(ratio) & since, "currentValue": "1.5", "datasource": "npm"}`
	if d := extract(t, one(t, jsonataEntry("toml", q)), src); len(d) != 1 || d[0].Name != "numberboolean"+"number1.51979-05-27" {
		t.Errorf("scalars: %+v", d)
	}
}

// Renovate's documented "name and version in one string" shape: the marker after each
// occurrence survives $split.
func TestJSONataSplit(t *testing.T) {
	src := `{"packages": ["foo@1.2.3", "bar@1.2.3", "baz@11.2.3"]}`
	q := `packages.($split($, "@") ~> function($p) { {"depName": $p[0], "currentValue": $p[1]} })`
	c := jsonataEntry("json", q)
	c.Datasource = "npm"
	check(t, src, extract(t, one(t, c), src), DepTypeJSONata, []want{
		{1, "npm", "foo", "", "semver", "1.2.3", "", ""},
		{1, "npm", "bar", "", "semver", "1.2.3", "", ""},
		{1, "npm", "baz", "", "semver", "11.2.3", "", ""},
	})
	deps := extract(t, one(t, c), src)
	if deps[0].RefSpan.Start != strings.Index(src, "1.2.3") || deps[1].RefSpan.Start != strings.LastIndex(src, "@1.2.3")+1 {
		t.Errorf("spans %+v %+v", deps[0].RefSpan, deps[1].RefSpan)
	}
}

// The query shape deps migrate writes for a literal extractVersionTemplate; a literal
// registryUrlTemplate becomes the entry's registryUrl, which git-tags deps ignore.
func TestJSONataMergedTemplates(t *testing.T) {
	src := "tools:\n  - repo: kubernetes-sigs/kind\n    version: v0.30.0\n"
	q := `(tools.{"depName": repo, "currentValue": version}) ~> $map(function($d) { $merge([$d, {"extractVersion": "^v(?<version>.+)$"}]) })`
	c := jsonataEntry("yaml", q)
	c.Datasource, c.RegistryURL = "github-tags", "https://github.com"
	deps := extract(t, one(t, c), src)
	check(t, src, deps, DepTypeJSONata, []want{
		{3, "git-tags", "kubernetes-sigs/kind", "https://github.com/kubernetes-sigs/kind", "semver", "v0.30.0", "", ""},
	})
	if deps[0].ExtractVersion != "^v(?<version>.+)$" {
		t.Errorf("extractVersion = %q", deps[0].ExtractVersion)
	}
}

// A version glued to letters ("v" prefix the query strips) is found in the second pass.
func TestJSONataGluedVersion(t *testing.T) {
	src := "tools:\n  kind: v0.30.0\n  helm: v3.19.0\n"
	q := `$each(tools, function($v, $k) { {"depName": $k, "currentValue": $substringAfter($v, "v")} })`
	c := jsonataEntry("yaml", q)
	c.Datasource = "npm"
	check(t, src, extract(t, one(t, c), src), DepTypeJSONata, []want{
		{2, "npm", "kind", "", "semver", "0.30.0", "", ""},
		{3, "npm", "helm", "", "semver", "3.19.0", "", ""},
	})
}

// A query that sorts its results by version still maps each dep to its own bytes.
func TestJSONataSortedResults(t *testing.T) {
	src := `{"a": "2.0.0", "b": "1.0.0", "c": "2.0.0"}`
	q := `$sort($each($, function($v, $k) { {"depName": $k, "currentValue": $v} }), function($l, $r) { $l.currentValue > $r.currentValue })`
	c := jsonataEntry("json", q)
	c.Datasource = "npm"
	deps := extract(t, one(t, c), src)
	for _, d := range deps {
		at := strings.Index(src, `"`+d.Name+`"`) + len(d.Name) + 5
		if d.Skip != "" || d.RefSpan.Start != at || src[d.RefSpan.Start:d.RefSpan.End] != d.Version {
			t.Errorf("%s: span %+v (want start %d), skip %q", d.Name, d.RefSpan, at, d.Skip)
		}
	}
}

func TestJSONataDigest(t *testing.T) {
	sum := "sha256:" + strings.Repeat("ab", 32)
	src := `{"images": [{"image": "nginx", "tag": "1.27.3", "digest": "` + sum + `"}, {"image": "redis", "tag": "8.2.2"}]}`
	q := `images.{"depName": image, "currentValue": tag, "currentDigest": digest}`
	c := jsonataEntry("json", q)
	c.Datasource, c.Versioning = "docker", "docker"
	deps := extract(t, one(t, c), src)
	check(t, src, deps, DepTypeJSONata, []want{
		{1, "docker", "nginx", "", "docker", "1.27.3", sum, ""},
		{1, "docker", "redis", "", "docker", "8.2.2", "", ""},
	})
	edits, err := one(t, c).Edits(dep.Update{Dep: deps[0], NewVersion: "1.29.3", NewDigest: "sha256:" + strings.Repeat("cd", 32)})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply([]byte(src), edits)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.NewReplacer("1.27.3", "1.29.3", sum, "sha256:"+strings.Repeat("cd", 32)).Replace(src); string(out) != want {
		t.Errorf("edited %s", out)
	}
}

// helm results give the chart repository as registryUrl.
func TestJSONataHelmRegistry(t *testing.T) {
	src := "charts:\n  - name: cert-manager\n    repo: https://charts.jetstack.io\n    version: v1.19.1\n  - name: bad\n    version: 1.0.0\n"
	q := `charts.{"depName": name, "registryUrl": repo, "currentValue": version, "datasource": "helm"}`
	check(t, src, extract(t, one(t, jsonataEntry("yaml", q)), src), DepTypeJSONata, []want{
		{4, "helm", "cert-manager", "https://charts.jetstack.io", "semver", "v1.19.1", "", ""},
		{6, "helm", "bad", "", "semver", "1.0.0", "", `invalid helm name "bad"`},
	})
}

// A result's registryUrl overrides the entry's; versioning defaults to the datasource's.
func TestJSONataRegistryURL(t *testing.T) {
	const base = "https://packages.microsoft.com/azurelinux/3.0/prod/base/x86_64/repodata/"
	src := "packages:\n  - name: golang\n    version: 1.22.5-1.azl3\n  - name: curl\n    version: 8.8.0-1.azl3\n    repo: https://example.org/rpm/repodata/\n"
	c := jsonataEntry("yaml", `packages.{"depName": name, "currentValue": version, "registryUrl": repo}`)
	c.Datasource, c.RegistryURL = "rpm", base
	check(t, src, extract(t, one(t, c), src), DepTypeJSONata, []want{
		{3, "rpm", "golang", base, "rpm", "1.22.5-1.azl3", "", ""},
		{5, "rpm", "curl", "https://example.org/rpm/repodata/", "rpm", "8.8.0-1.azl3", "", ""},
	})
}

func TestJSONataSkips(t *testing.T) {
	src := `{"deps": [
  {"n": "a", "v": 3},
  {"n": "b"},
  {"v": "1.0.0"},
  {"n": "d", "v": "1.0.0", "ds": ""},
  {"n": "e", "v": "1.0.0", "computed": true},
  {"n": "f", "v": "1.0.0", "ver": "nope"},
  {"n": "g", "v": "1.0.0", "ev": "("},
  {"n": "h", "v": "1.0.0", "ds": "cpan"},
  {"n": "i", "v": "1.0.0", "d": "sha256:00"}
]}`
	q := `deps.{"depName": n, "currentValue": computed ? "v" & v : v, "datasource": ds = "" ? "" : ds ? ds : "npm", "versioning": ver, "extractVersion": ev, "currentDigest": d ? d & "ff"}`
	deps := extract(t, one(t, jsonataEntry("json", q)), src)
	var got []string
	for _, d := range deps {
		got = append(got, d.Skip)
	}
	want := []string{
		"not a string: currentValue", "empty version", "empty name", "empty datasource", "version not found in file",
		`unknown versioning "nope"`, "invalid extractVersion: invalid RE2 regexp: error parsing regexp: missing closing ): `(`",
		`unknown datasource "cpan"`, "digest not found in file",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("skips:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestJSONataErrors(t *testing.T) {
	for _, c := range []struct{ format, query, src, want string }{
		{"json", "a", "{", "parse as json: unexpected end of JSON input"},
		{"yaml", "a", "a: [", "parse as yaml:"},
		{"toml", "a", "a = ", "parse as toml:"},
		{"json", "$error('boom')", "{}", "jsonata: "},
		{"json", "a", `{"a": "x"}`, "jsonata: result is a string, not an object or a list of objects"},
		{"json", "a", `{"a": [{}, 1]}`, "jsonata: result [1] is a number, not an object"},
	} {
		_, err := one(t, jsonataEntry(c.format, c.query)).Extract("f", []byte(c.src), manager.Env{})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %q over %q: %v, want %q", c.format, c.query, c.src, err, c.want)
		}
	}
	// No result is no deps.
	if d := extract(t, one(t, jsonataEntry("json", "missing")), `{}`); d != nil {
		t.Errorf("missing: %+v", d)
	}
}

func TestJSONataNewErrors(t *testing.T) {
	for _, c := range []struct {
		cfg  config.Custom
		want string
	}{
		{config.Custom{Files: []string{"x"}, JSONata: "a", FileFormat: "xml"}, `fileFormat: must be one of json, yaml, toml, got "xml"`},
		{config.Custom{Files: []string{"x"}, JSONata: "a"}, `fileFormat: must be one of`},
		{config.Custom{Files: []string{"x"}, JSONata: "a(", FileFormat: "json"}, `jsonata: `},
		{config.Custom{Files: []string{"x"}, JSONata: "a", FileFormat: "json", Match: "x"}, `match: exclusive with jsonata`},
		{config.Custom{Files: []string{"x"}, JSONata: "a", FileFormat: "json", Checksum: &config.Checksum{URL: "u"}}, `checksum: requires match`},
		{config.Custom{Files: []string{"x"}, FileFormat: "json"}, `fileFormat: requires jsonata`},
	} {
		if _, err := New(&config.Config{Custom: []config.Custom{c.cfg}}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("New(%+v) = %v, want %q", c.cfg, err, c.want)
		}
	}
}

func TestYAMLDocumentScalars(t *testing.T) {
	src := "base: &b {x: 1}\nuse: *b\n? [k]\n: v\nn: ~\nf: 1.5\ninf: .inf\nts: 2001-12-14\nbool: true\n"
	got, err := jsonataDocument("yaml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"base":{"x":1},"use":{"x":1},"":"v","n":null,"f":1.5,"inf":".inf","ts":"2001-12-14","bool":true}]`; string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if _, err := jsonataDocument("xml", nil); err == nil {
		t.Error("xml: no error")
	}
}

// BenchmarkJSONataLockfile extracts a package-lock-like file of 2000 packages.
func BenchmarkJSONataLockfile(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("{\n  \"name\": \"app\",\n  \"lockfileVersion\": 3,\n  \"packages\": {\n")
	for i := range 2000 {
		if i > 0 {
			sb.WriteString(",\n")
		}
		fmt.Fprintf(&sb, "    \"node_modules/pkg-%d\": {\n      \"version\": \"%d.%d.%d\",\n      \"resolved\": \"https://registry.npmjs.org/pkg-%d/-/pkg-%d-%d.%d.%d.tgz\",\n      \"integrity\": \"sha512-%040d\"\n    }",
			i, i%7, i%13, i, i, i, i%7, i%13, i, i)
	}
	sb.WriteString("\n  }\n}\n")
	src := []byte(sb.String())
	c := jsonataEntry("json", `$each(packages, function($v, $k) { {"depName": $substringAfter($k, "node_modules/"), "currentValue": $v.version} })`)
	c.Datasource = "npm"
	m, err := New(&config.Config{Custom: []config.Custom{c}})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		deps, err := m[0].Extract("package-lock.json", src, manager.Env{})
		if err != nil || len(deps) != 2000 || deps[1999].RefSpan.End == 0 {
			b.Fatal(err, len(deps))
		}
	}
}

func TestOccurrences(t *testing.T) {
	want := map[string]bool{"1.2": true, "1.2.3": true, "2.3": true, "v1": true}
	for _, c := range []struct {
		src   string
		glued bool
		want  string
	}{
		// Longest at one offset wins; occurrences do not overlap.
		{`"1.2.3" 1.2.3`, false, "1:6=1.2.3 8:13=1.2.3"},
		// Unless glued, an alphanumeric neighbor rejects the longer value but not a shorter one.
		{`1.2.3a 1.2.x`, false, "0:3=1.2 7:10=1.2"},
		{`x1.2.3 v1`, false, "3:6=2.3 7:9=v1"},
		{`x1.2.3 v1`, true, "1:6=1.2.3 7:9=v1"},
		// A rejected occurrence leaves its bytes free for a later start.
		{`a1.2.3 `, false, "3:6=2.3"},
		{`1.2.3`, true, "0:5=1.2.3"},
		{``, false, ""},
	} {
		var got []string
		for _, o := range occurrences([]byte(c.src), want, c.glued) {
			got = append(got, fmt.Sprintf("%d:%d=%s", o.start, o.end, o.value))
		}
		if g := strings.Join(got, " "); g != c.want {
			t.Errorf("occurrences(%q, glued=%v) = %q, want %q", c.src, c.glued, g, c.want)
		}
	}
}
