package composer

import (
	"os"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// testdata/bookstack/composer.json is BookStackApp/BookStack's (branch development, fetched
// 2026-11-05), its composer.lock that lock's entries of the packages composer.json requires
// verbatim; testdata/symfony/composer.json is symfony/symfony's (branch 7.3) and
// testdata/drupal/composer.json drupal/recommended-project's (branch 11.x);
// testdata/news/composer.json is the TYPO3 extension georgringer/news's (branch main, fetched
// 2026-11-09).

func read(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func extract(t *testing.T, file string, src []byte) []dep.Dep {
	t.Helper()
	deps, err := New().Extract(file, src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deps {
		if d.Skip == "" && string(src[d.RefSpan.Start:d.RefSpan.End]) != d.Range {
			t.Errorf("%s: RefSpan %q, Range %q", d.Name, src[d.RefSpan.Start:d.RefSpan.End], d.Range)
		}
	}
	return deps
}

func find(deps []dep.Dep, name string) dep.Dep {
	for _, d := range deps {
		if d.Name == name {
			return d
		}
	}
	return dep.Dep{}
}

func TestMatch(t *testing.T) {
	m := New()
	for p, want := range map[string]bool{
		"composer.json": true, "app/composer.json": true, "composer.lock": false, "mycomposer.json": false, "composer.json5": false,
	} {
		if m.Match(p) != want {
			t.Errorf("Match(%s) != %v", p, want)
		}
	}
	if m.Name() != Name {
		t.Error("name")
	}
}

func TestExtractBookStack(t *testing.T) {
	src := read(t, "testdata/bookstack/composer.json")
	deps := extract(t, "composer.json", src)
	if len(deps) != 40 {
		t.Fatalf("deps = %d", len(deps))
	}
	var platform, require, dev int
	for _, d := range deps {
		switch {
		case d.Skip == skipPlatform:
			platform++
		case d.Skip != "":
			t.Errorf("%s skipped: %s", d.Name, d.Skip)
		case d.DepType == DepTypeRequire:
			require++
		case d.DepType == DepTypeRequireDev:
			dev++
		}
	}
	if platform != 8 || require != 24 || dev != 8 {
		t.Errorf("platform %d, require %d, dev %d", platform, require, dev)
	}
	if d := find(deps, "laravel/framework"); d.Version != "v13.35.0" || d.Range != "^v13.35.0" || d.Line != 25 ||
		d.Datasource != Datasource || d.Versioning != Versioning || d.Source != "" || d.Manager != Name {
		t.Errorf("laravel/framework = %+v", d)
	}
	if d := find(deps, "phpunit/phpunit"); d.DepType != DepTypeRequireDev || d.Version != "12.5.38" || d.Line != 50 {
		t.Errorf("phpunit = %+v", d)
	}
	if d := find(deps, "php"); d.Skip != skipPlatform || d.Range != "" {
		t.Errorf("php = %+v", d)
	}
}

func TestExtractSymfony(t *testing.T) {
	deps := extract(t, "composer.json", read(t, "testdata/symfony/composer.json"))
	for name, want := range map[string][2]string{
		"psr/cache":               {"^2.0|^3.0", "3.0"},
		"psr/log":                 {"^1|^2|^3", "3"},
		"doctrine/event-manager":  {"^2", "2"},
		"symfony/polyfill-ctype":  {"~1.8", "1.8"},
		"egulias/email-validator": {"^2.1.10|^3.1|^4", "4"},
		"symfony/security-acl":    {"~2.8|~3.0", "3.0"},
	} {
		if d := find(deps, name); d.Skip != "" || d.Range != want[0] || d.Version != want[1] {
			t.Errorf("%s = %+v", name, d)
		}
	}
	for name, skip := range map[string]string{
		"cache/integration-tests":                          skipUnsupported, // dev-master
		"symfony/contracts":                                skipPath,        // a path repository's options.versions
		"jsonpath-standard/jsonpath-compliance-test-suite": skipPackageRepo,
		"symfony/runtime":                                  skipUnsupported, // self.version
		"composer-runtime-api":                             skipPlatform,
		"ext-xml":                                          skipPlatform,
	} {
		if d := find(deps, name); d.Skip != skip {
			t.Errorf("%s skip = %q, want %q", name, d.Skip, skip)
		}
	}
}

// TestExtractTYPO3Extension: the deps of a `"type": "typo3-cms-extension"` are AutoWiden, any
// other type's are not.
func TestExtractTYPO3Extension(t *testing.T) {
	deps := extract(t, "composer.json", read(t, "testdata/news/composer.json"))
	if d := find(deps, "typo3/cms-core"); d.Skip != "" || d.Range != "^13.4.29 || ^14.3" || !d.AutoWiden {
		t.Errorf("typo3/cms-core = %+v", d)
	}
	if d := find(deps, "phpunit/phpunit"); d.DepType != DepTypeRequireDev || !d.AutoWiden {
		t.Errorf("phpunit/phpunit = %+v", d)
	}
	for _, src := range []string{
		`{"type": "library", "require": {"a/b": "^1.0"}}`,
		`{"type": ["typo3-cms-extension"], "require": {"a/b": "^1.0"}}`,
	} {
		if deps := extract(t, "composer.json", []byte(src)); len(deps) != 1 || deps[0].AutoWiden {
			t.Errorf("%s: %+v", src, deps)
		}
	}
	if deps := extract(t, "composer.json", []byte(`{"require": {"a/b": "^1.0"}, "type": "typo3-cms-extension"}`)); len(deps) != 1 || !deps[0].AutoWiden {
		t.Errorf("type after require: %+v", deps)
	}
}

func TestExtractRepositories(t *testing.T) {
	deps := extract(t, "web/composer.json", read(t, "testdata/drupal/composer.json"))
	if d := find(deps, "drupal/core-recommended"); d.Source != "https://packages.drupal.org/8 "+PackagistURL || d.Range != "^11" || d.Version != "11" {
		t.Errorf("drupal/core-recommended = %+v", d)
	}
	src := []byte(`{
  "repositories": {
    "packagist.org": false,
    "private": {"type": "composer", "url": "https://repo.example.com", "only": ["acme/*"]},
    "mirror": {"type": "composer", "url": "https://mirror.example.com", "exclude": ["acme/secret"]},
    "acme/fork": {"type": "vcs", "url": "git@github.com:acme/fork.git"},
    "acme/odd": {"type": "git", "url": "file:///srv/odd"},
    "acme/local": {"type": "path", "url": "../local"},
    "pkg": {"type": "package", "package": [{"name": "Acme/Inline", "version": "1.0.0"}]},
    "one": {"type": "package", "package": {"name": "acme/single", "version": "1.0.0"}},
    "artifact": {"type": "artifact", "url": "zips/"},
    "bad": 3
  },
  "require": {
    "acme/lib": "^1.0", "acme/secret": "^1.0", "other/lib": "~2.1", "acme/fork": "^3.0", "acme/odd": "1.0.0",
    "acme/local": "*", "acme/inline": "1.0.0", "acme/single": "1.0.0"
  },
  "require-dev": {"x/escaped": "\u003e=1.0", "x/notstring": 1, "x/nover": "dev-main as 1.0"}
}`)
	deps = extract(t, "composer.json", src)
	for name, want := range map[string][3]string{ // datasource, source, skip
		"acme/lib":    {Datasource, "https://repo.example.com https://mirror.example.com", ""},
		"acme/secret": {Datasource, "https://repo.example.com", ""},
		"other/lib":   {Datasource, "https://mirror.example.com", ""},
		"acme/fork":   {GitDatasource, "https://github.com/acme/fork", ""},
		"acme/odd":    {Datasource, "", skipGitURL},
		"acme/local":  {Datasource, "", skipPath},
		"acme/inline": {Datasource, "", skipPackageRepo},
		"acme/single": {Datasource, "", skipPackageRepo},
		"x/escaped":   {Datasource, "", skipEscaped},
		"x/nover":     {Datasource, "", skipUnsupported},
	} {
		if d := find(deps, name); d.Datasource != want[0] || d.Source != want[1] || d.Skip != want[2] {
			t.Errorf("%s = %+v", name, d)
		}
	}
	if d := find(deps, "x/notstring"); d.Name != "" {
		t.Errorf("a non-string constraint is a dep: %+v", d)
	}

	// Array form; [{"packagist.org": false}] with no composer repository left.
	deps = extract(t, "composer.json", []byte(`{"require": {"a/b": "^1.0", "c/d": "^1.0"}, "repositories": [
		{"packagist.org": false}, {"type": "composer", "url": "https://r.example.com", "only": ["a/*"]}, {"type": "vcs", "url": "https://github.com/x/y"}]}`))
	if d := find(deps, "a/b"); d.Source != "https://r.example.com" || d.Skip != "" {
		t.Errorf("a/b = %+v", d)
	}
	if d := find(deps, "c/d"); d.Skip != skipNoRepository {
		t.Errorf("c/d = %+v", d)
	}
	deps = extract(t, "composer.json", []byte(`{"repositories": [{"type": "vcs", "name": "x/y", "url": "https://gitlab.com/x/y.git"}], "require": {"x/y": "^1.0"}}`))
	if d := find(deps, "x/y"); d.Datasource != GitDatasource || d.Source != "https://gitlab.com/x/y" {
		t.Errorf("named vcs = %+v", d)
	}
	// A vcs repository without a name provides the package of its repository path.
	deps = extract(t, "composer.json", []byte(`{"repositories": [{"type": "vcs", "url": "git@github.com:Acme/Tool.git"}, {"type": "vcs", "url": "file:///srv/x"}, {"type": "vcs"}],
		"require": {"acme/tool": "^1.0", "acme/other": "^1.0"}}`))
	if d := find(deps, "acme/tool"); d.Datasource != GitDatasource || d.Source != "https://github.com/Acme/Tool" {
		t.Errorf("unnamed vcs = %+v", d)
	}
	if d := find(deps, "acme/other"); d.Datasource != Datasource || d.Source != "" {
		t.Errorf("acme/other = %+v", d)
	}
}

func TestExtractErrors(t *testing.T) {
	for _, src := range []string{`[]`, `{"require": {"a/b": "^1.0"`, `{"require": {"a/b"`, `{"require": {1: 2}}`, ``, `{"repositories": [`, `{"type": "typo3`, `{"type": [`} {
		if _, err := New().Extract("composer.json", []byte(src), manager.Env{}); err == nil {
			t.Errorf("%q: no error", src)
		}
	}
	deps, err := New().Extract("composer.json", []byte(`{"require": [], "require-dev": "x", "name": "a/b"}`), manager.Env{})
	if err != nil || len(deps) != 0 {
		t.Errorf("non-object sections: %v, %v", deps, err)
	}
}

func TestNewRange(t *testing.T) {
	for _, tt := range []struct {
		rng, v, strategy, want string
	}{
		// Outside the constraint: replace (default).
		{"^1.2.3", "2.0.1", "", "^2.0.1"},
		{"^5.2.8", "6.0.1", "", "^6.0.1"},
		{"^4.1", "5.2.0", "", "^5.0"},
		{"~4.1", "5.2.0", "", "~5.0"},
		{"^0.4", "0.5.1", "", "^0.5"},
		{"^0.4", "1.2.0", "", "^1.0"},
		{"^4", "5.2.0", "", "^5"},
		{"~1.2.3", "1.3.1", "", "~1.3.1"},
		{"^v13.35.0", "v14.0.1", "", "^v14.0.1"},
		{"^13.35", "v14.2.0", "", "^14.0"},
		{"1.2.3", "1.4.0", "", "1.4.0"},
		{"v1.2.3", "1.4.0", "", "v1.4.0"},
		{"==1.2", "v1.3.0", "", "==1.3.0"},
		{"1.2.*", "1.4.2", "", "1.4.*"},
		{"1.x", "2.3.0", "", "2.x"},
		{">=1.0 <2.0", "2.3.0", "", ">=2.3 <3.0"},
		{">= 1.0, < 2.0", "2.3.0", "", ">= 2.3, < 3.0"},
		{">1.0 <1.3", "1.4.2", "", ">=1.4 <1.5"},
		{"<2", "2.5.0", "", "<3"},
		{"<=2.6", "3.1.4", "", "<=3.1.4"},
		{">=1.0 !=1.5.0 <2.0", "2.1.0", "", ">=2.1 !=1.5.0 <3.0"},
		{"1.0 - 2.0", "3.1.4", "", "3.1 - 3.1"},
		{"1.0.0 - 2.1.0", "3.1.4", "", "3.1.4 - 3.1.4"},
		{"^2.0|^3.0", "4.1.0", "", "^4.0"},
		{"^1.0@dev", "2.0.0-beta1", "", "^2.0@dev"},
		// Outside the constraint: widen.
		{"^1.2", "2.0.1", "widen", "^1.2 || ^2.0"},
		{"^2.0|^3.0", "4.1.0", "widen", "^2.0|^3.0|^4.0"},
		{"^1 || ^2", "3.0.0", "widen", "^1 || ^2 || ^3"},
		{">=1.0 <2.0", "2.3.0", "widen", ">=1.0 <3.0"},
		{"1.0 - 2.0", "2.5.0", "widen", "1.0 - 2.5"},
		{"1.2.*", "1.4.0", "widen", "1.2.* || 1.4.*"},
		{"1.2.3", "1.4.0", "widen", "1.4.0"},
		// Admitted: bump.
		{"^1.2", "1.4.2", "bump", "^1.4"},
		{"^1.2.3", "1.4.0", "bump", "^1.4.0"},
		{"~1.2.3", "1.2.5", "bump", "~1.2.5"},
		{">=1.2 <2.0", "1.4.0", "bump", ">=1.4 <2.0"},
		{">1.2", "1.4.0", "bump", ">=1.4"},
		{"1.0 - 2.0", "1.5.0", "bump", "1.5 - 2.0"},
		{"^1.0|^2.0", "2.3.0", "bump", "^1.0|^2.3"},
		{"^1.0@beta", "1.1.0-beta2", "", "^1.1@beta"},
		{"^v1.2", "v1.4.0", "", "^v1.4"},
		// pin.
		{"^1.2", "1.4.2", "pin", "1.4.2"},
		{"^v1.2", "1.4.2", "pin", "v1.4.2"},
	} {
		got, ok := newRange(tt.rng, tt.v, tt.strategy)
		if !ok || got != tt.want {
			t.Errorf("newRange(%q, %s, %q) = %q, %v, want %q", tt.rng, tt.v, tt.strategy, got, ok, tt.want)
		}
	}
	for _, tt := range []struct{ rng, v, strategy string }{
		{"^1", "1.4.0", "bump"},     // nothing to raise
		{"1.2.*", "1.2.5", "bump"},  // wildcards stay
		{"*", "1.0.0", ""},          // no lower bound
		{"<2.0", "1.5.0", "bump"},   // no lower bound
		{"1.2.3", "1.2.3", "pin"},   // already exact
		{"dev-main", "1.0.0", ""},   // no constraint
		{"^1.0", "dev-main", ""},    // no version
		{"!=1.5", "1.5", ""},        // an exclusion cannot admit it
		{"^1.0", "2.0.0-beta1", ""}, // `^2.0` would not admit the prerelease
	} {
		if got, ok := newRange(tt.rng, tt.v, tt.strategy); ok {
			t.Errorf("newRange(%q, %s, %q) = %q", tt.rng, tt.v, tt.strategy, got)
		}
	}
}

func TestEdits(t *testing.T) {
	src := []byte("{\n  \"require\": {\n    \"monolog/monolog\": \"^2.9\",\n    \"psr/log\": \"^1.1|^2.0\"\n  }\n}\n")
	deps := extract(t, "composer.json", src)
	m := New().(manager.RangeRewriter)
	var edits []patch.Edit
	for _, u := range []dep.Update{
		{Dep: deps[0], NewVersion: "3.8.1"},
		{Dep: deps[1], NewVersion: "3.0.2", RangeStrategy: "widen"},
	} {
		e, err := New().Edits(u)
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"require\": {\n    \"monolog/monolog\": \"^3.0\",\n    \"psr/log\": \"^1.1|^2.0|^3.0\"\n  }\n}\n"; string(out) != want {
		t.Errorf("got\n%s", out)
	}
	if _, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "2.9.0", RangeStrategy: "bump"}); err == nil || !strings.Contains(err.Error(), "cannot be updated") {
		t.Errorf("unchanged: %v", err)
	}
	if r, ok := m.NewRange(deps[0], "3.0.0", "pin"); !ok || r != "3.0.0" {
		t.Errorf("NewRange = %q, %v", r, ok)
	}
	if !slices.Contains([]string{anyAnchor("*"), anyAnchor("<2.0")}, "") || anyAnchor("1.0 - 2.0") != "1.0" || anyAnchor("1.2.*") != "1.2" {
		t.Error("anchor")
	}
}

func anyAnchor(rng string) string {
	a, _ := anchor(rng)
	return a
}
