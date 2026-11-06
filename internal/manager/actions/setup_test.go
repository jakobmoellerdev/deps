package actions

import (
	"cmp"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

const setupFixture = `runs:
  using: composite
  steps:
    - uses: actions/setup-node@v4
      with:
        node-version: 26
    - uses: actions/setup-go@v5
      with:
        go-version: '1.27'
    - uses: Actions/Setup-Go@v5
      with: {go-version: "1.27.1", cache: false}
    - uses: azure/setup-helm@v4
      with:
        version: v3.16.0 # pinned
    - uses: azure/setup-kubectl@v4
      with:
        version: "v1.34.1"
    - uses: hashicorp/setup-terraform@v3
      with:
        terraform_version: 1.13.4
    - uses: golangci/golangci-lint-action@v8
      with:
        version: v2.1
    - uses: actions/setup-node@v4
      with:
        node-version: ${{ matrix.node }}
    - uses: actions/setup-node@v4
      with:
        node-version: lts/*
    - uses: actions/setup-go@v5
      with:
        go-version: stable
    - uses: actions/setup-go@v5
      with:
        go-version: 1.27.x
    - uses: actions/setup-node@v4
      with:
        node-version: ">=24"
    - uses: azure/setup-helm@v4
      with:
        # renovate: datasource=github-releases depName=helm/helm
        version: v3.16.0
    - uses: azure/setup-helm@v4
      with:
        # deps: github-releases helm/helm
        version: v3.16.0
    - uses: actions/setup-python@v5
      with:
        python-version: "3.13"
    - uses: actions/setup-java@v4
      with:
        java-version: "21"
    - uses: actions/setup-node@v4
      with:
        node-version: |
          26
    - uses: actions/setup-node@v4
    - with:
        node-version: 26
    - uses: actions/setup-python@v5
      with:
        python-version: 3.x
    - uses: actions/setup-python@v5
      with:
        python-version: pypy3.10
    - uses: actions/setup-python@v5
      with:
        python-version: 3.13t
    - uses: actions/setup-java@v4
      with:
        distribution: temurin
        java-version: 21
    - uses: actions/setup-java@v4
      with:
        distribution: Temurin
        java-version: 17.0.7+7
        java-package: jre
    - uses: actions/setup-java@v4
      with:
        java-version: 21.x
        distribution: adopt
    - uses: actions/setup-java@v4
      with: {distribution: zulu, java-version: "21"}
    - uses: actions/setup-dotnet@v4
      with:
        dotnet-version: 8.0.x
    - uses: actions/setup-dotnet@v4
      with:
        dotnet-version: '8.0'
    - uses: actions/setup-dotnet@v4
      with:
        dotnet-version: 8.0.100
    - uses: actions/setup-dotnet@v4
      with:
        dotnet-version: 9.0.1xx
    - uses: ruby/setup-ruby@v1
      with:
        ruby-version: "3.4"
    - uses: ruby/setup-ruby@v1
      with:
        ruby-version: .ruby-version
    - uses: ruby/setup-ruby@v1
      with:
        ruby-version: jruby-9.4
    - uses: ruby/setup-ruby@v1
      with:
        ruby-version: 3.4.7
`

func TestExtractSetup(t *testing.T) {
	src := []byte(setupFixture)
	deps, err := New().Extract("action.yml", src, env)
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		line                              int
		datasource, name, versioning, ver string
		skip                              string
	}
	wants := []want{
		{6, "node-version", "node", "tag", "26", ""},
		{9, "go-toolchain", "go", "tag", "1.27", ""},
		{11, "go-toolchain", "go", "semver", "1.27.1", ""},
		{14, "github-releases", "helm/helm", "semver", "v3.16.0", ""},
		{17, "github-releases", "kubernetes/kubernetes", "semver", "v1.34.1", ""},
		{20, "github-releases", "hashicorp/terraform", "semver", "1.13.4", ""},
		{23, "github-releases", "golangci/golangci-lint", "tag", "v2.1", ""},
		{26, "node-version", "node", "", "${{ matrix.node }}", "expression"},
		{29, "node-version", "node", "", "lts/*", "non-version value"},
		{32, "go-toolchain", "go", "", "stable", "non-version value"},
		{35, "go-toolchain", "go", "semver", "1.27.0", ""},   // range 1.27.x
		{38, "node-version", "node", "semver", "24.0.0", ""}, // range >=24
		{49, "python-version", "python", "tag", "3.13", ""},
		{52, "java-version", "java-jdk", "", "21", "unsupported distribution"},
		{55, "node-version", "node", "", "26\n", "unsupported range"},
		{62, "python-version", "python", "semver", "3.0.0", ""}, // range 3.x
		{65, "python-version", "python", "", "pypy3.10", "non-version value"},
		{68, "python-version", "python", "", "3.13t", "unsupported range"},
		{72, "java-version", "java-jdk", "tag", "21", ""},
		{76, "java-version", "java-jre", "semver", "17.0.7+7", ""},
		{80, "java-version", "java-jdk", "", "21.x", "unsupported range"},
		{83, "java-version", "java-jdk", "", "21", "unsupported distribution"},
		{86, "dotnet-version", "dotnet-sdk", "semver", "8.0.0", ""}, // range 8.0.x
		{89, "dotnet-version", "dotnet-sdk", "semver", "8.0.0", ""}, // range 8.0 (channel)
		{92, "dotnet-version", "dotnet-sdk", "semver", "8.0.100", ""},
		{95, "dotnet-version", "dotnet-sdk", "", "9.0.1xx", "unsupported range"},
		{98, "ruby-version", "ruby", "tag", "3.4", ""},
		{101, "ruby-version", "ruby", "", ".ruby-version", "non-version value"},
		{104, "ruby-version", "ruby", "", "jruby-9.4", "non-version value"},
		{107, "ruby-version", "ruby", "semver", "3.4.7", ""},
	}
	ranges := map[int]string{35: "1.27.x", 38: ">=24", 62: "3.x", 86: "8.0.x", 89: "8.0"}
	var got []dep.Dep
	for _, d := range deps {
		if d.DepType == DepTypeSetup {
			got = append(got, d)
		}
	}
	if len(got) != len(wants) {
		t.Fatalf("got %d setup deps, want %d: %+v", len(got), len(wants), got)
	}
	for i, w := range wants {
		d := got[i]
		if d.Manager != Name || d.Line != w.line || d.Datasource != w.datasource || d.Name != w.name ||
			d.Versioning != w.versioning || d.Version != w.ver || d.Skip != w.skip {
			t.Errorf("%d: got %+v, want %+v", i, d, w)
			continue
		}
		if rng := ranges[w.line]; d.Range != rng {
			t.Errorf("%d: range %q, want %q", i, d.Range, rng)
		}
		if d.Skip == "" {
			if s, want := string(src[d.RefSpan.Start:d.RefSpan.End]), cmp.Or(d.Range, w.ver); s != want {
				t.Errorf("%d: span %q, want %q", i, s, want)
			}
		}
		if w.datasource == "github-releases" && d.Source != "https://github.com/"+w.name {
			t.Errorf("%d: source %q", i, d.Source)
		}
	}
}

func TestSetupEdits(t *testing.T) {
	tests := []struct{ src, newVersion, want string }{
		{"- uses: actions/setup-node@v4\n  with:\n    node-version: 26\n", "27", "- uses: actions/setup-node@v4\n  with:\n    node-version: 27\n"},
		{"- uses: actions/setup-go@v5\n  with:\n    go-version: '1.27.1' # go\n", "1.27.2", "- uses: actions/setup-go@v5\n  with:\n    go-version: '1.27.2' # go\n"},
		{"- uses: azure/setup-helm@v4\r\n  with: {version: \"v3.16.0\"}\r\n", "v3.17.0", "- uses: azure/setup-helm@v4\r\n  with: {version: \"v3.17.0\"}\r\n"},
	}
	for i, tt := range tests {
		deps, err := New().Extract("action.yml", []byte(tt.src), env)
		if err != nil || len(deps) != 2 || deps[1].Skip != "" {
			t.Fatalf("%d: deps = %+v, err = %v", i, deps, err)
		}
		edits, err := New().Edits(dep.Update{Dep: deps[1], NewVersion: tt.newVersion})
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply([]byte(tt.src), edits)
		if err != nil || string(out) != tt.want {
			t.Errorf("%d: got %q, %v; want %q", i, out, err, tt.want)
		}
	}
}

// Setup input ranges are rewritten whole, per rangeStrategy, by npm's range rules.
func TestSetupRangeEdits(t *testing.T) {
	tests := []struct{ value, newVersion, strategy, want string }{
		{"1.27.x", "1.28.1", "", "1.28.x"},
		{"'1.27.x'", "1.28", "update-lockfile", "'1.28.x'"},
		{`">=24"`, "26.1.0", "bump", `">=26"`},
		{"^3.12", "4.0.0", "replace", "^4.0"},
		{"8.0", "10.0.100", "", "10.0"},
		{"8.0.x", "10.0.100", "widen", "8.0.x || 10.0.x"},
	}
	for i, tt := range tests {
		src := "- uses: actions/setup-dotnet@v4\n  with:\n    dotnet-version: " + tt.value + "\n"
		deps, err := New().Extract("action.yml", []byte(src), env)
		if err != nil || len(deps) != 2 || deps[1].Skip != "" || deps[1].Range == "" {
			t.Fatalf("%d: deps = %+v, err = %v", i, deps, err)
		}
		d := deps[1]
		want := tt.want
		if q := tt.value[0]; q == '\'' || q == '"' {
			want = tt.want[1 : len(tt.want)-1]
		}
		if nr, ok := New().(actionsManager).NewRange(d, tt.newVersion, tt.strategy); !ok || nr != want {
			t.Errorf("%d: NewRange = %q, %v; want %q", i, nr, ok, want)
		}
		edits, err := New().Edits(dep.Update{Dep: d, NewVersion: tt.newVersion, RangeStrategy: tt.strategy})
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply([]byte(src), edits)
		if wantSrc := "- uses: actions/setup-dotnet@v4\n  with:\n    dotnet-version: " + tt.want + "\n"; err != nil || string(out) != wantSrc {
			t.Errorf("%d: got %q, %v; want %q", i, out, err, wantSrc)
		}
	}
	// A version the range cannot be written for, and deps without a range.
	src := "- uses: actions/setup-go@v5\n  with:\n    go-version: 1.27.x\n"
	deps, _ := New().Extract("action.yml", []byte(src), env)
	if _, err := New().Edits(dep.Update{Dep: deps[1], NewVersion: "not-a-version"}); err == nil {
		t.Error("want an error for a version the range cannot take")
	}
	if _, ok := New().(actionsManager).NewRange(deps[0], "v5.1.0", ""); ok {
		t.Error("NewRange of a dep without a range")
	}
}
