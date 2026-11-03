package actions

import (
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
		{35, "go-toolchain", "go", "", "1.27.x", "unsupported range"},
		{38, "node-version", "node", "", ">=24", "unsupported range"},
		{55, "node-version", "node", "", "26\n", "unsupported range"},
	}
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
		if d.Skip == "" {
			if s := string(src[d.RefSpan.Start:d.RefSpan.End]); s != w.ver {
				t.Errorf("%d: span %q, want %q", i, s, w.ver)
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
