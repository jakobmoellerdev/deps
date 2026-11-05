package bazel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// The testdata module files are verbatim copies (fetched 2026-11-05) of the MODULE.bazel of
// bazelbuild/bazel (overrides, maven.install and maven.artifact comprehensions,
// local_path_override), angular/angular (git_override commits), typedb/typedb (git_override
// tag and commits, oci.pull), google/comprehensive-rust (crate.spec), the skaffold Bazel
// example (oci.pull with a digest only), google/gvisor (rules_img pull) and cloudflare/workerd
// (archive_override in the root module, git_repository and local_path_override in the
// included build/deps/v8.MODULE.bazel).
var fixtures = []string{
	"bazel/MODULE.bazel", "angular/MODULE.bazel", "typedb/MODULE.bazel", "comprehensive-rust/MODULE.bazel",
	"skaffold/MODULE.bazel", "gvisor/MODULE.bazel", "workerd/MODULE.bazel", "workerd/build/deps/v8.MODULE.bazel",
}

func extractLinked(t *testing.T, files ...string) ([][]dep.Dep, map[string][]byte) {
	t.Helper()
	m := New()
	srcs := map[string][]byte{}
	var out [][]dep.Dep
	for _, f := range files {
		src, err := os.ReadFile(filepath.Join("testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		if !m.Match(f) {
			t.Fatalf("Match(%s) = false", f)
		}
		ds, err := m.Extract(f, src, manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		srcs[f] = src
		out = append(out, ds)
	}
	m.(manager.Linker).Link(out)
	return out, srcs
}

func find(t *testing.T, files [][]dep.Dep, file, depType, name string) dep.Dep {
	t.Helper()
	for _, f := range files {
		for _, d := range f {
			if d.File == file && d.DepType == depType && d.Name == name {
				return d
			}
		}
	}
	t.Fatalf("no %s %s in %s", depType, name, file)
	return dep.Dep{}
}

// at returns the source text of a span.
func at(srcs map[string][]byte, d dep.Dep, sp dep.Span) string {
	return string(srcs[d.File][sp.Start:sp.End])
}

func TestExtractRealWorld(t *testing.T) {
	files, srcs := extractLinked(t, fixtures...)
	count := map[string]int{}
	for _, f := range files {
		for _, d := range f {
			count[d.DepType]++
			if d.Skip == "" && d.Version != defaultBranch && at(srcs, d, d.RefSpan) != d.Version {
				t.Errorf("%s %s: RefSpan reads %q, version %q", d.File, d.Name, at(srcs, d, d.RefSpan), d.Version)
			}
			if d.Digest != "" && at(srcs, d, d.DigestSpan) != d.Digest {
				t.Errorf("%s %s: DigestSpan reads %q", d.File, d.Name, at(srcs, d, d.DigestSpan))
			}
		}
	}
	want := map[string]int{
		DepTypeBazelDep: 111, DepTypeSingleVersionOverride: 8, DepTypeGitOverride: 10, DepTypeLocalPathOverride: 4,
		DepTypeArchiveOverride: 1, DepTypeMavenInstall: 98, DepTypeOCIPull: 3, DepTypeCrateSpec: 6,
		DepTypeRulesImgPull: 5, DepTypeGitRepository: 1,
	}
	for k, n := range want {
		if count[k] != n {
			t.Errorf("%s: %d deps, want %d (all: %v)", k, count[k], n, count)
		}
	}

	for _, c := range []struct {
		file, depType, name, ds, version, digest, source, skip string
		line                                                   int
	}{
		{"bazel/MODULE.bazel", DepTypeBazelDep, "blake3", "bazel", "1.8.2.bcr.1", "", "", "", 19},
		{"bazel/MODULE.bazel", DepTypeBazelDep, "re2", "bazel", "2025-11-05.bcr.1", "", "", "", 28},
		{"bazel/MODULE.bazel", DepTypeBazelDep, "googleapis", "bazel", "", "", "", skipPinned, 22},
		{"bazel/MODULE.bazel", DepTypeBazelDep, "grpc", "bazel", "", "", "", skipPinned, 25},
		{"bazel/MODULE.bazel", DepTypeBazelDep, "rules_python", "bazel", "2.2.0", "", "", skipPinned, 35},
		{"bazel/MODULE.bazel", DepTypeBazelDep, "remoteapis", "bazel", "", "", "", "overridden by local_path_override", 60},
		{"bazel/MODULE.bazel", DepTypeSingleVersionOverride, "grpc", "bazel", "1.76.0.bcr.1", "", "", skipVersionPin, 85},
		{"bazel/MODULE.bazel", DepTypeLocalPathOverride, "remoteapis", "", "", "", "", skipLocal, 150},
		{"bazel/MODULE.bazel", DepTypeMavenInstall, "com.google.guava:guava", "maven", "33.7.2-jre", "", "https://repo1.maven.org/maven2/", "", 191},
		{"bazel/MODULE.bazel", DepTypeMavenInstall, "org.ow2.asm:", "maven", "", "", "https://repo1.maven.org/maven2/", skipNonLiteral, 291},
		{"angular/MODULE.bazel", DepTypeBazelDep, "rules_angular", "bazel", "", "", "", "overridden by git_override", 25},
		{"angular/MODULE.bazel", DepTypeGitOverride, "rules_angular", "git-refs", "HEAD", "7822265c5a0565c8c1fdc254e021178131bfcfa8", "https://github.com/angular/rules_angular", "", 28},
		{"typedb/MODULE.bazel", DepTypeGitOverride, "rules_rust", "git-tags", "0.56.0", "", "https://github.com/bazelbuild/rules_rust", "", 28},
		{"typedb/MODULE.bazel", DepTypeOCIPull, "typedb/ubuntu", "docker", "3.1.0-amd64", "", "docker.io/typedb/ubuntu", "", 97},
		{"comprehensive-rust/MODULE.bazel", DepTypeCrateSpec, "mdbook", "crate", "0.5.3", "", "", "", 54},
		{"skaffold/MODULE.bazel", DepTypeOCIPull, "gcr.io/distroless/base", "docker", "", "", "gcr.io/distroless/base", skipDigestNoTag, 17},
		{"gvisor/MODULE.bazel", DepTypeRulesImgPull, "oven/bun", "docker", "1.4.3", "sha256:ec06c3b6cea04192ae6770c434f668ca41d343ad19fa6472216c7b48be39c598", "docker.io/oven/bun", "", 314},
		{"workerd/MODULE.bazel", DepTypeBazelDep, "sqlite3", "bazel", "", "", "", "overridden by archive_override", 8},
		{"workerd/MODULE.bazel", DepTypeArchiveOverride, "sqlite3", "", "", "", "", skipArchive, 9},
		{"workerd/build/deps/v8.MODULE.bazel", DepTypeBazelDep, "google_benchmark", "bazel", "", "", "", "overridden by local_path_override", 115},
		{"workerd/build/deps/v8.MODULE.bazel", DepTypeGitRepository, "external/github.com/llvm/llvm-project/libc", "git-refs", "HEAD", "fe440a8abe663d6878686f147ef6b1a02075ae15", "https://chromium.googlesource.com/external/github.com/llvm/llvm-project/libc", "", 90},
	} {
		d := find(t, files, c.file, c.depType, c.name)
		if d.Datasource != c.ds || d.Version != c.version || d.Digest != c.digest || d.Source != c.source || d.Skip != c.skip || d.Line != c.line {
			t.Errorf("%s %s %s = %s %q %q %q skip %q line %d\nwant %s %q %q %q skip %q line %d", c.file, c.depType, c.name,
				d.Datasource, d.Version, d.Digest, d.Source, d.Skip, d.Line, c.ds, c.version, c.digest, c.source, c.skip, c.line)
		}
	}
	d := find(t, files, "comprehensive-rust/MODULE.bazel", DepTypeCrateSpec, "mdbook")
	if d.Range != "0.5.3" || d.Constraint == "" || d.Versioning != "cargo" {
		t.Errorf("crate.spec range = %q, constraint %q, versioning %q", d.Range, d.Constraint, d.Versioning)
	}
}

func TestMatch(t *testing.T) {
	m := New()
	for p, want := range map[string]bool{
		"MODULE.bazel": true, "a/MODULE.bazel": true, "build/deps/v8.MODULE.bazel": true,
		"MODULE.bazel.lock": false, "XMODULE.bazel": false, "WORKSPACE": false, "MODULE.bzl": false,
	} {
		if m.Match(p) != want {
			t.Errorf("Match(%s) = %v", p, !want)
		}
	}
}

func extractSrc(t *testing.T, file, src string) []dep.Dep {
	t.Helper()
	ds, err := New().Extract(file, []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

// TestExtractForms covers the call and literal forms the fixtures do not.
func TestExtractForms(t *testing.T) {
	src := `# bazel_dep(name = "commented", version = "1.0")
bazel_dep(name = 'single', version = '0.1.0')
bazel_dep(name = "raw", version = r"2.0")
bazel_dep(name = "escaped", version = "1.\x30")
bazel_dep(name = "var", version = VERSION)
bazel_dep(name = NAME, version = "1")
bazel_dep(name = "triple", version = """3.0""")
bazel_dep(
    name = "multi",  # a comment with "quotes" and (parens)
    version = "4.0",
    dev_dependency = True,
)
not_a.bazel_dep(name = "method", version = "9")
single_version_override(module_name = "single", registry = "https://registry.example.com")
single_version_override(module_name = VAR)
git_override(module_name = "g1", remote = "https://example.com/o/r.git", branch = "main")
git_override(module_name = "g2", remote = "/local/path", commit = "0123456789012345678901234567890123456789")
git_override(module_name = "g3", remote = "git@github.com:o/r.git", commit = "abc")
git_override(module_name = "g4", remote = "https://github.com/o/r", tag = "v1.2.0", commit = "0123456789012345678901234567890123456789")
git_override(module_name = "g5", remote = "https://github.com/o/r", tag = TAG)
git_override(module_name = "g6", remote = "https://github.com/o/r")
git_override(module_name = "g7", remote = REMOTE, commit = "0123456789012345678901234567890123456789")
multiple_version_override(module_name = "multi", versions = ["4.0", "5.0"])
git_repository(name = "direct", remote = "https://github.com/o/r", tag = "v1")

oci = use_extension("@@rules_oci//oci:extensions.bzl", "oci")
oci.pull(name = "a", image = "https://docker.io/library/nginx", tag = "1.27.1", digest = "sha256:aaaa")
oci.pull(name = "b", image = "nginx:1.0", tag = "1")
oci.pull(name = "c", image = "nginx")
oci.pull(name = "d", image = IMG)
oci.pull(name = "e", image = "nginx", tag = TAG)
other = use_extension("@other//:ext.bzl", "oci")
other.pull(name = "x", image = "nginx", tag = "1")

maven = use_extension("@rules_jvm_external//:extensions.bzl", "maven")
maven.artifact(group = "com.squareup", artifact = "javapoet", version = "1.11.1", name = "other")
maven.artifact(group = "com.squareup", artifact = "javapoet", version = VERSION)
maven.artifact(group = "", artifact = "x", version = "1")
maven.install(artifacts = ARTIFACTS)
maven.install(name = "other", repositories = ["https://maven.example.com/repo/", "https://repo1.maven.org/maven2"], artifacts = [
    "g:a:jar:sources:1.0",
    "g:managed",
    "g::1",
    "a:b:c:d:e:f",
    "g:esc\x3a1",
])
maven.install()

crate = use_extension("@rules_rust//crate_universe:extension.bzl", "crate")
crate.spec(package = "tokio", version = "~1.45", features = ["full"])
crate.spec(package = "local", path = "../x")
crate.spec(package = "fork", git = "https://github.com/o/fork.git", tag = "v1.0.0")
crate.spec(package = "branch", git = "https://github.com/o/fork.git", branch = "main")
crate.spec(package = "bad", version = "latest")
crate.spec(package = PKG, version = "1")

git_repo = use_repo_rule("@bazel_tools//tools/build_defs/repo:git.bzl", "new_git_repository")
git_repo(name = "ngr", remote = "https://gitlab.com/g/p.git", commit = "0123456789012345678901234567890123456789", build_file = "//:x.BUILD")
pull = use_repo_rule("@rules_img//img:pull.bzl", "pull")
pull(name = "u", registry = "ghcr.io", repository = "o/img", tag = "2.0")
pull(name = "v", registry = REG, repository = "o/img", tag = "2.0")
pull(name = "w", repository = REPO, tag = "2.0")
`
	ds := extractSrc(t, "MODULE.bazel", src)
	linked := [][]dep.Dep{ds}
	New().(manager.Linker).Link(linked)
	ds = linked[0]
	type w struct{ depType, name, ds, version, digest, source, skip string }
	var got []w
	for _, d := range ds {
		got = append(got, w{d.DepType, d.Name, d.Datasource, d.Version, d.Digest, d.Source, d.Skip})
	}
	sha := "0123456789012345678901234567890123456789"
	want := []w{
		{DepTypeBazelDep, "single", "bazel", "0.1.0", "", "https://registry.example.com", ""},
		{DepTypeBazelDep, "raw", "bazel", "2.0", "", "", ""},
		{DepTypeBazelDep, "escaped", "bazel", "", "", "", skipNonLiteral},
		{DepTypeBazelDep, "var", "bazel", "", "", "", skipNonLiteral},
		{DepTypeBazelDep, "", "bazel", "", "", "", skipNonLiteral},
		{DepTypeBazelDep, "triple", "bazel", "3.0", "", "", ""},
		{DepTypeBazelDep, "multi", "bazel", "4.0", "", "", "overridden by multiple_version_override"},
		{DepTypeSingleVersionOverride, "", "", "", "", "", skipNonLiteral},
		{DepTypeGitOverride, "g1", "", "", "", "https://example.com/o/r", skipBranch},
		{DepTypeGitOverride, "g2", "", "", "", "", skipRemote},
		{DepTypeGitOverride, "g3", "", "", "", "https://github.com/o/r", skipGitRef},
		{DepTypeGitOverride, "g4", "git-tags", "v1.2.0", sha, "https://github.com/o/r", ""},
		{DepTypeGitOverride, "g5", "", "", "", "https://github.com/o/r", skipNonLiteral},
		{DepTypeGitOverride, "g6", "", "", "", "https://github.com/o/r", skipGitRef},
		{DepTypeGitOverride, "g7", "", "", "", "", skipRemote},
		{DepTypeMultipleVersionOverride, "multi", "", "", "", "", skipMultiple},
		{DepTypeOCIPull, "nginx", "docker", "1.27.1", "sha256:aaaa", "docker.io/library/nginx", ""},
		{DepTypeOCIPull, "nginx:1.0", "docker", "", "", "", skipImage},
		{DepTypeOCIPull, "nginx", "docker", "", "", "docker.io/library/nginx", skipNoTag},
		{DepTypeOCIPull, "", "docker", "", "", "", skipNonLiteral},
		{DepTypeOCIPull, "nginx", "docker", "", "", "docker.io/library/nginx", skipNonLiteral},
		{DepTypeMavenInstall, "com.squareup:javapoet", "maven", "1.11.1", "", "https://maven.example.com/repo/ https://repo1.maven.org/maven2/", ""},
		{DepTypeMavenInstall, "com.squareup:javapoet", "maven", "", "", "", skipNonLiteral},
		{DepTypeMavenInstall, ":x", "maven", "", "", "", skipCoordinates},
		{DepTypeMavenInstall, "artifacts", "maven", "", "", "", skipNonLiteral},
		{DepTypeMavenInstall, "g:a", "maven", "1.0", "", "https://maven.example.com/repo/ https://repo1.maven.org/maven2/", ""},
		{DepTypeMavenInstall, "g:managed", "maven", "", "", "https://maven.example.com/repo/ https://repo1.maven.org/maven2/", skipNoVersion},
		{DepTypeMavenInstall, "g::1", "maven", "", "", "https://maven.example.com/repo/ https://repo1.maven.org/maven2/", skipCoordinates},
		{DepTypeMavenInstall, "a:b:c:d:e:f", "maven", "", "", "https://maven.example.com/repo/ https://repo1.maven.org/maven2/", skipCoordinates},
		{DepTypeMavenInstall, `g:esc\x3a1`, "maven", "", "", "https://maven.example.com/repo/ https://repo1.maven.org/maven2/", skipNonLiteral},
		{DepTypeCrateSpec, "tokio", "crate", "1.45", "", "", ""},
		{DepTypeCrateSpec, "local", "crate", "", "", "", skipCratePath},
		{DepTypeCrateSpec, "fork", "git-tags", "v1.0.0", "", "https://github.com/o/fork", ""},
		{DepTypeCrateSpec, "branch", "crate", "", "", "", skipCrateGit},
		{DepTypeCrateSpec, "bad", "crate", "latest", "", "", skipRequirement},
		{DepTypeCrateSpec, "", "crate", "", "", "", skipNonLiteral},
		{DepTypeNewGitRepository, "g/p", "git-refs", "HEAD", sha, "https://gitlab.com/g/p", ""},
		{DepTypeRulesImgPull, "ghcr.io/o/img", "docker", "2.0", "", "ghcr.io/o/img", ""},
		{DepTypeRulesImgPull, "", "docker", "", "", "", skipNonLiteral},
		{DepTypeRulesImgPull, "", "docker", "", "", "", skipNonLiteral},
	}
	if len(got) != len(want) {
		t.Errorf("got %d deps, want %d: %+v", len(got), len(want), got[min(len(got), len(want)):])
	}
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			t.Errorf("dep %d = %+v\nwant   %+v", i, got[i], want[i])
		}
	}
}

// Overrides apply across the module files of a workspace, not across workspaces; more than
// one override for a module leaves its bazel_dep alone.
func TestLinkWorkspaces(t *testing.T) {
	root := extractSrc(t, "MODULE.bazel", `include("//deps:a.MODULE.bazel")
local_path_override(module_name = "seg", path = "x")
single_version_override(module_name = "twice", version = "1.0")
git_override(module_name = "twice", remote = "https://github.com/o/r", commit = "0123456789012345678901234567890123456789")
single_version_override(module_name = "reg", registry = "file://%workspace%/registry")
`)
	seg := extractSrc(t, "deps/a.MODULE.bazel", `bazel_dep(name = "seg", version = "1.0")
bazel_dep(name = "twice", version = "1.0")
bazel_dep(name = "reg", version = "1.0")
`)
	nested := extractSrc(t, "examples/MODULE.bazel", `bazel_dep(name = "seg", version = "1.0")`)
	stray := extractSrc(t, "other/x.MODULE.bazel", `bazel_dep(name = "seg", version = "1.0")`)
	files := [][]dep.Dep{root, seg, nested, stray, nil}
	New().(manager.Linker).Link(files)
	if n := len(files[0]); n != 3 {
		t.Errorf("root deps = %+v (registry-only override not dropped)", files[0])
	}
	skips := []string{files[1][0].Skip, files[1][1].Skip, files[1][2].Skip, files[2][0].Skip, files[3][0].Skip}
	if !slices.Equal(skips, []string{"overridden by local_path_override", "", "", "", "overridden by local_path_override"}) {
		t.Errorf("skips = %q", skips)
	}
	if files[1][2].Source != "file://%workspace%/registry" {
		t.Errorf("registry override source = %q", files[1][2].Source)
	}
}

func TestEdits(t *testing.T) {
	src := `bazel_dep(name = "rules_go", version = "0.62.0")
git_override(module_name = "x", remote = "https://github.com/o/x", commit = "0123456789012345678901234567890123456789")
oci = use_extension("@rules_oci//oci:extensions.bzl", "oci")
oci.pull(name = "n", image = "docker.io/library/nginx", tag = "1.27.1", digest = "sha256:aaaa")
oci.pull(name = "m", image = "docker.io/library/nginx", tag = "1.27.1")
crate = use_extension("@rules_rust//crate_universe:extension.bzl", "crate")
crate.spec(package = "tokio", version = "1.45.1")
`
	ds := extractSrc(t, "MODULE.bazel", src)
	m := New()
	apply := func(u dep.Update) string {
		t.Helper()
		e, err := m.Edits(u)
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply([]byte(src), e)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	line := func(s string, n int) string { return strings.Split(s, "\n")[n] }
	if got := line(apply(dep.Update{Dep: ds[0], NewVersion: "0.65.0"}), 0); got != `bazel_dep(name = "rules_go", version = "0.65.0")` {
		t.Errorf("bazel_dep: %s", got)
	}
	newSHA := strings.Repeat("f", 40)
	if got := line(apply(dep.Update{Dep: ds[1], NewVersion: "HEAD", NewDigest: newSHA}), 1); !strings.Contains(got, `commit = "`+newSHA+`"`) {
		t.Errorf("git_override: %s", got)
	}
	if got := line(apply(dep.Update{Dep: ds[2], NewVersion: "1.28.0", NewDigest: "sha256:bbbb"}), 3); got != `oci.pull(name = "n", image = "docker.io/library/nginx", tag = "1.28.0", digest = "sha256:bbbb")` {
		t.Errorf("oci.pull: %s", got)
	}
	if got := line(apply(dep.Update{Dep: ds[4], NewVersion: "2.0.0", RangeStrategy: "replace"}), 6); got != `crate.spec(package = "tokio", version = "2.0.0")` {
		t.Errorf("crate.spec: %s", got)
	}
	if r, ok := m.(manager.RangeRewriter).NewRange(ds[4], "2.1.0", "replace"); !ok || r != "2.1.0" {
		t.Errorf("NewRange = %q, %v", r, ok)
	}
	if _, err := m.Edits(dep.Update{Dep: ds[2], NewVersion: "1.28.0"}); err == nil {
		t.Error("dropping a digest succeeded")
	}
	if _, err := m.Edits(dep.Update{Dep: ds[3], NewVersion: "1.27.1", NewDigest: "sha256:cc"}); err == nil || !strings.Contains(err.Error(), "no digest argument") {
		t.Errorf("pinning without a digest argument: %v", err)
	}
}

func TestTokenize(t *testing.T) {
	toks := tokenize([]byte("a = \"x\\\"y\" + b'z' # c \"d\"\n'''multi\nline''' 1.5 == != é \"unterminated\n\"\"\"open"))
	var kinds []int
	for _, tk := range toks {
		kinds = append(kinds, tk.kind)
	}
	want := []int{tokIdent, tokPunct, tokString, tokPunct, tokString, tokString, tokOther, tokPunct, tokPunct, tokOther, tokString, tokString}
	if !slices.Equal(kinds, want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	if toks[2].plain || !toks[4].plain || toks[4].text != "z" || toks[5].text != "multi\nline" || toks[10].plain || toks[11].plain {
		t.Errorf("strings = %+v %+v %+v %+v", toks[2], toks[4], toks[5], toks[10])
	}
	// An unbalanced call still yields its arguments.
	cs := calls(tokenize([]byte(`f(a = "1", "p"`)))
	if len(cs) != 1 || cs[0].kwargs["a"].str.text != "1" || positional(cs[0], 0) != "p" || positional(cs[0], 1) != "" {
		t.Errorf("calls = %+v", cs)
	}
	if v := valueOf(tokenize([]byte(`["a" "b"]`))); v.kind != valOther {
		t.Errorf("implicit concatenation in a list = %+v", v)
	}
}

func TestBazelisk(t *testing.T) {
	m := NewBazelisk()
	if !m.Match(".bazelversion") || !m.Match("a/.bazelversion") || m.Match("bazelversion") {
		t.Error("Match")
	}
	for src, want := range map[string]struct{ version, skip string }{
		"9.3.1\n":             {"9.3.1", ""},
		"\ufeff  8.4.2 \r\n7": {"8.4.2", ""},
		"latest\n":            {"latest", skipRolling},
		"last_green":          {"last_green", skipRolling},
		"7.x":                 {"7.x", skipRolling},
		"user/7.0.0":          {"user/7.0.0", skipFork},
	} {
		ds, err := m.Extract(".bazelversion", []byte(src), manager.Env{})
		if err != nil || len(ds) != 1 {
			t.Fatalf("Extract(%q) = %v, %v", src, ds, err)
		}
		d := ds[0]
		if d.Version != want.version || d.Skip != want.skip || d.Name != "bazelbuild/bazel" || d.Datasource != "github-releases" || d.DepType != DepTypeBazel {
			t.Errorf("Extract(%q) = %+v", src, d)
		}
		if d.Skip == "" && src[d.RefSpan.Start:d.RefSpan.End] != d.Version {
			t.Errorf("Extract(%q) span reads %q", src, src[d.RefSpan.Start:d.RefSpan.End])
		}
	}
	if ds, _ := m.Extract(".bazelversion", []byte("\n"), manager.Env{}); ds != nil {
		t.Errorf("empty file: %+v", ds)
	}
	ds, _ := m.Extract(".bazelversion", []byte("9.3.1\n"), manager.Env{})
	e, err := m.Edits(dep.Update{Dep: ds[0], NewVersion: "9.4.0"})
	if out, _ := patch.Apply([]byte("9.3.1\n"), e); err != nil || string(out) != "9.4.0\n" {
		t.Errorf("Edits = %q, %v", out, err)
	}
}
