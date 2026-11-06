package kustomize

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

const digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const digestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

const sha = "0123456789abcdef0123456789abcdef01234567"

const kustomization = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml
  - ../base
  - https://github.com/kubernetes-sigs/kustomize//examples/multibases?ref=v5.4.0
  - github.com/org/repo/deploy/prod?ref=v1 # legacy form
  - git@github.com:org/infra.git//apps/x?ref=v1.2&timeout=2m
  - "ssh://git@git.example.com:2222/team/app.git//k8s?submodules=false&ref=v0.3.1"
  - https://dev.azure.com/org/project/_git/repo/path?version=v2.0.0
  - https://gitlab.com/group/repo.git?ref=main
  - https://github.com/org/pinned//x?ref=` + sha + `
  - https://raw.githubusercontent.com/org/repo/main/crd.yaml
components:
  - git::https://git.example.com/org/comp//c?ref=v3
images:
  - name: nginx
    newTag: 1.27.3
  - name: app
    newName: ghcr.io/org/app
    newTag: "v2.1.0"
  - name: redis
    newTag: 7.4@` + digestA + `
  - name: postgres
    newTag: '16.4'
    digest: ` + digestB + `
  - name: busybox
    digest: ` + digestA + `
  - name: rename
    newName: example.com/renamed
  - name: templ
    newTag: ${TAG}
helmCharts:
  - name: cert-manager
    repo: https://charts.jetstack.io/
    version: v1.16.1
    releaseName: cm
  - name: podinfo
    repo: oci://ghcr.io/stefanprodan/charts
    version: 6.7.0
  - name: local
    version: 1.0.0
  - name: loose
    repo: https://charts.example.com
    version: ~1.2.0
  - name: latest
    repo: https://charts.example.com
  - name: alias
    repo: "@stable"
    version: 1.0.0
`

// want is the expected outcome of one dep; span is the text under RefSpan, digestSpan under
// DigestSpan.
type want struct {
	depType, name, source, version, digest, span, digestSpan, skip string
	line                                                           int
}

func TestExtract(t *testing.T) {
	m := New()
	got, err := m.Extract("overlays/prod/kustomization.yaml", []byte(kustomization), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	ws := []want{
		{TypeImage, "nginx", "docker.io/library/nginx", "1.27.3", "", "1.27.3", "", "", 18},
		{TypeImage, "ghcr.io/org/app", "ghcr.io/org/app", "v2.1.0", "", "v2.1.0", "", "", 21},
		{TypeImage, "redis", "docker.io/library/redis", "7.4", digestA, "7.4", digestA, "", 23},
		{TypeImage, "postgres", "docker.io/library/postgres", "16.4", digestB, "16.4", digestB, "", 25},
		{TypeImage, "busybox", "docker.io/library/busybox", "latest", digestA, "", digestA, "", 28},
		{TypeImage, "example.com/renamed", "example.com/renamed", "latest", "", "", "", "", 29},
		{TypeImage, "templ", "", "", "", "", "", "variable reference", 31},
		{TypeHelmChart, "cert-manager", "https://charts.jetstack.io", "v1.16.1", "", "v1.16.1", "", "", 36},
		{TypeHelmChart, "podinfo", "oci://ghcr.io/stefanprodan/charts", "6.7.0", "", "6.7.0", "", "", 40},
		{TypeHelmChart, "local", "", "1.0.0", "", "", "", "no repo", 42},
		{TypeHelmChart, "loose", "https://charts.example.com", "~1.2.0", "", "", "", "version range", 45},
		{TypeHelmChart, "latest", "https://charts.example.com", "", "", "", "", "no version", 46},
		{TypeHelmChart, "alias", "@stable", "1.0.0", "", "", "", "unknown chart repository @stable", 50},
		{TypeResource, "github.com/kubernetes-sigs/kustomize", "https://github.com/kubernetes-sigs/kustomize", "v5.4.0", "", "v5.4.0", "", "", 6},
		{TypeResource, "github.com/org/repo", "https://github.com/org/repo", "v1", "", "v1", "", "", 7},
		{TypeResource, "github.com/org/infra", "https://github.com/org/infra", "v1.2", "", "v1.2", "", "", 8},
		{TypeResource, "git.example.com/team/app", "https://git.example.com/team/app", "v0.3.1", "", "v0.3.1", "", "", 9},
		{TypeResource, "dev.azure.com/org/project/_git/repo", "https://dev.azure.com/org/project/_git/repo", "v2.0.0", "", "v2.0.0", "", "", 10},
		{TypeResource, "gitlab.com/group/repo", "https://gitlab.com/group/repo", "main", "", "", "", "non-version ref", 11},
		{TypeResource, "github.com/org/pinned", "https://github.com/org/pinned", "HEAD", sha, "", sha, "", 12},
		{TypeResource, "git.example.com/org/comp", "https://git.example.com/org/comp", "v3", "", "v3", "", "", 15},
	}
	if len(got) != len(ws) {
		t.Fatalf("got %d deps, want %d: %+v", len(got), len(ws), got)
	}
	for i, w := range ws {
		d := got[i]
		if d.Manager != Name || d.File != "overlays/prod/kustomization.yaml" || d.DepType != w.depType || d.Name != w.name ||
			d.Version != w.version || d.Digest != w.digest || d.Skip != w.skip || d.Line != w.line ||
			w.source != "" && d.Source != w.source {
			t.Errorf("%d: got %+v, want %+v", i, d, w)
			continue
		}
		dv := map[string][2]string{TypeImage: {"docker", "docker"}, TypeHelmChart: {"helm", "semver"}, TypeResource: {"git-tags", "tag"}}[w.depType]
		if w.version == defaultBranch {
			dv[0] = "git-refs"
		}
		ds, vs := dv[0], dv[1]
		if d.Datasource != ds || d.Versioning != vs {
			t.Errorf("%d: datasource/versioning = %s/%s, want %s/%s", i, d.Datasource, d.Versioning, ds, vs)
		}
		if w.skip != "" {
			continue
		}
		if s := kustomization[d.RefSpan.Start:d.RefSpan.End]; s != w.span {
			t.Errorf("%d: RefSpan text = %q, want %q", i, s, w.span)
		}
		if w.digestSpan != "" {
			if s := kustomization[d.DigestSpan.Start:d.DigestSpan.End]; s != w.digestSpan {
				t.Errorf("%d: DigestSpan text = %q, want %q", i, s, w.digestSpan)
			}
		}
	}
}

func TestMatch(t *testing.T) {
	m := New()
	for p, w := range map[string]bool{
		"kustomization.yaml":          true,
		"a/b/kustomization.yml":       true,
		"deploy/Kustomization":        true,
		"kustomization.yaml.bak":      false,
		"my-kustomization.yaml":       false,
		"deploy/kustomization.json":   false,
		"deploy/Kustomization.yaml":   false,
		"kustomization/deploy.yaml":   false,
		"overlays/dev/values.yaml":    false,
		"overlays/dev/Kustomizations": false,
	} {
		if m.Match(p) != w {
			t.Errorf("Match(%q) = %v, want %v", p, !w, w)
		}
	}
}

func TestExtractEdgeCases(t *testing.T) {
	m := New()
	for _, src := range []string{
		"",
		"- a\n",
		"resources: {}\nimages: x\nhelmCharts: [1]\n",
		"images: [{newTag: 1.0}, {name: ''}]\nhelmCharts: [{version: 1.0.0}]\n",
		"resources:\n  - ../base\n  - deploy.yaml\n  - ./x?ref=v1\n  - file:///tmp/r?ref=v1\n  - https://github.com/o/r?timeout=1s\n  - {a: b}\n",
	} {
		got, err := m.Extract("kustomization.yaml", []byte(src), manager.Env{})
		if err != nil || len(got) != 0 {
			t.Errorf("%q: got %+v, %v; want no deps", src, got, err)
		}
	}
	if _, err := m.Extract("kustomization.yaml", []byte("images: [\n"), manager.Env{}); err == nil {
		t.Error("broken kustomization accepted")
	}
	// Escaped and block scalars cannot be located; an invalid image reference is reported.
	src := "images:\n  - name: a\n    newTag: \"1.\\x30\"\n  - name: UPPER\n    newTag: 1.0\n  - name: t\n    newName: x:1\n  - name: d\n    digest: md5:x\n" +
		"helmCharts:\n  - name: c\n    repo: https://x\n    version: >-\n      1.0.0\n" +
		"resources:\n  - \"https://github.com/o/r?ref=v\\x31\"\n  - https://github.com//?ref=v1\n  - ?ref=v1\n  - https://github.com/o/r?ref=\n"
	got, err := m.Extract("kustomization.yaml", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	skips := []string{"cannot locate value", "invalid reference", "invalid reference", "invalid digest", "cannot locate value", "cannot locate value", "invalid repository URL", "no ref"}
	if len(got) != len(skips) {
		t.Fatalf("got %d deps, want %d: %+v", len(got), len(skips), got)
	}
	for i, s := range skips {
		if got[i].Skip != s {
			t.Errorf("%d: skip = %q, want %q (%+v)", i, got[i].Skip, s, got[i])
		}
	}
}

func TestParseRemote(t *testing.T) {
	for _, tt := range []struct {
		in, name, source, ref string
		ok                    bool
	}{
		{"https://github.com/o/r//p?ref=v1", "github.com/o/r", "https://github.com/o/r", "v1", true},
		{"http://git.local:8080/o/r.git?ref=v1", "git.local:8080/o/r", "http://git.local:8080/o/r", "v1", true},
		{"ssh://git@github.com/o/r.git/sub?ref=v1", "github.com/o/r", "https://github.com/o/r", "v1", true},
		{"git@gitlab.com:g/sub/r.git//p?ref=v1", "gitlab.com/g/sub/r", "https://gitlab.com/g/sub/r", "v1", true},
		{"github.com/o/r?version=v2&ref=v1", "github.com/o/r", "https://github.com/o/r", "v2", true},
		{"example.com/a/b/c?ref=v1", "example.com/a/b/c", "https://example.com/a/b/c", "v1", true},
		{"github.com/o/r", "", "", "", false},
		{"base?ref=v1", "", "", "", false},
		{"file:///r?ref=v1", "", "", "", false},
	} {
		r, ok := parseRemote(tt.in)
		if ok != tt.ok || r.name != tt.name || r.source != tt.source || r.ref != tt.ref {
			t.Errorf("parseRemote(%q) = %+v, %v; want %s %s %s %v", tt.in, r, ok, tt.name, tt.source, tt.ref, tt.ok)
			continue
		}
		if ok && tt.in[r.refOff:r.refOff+len(r.ref)] != r.ref {
			t.Errorf("parseRemote(%q): refOff %d does not point at %q", tt.in, r.refOff, r.ref)
		}
	}
}

func TestEdits(t *testing.T) {
	m := New()
	deps, err := m.Extract("kustomization.yaml", []byte(kustomization), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]dep.Dep{}
	for _, d := range deps {
		byName[d.Name] = d
	}
	for _, tt := range []struct {
		name, newVersion, newDigest, old, new string
	}{
		{"nginx", "1.28.0", "", "    newTag: 1.27.3\n", "    newTag: 1.28.0\n"},
		{"ghcr.io/org/app", "v2.2.0", "", `    newTag: "v2.1.0"` + "\n", `    newTag: "v2.2.0"` + "\n"},
		{"redis", "7.4", digestB, "    newTag: 7.4@" + digestA + "\n", "    newTag: 7.4@" + digestB + "\n"},
		{"redis", "7.6", digestB, "    newTag: 7.4@" + digestA + "\n", "    newTag: 7.6@" + digestB + "\n"},
		{"postgres", "17.0", digestA, "    newTag: '16.4'\n    digest: " + digestB + "\n", "    newTag: '17.0'\n    digest: " + digestA + "\n"},
		{"cert-manager", "v1.17.0", "", "    version: v1.16.1\n", "    version: v1.17.0\n"},
		{"podinfo", "6.8.0", "", "    version: 6.7.0\n", "    version: 6.8.0\n"},
		{"github.com/org/infra", "v1.3", "", "git@github.com:org/infra.git//apps/x?ref=v1.2&timeout=2m", "git@github.com:org/infra.git//apps/x?ref=v1.3&timeout=2m"},
		{"git.example.com/team/app", "v0.4.0", "", "k8s?submodules=false&ref=v0.3.1\"", "k8s?submodules=false&ref=v0.4.0\""},
		{"dev.azure.com/org/project/_git/repo", "v2.1.0", "", "path?version=v2.0.0", "path?version=v2.1.0"},
		{"nginx", "1.27.3", digestA, "    newTag: 1.27.3\n", "    newTag: 1.27.3@" + digestA + "\n"},
		{"nginx", "1.28.0", digestA, "    newTag: 1.27.3\n", "    newTag: 1.28.0@" + digestA + "\n"},
		{"ghcr.io/org/app", "v2.1.0", digestB, `    newTag: "v2.1.0"` + "\n", `    newTag: "v2.1.0@` + digestB + `"` + "\n"},
		{"busybox", "latest", digestB, "    digest: " + digestA + "\n  - name: rename", "    digest: " + digestB + "\n  - name: rename"},
		{"github.com/org/pinned", defaultBranch, "fedcba9876543210fedcba9876543210fedcba98", "x?ref=" + sha, "x?ref=fedcba9876543210fedcba9876543210fedcba98"},
	} {
		d, ok := byName[tt.name]
		if !ok {
			t.Fatalf("no dep %s", tt.name)
		}
		edits, err := m.Edits(dep.Update{Dep: d, NewVersion: tt.newVersion, NewDigest: tt.newDigest})
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		out, err := patch.Apply([]byte(kustomization), edits)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		want := replaceOnce(t, kustomization, tt.old, tt.new)
		if string(out) != want {
			t.Errorf("%s: got\n%s\nwant\n%s", tt.name, out, want)
		}
	}
	if _, err := m.Edits(dep.Update{Dep: byName["redis"], NewVersion: "7.6"}); err == nil {
		t.Error("unpinning a digest: no error")
	}
	if _, err := m.Edits(dep.Update{Dep: byName["example.com/renamed"], NewVersion: "latest", NewDigest: digestA}); err == nil {
		t.Error("pinning an image without newTag: no error")
	}
	if _, err := m.Edits(dep.Update{Dep: byName["example.com/renamed"], NewVersion: "1.0"}); err == nil {
		t.Error("setting a tag of an image without newTag: no error")
	}
	if !Pinnable(byName["nginx"]) || Pinnable(byName["redis"]) || Pinnable(byName["example.com/renamed"]) || Pinnable(byName["github.com/org/pinned"]) {
		t.Error("Pinnable: want only the tagged image without digest")
	}
	if !Untagged(byName["example.com/renamed"]) || !Untagged(byName["busybox"]) || Untagged(byName["nginx"]) {
		t.Error("Untagged: want only the images without newTag")
	}
}

func TestNameParts(t *testing.T) {
	deps, err := New().Extract("kustomization.yaml", []byte(kustomization), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]dep.NamePart{
		"nginx":               {Text: "  - name: nginx", Role: roleNameLine},
		"ghcr.io/org/app":     {Text: "ghcr.io/org/app"},
		"redis":               {Text: "  - name: redis", Role: roleNameLine},
		"postgres":            {Text: "  - name: postgres", Role: roleNameLine},
		"busybox":             {Text: "  - name: busybox", Role: roleNameLine},
		"example.com/renamed": {Text: "example.com/renamed"},
	}
	for _, d := range deps {
		w, ok := want[d.Name]
		switch {
		case d.DepType == TypeHelmChart && d.Skip == "":
			if len(d.NameParts) != 2 || d.NameParts[0].Text != d.Name || d.NameParts[1].Role != dep.NameRepository {
				t.Errorf("%s: NameParts = %+v, want chart and repository", d.Name, d.NameParts)
			}
			continue
		case !ok:
			if d.NameParts != nil {
				t.Errorf("%s: NameParts = %+v, want none", d.Name, d.NameParts)
			}
			continue
		}
		if len(d.NameParts) != 1 || d.NameParts[0].Text != w.Text || d.NameParts[0].Role != w.Role {
			t.Errorf("%s: NameParts = %+v, want %+v", d.Name, d.NameParts, w)
			continue
		}
		if p := d.NameParts[0]; kustomization[p.Span.Start:p.Span.End] != p.Text {
			t.Errorf("%s: span text %q, want %q", d.Name, kustomization[p.Span.Start:p.Span.End], p.Text)
		}
	}
	// Flow mappings, an empty newName key and a name off its key's line are not renamable.
	for _, src := range []string{
		"images: [{name: a, newTag: '1.0'}]\n",
		"images:\n  - {name: a, newTag: '1.0'}\n",
		"images:\n  - name: a\n    newName: ''\n    newTag: '1.0'\n",
		"images:\n  - name:\n      a\n    newTag: '1.0'\n",
	} {
		deps, err := New().Extract("kustomization.yaml", []byte(src), manager.Env{})
		if err != nil || len(deps) != 1 || deps[0].Skip != "" || deps[0].Renamable() {
			t.Errorf("%q: got %+v, %v; want one updatable, not renamable dep", src, deps, err)
		}
	}
}

// A registryAliases prefix resolves tagged and untagged images; aliased images keep the alias
// as written and are not renamable.
func TestExtractRegistryAliases(t *testing.T) {
	const src = "images:\n  - name: $CI_REGISTRY/group/app\n    newTag: '1.2'\n  - name: $CI_REGISTRY/group/db\n  - name: $OTHER/x\n    newTag: '1'\n"
	deps, err := New().Extract("kustomization.yaml", []byte(src), manager.Env{RegistryAliases: map[string]string{"$CI_REGISTRY": "registry.gitlab.com"}})
	if err != nil || len(deps) != 3 {
		t.Fatalf("got %+v, %v; want 3 deps", deps, err)
	}
	for i, w := range []struct{ name, version, skip string }{
		{"registry.gitlab.com/group/app", "1.2", ""},
		{"registry.gitlab.com/group/db", untaggedVersion, ""},
		{"$OTHER/x", "", "variable reference"},
	} {
		d := deps[i]
		if d.Name != w.name || d.Source != w.name && w.skip == "" || d.Version != w.version || d.Skip != w.skip || d.Renamable() {
			t.Errorf("%d: got name=%q source=%q version=%q skip=%q renamable=%v, want %+v", i, d.Name, d.Source, d.Version, d.Skip, d.Renamable(), w)
		}
	}
	if !Untagged(deps[1]) {
		t.Error("untagged aliased image: Untagged = false")
	}
}

func TestRenameEdits(t *testing.T) {
	m := New()
	for _, tt := range []struct {
		name, src, newName, newVersion, newDigest, want string
	}{
		{"newName", "images:\n  - name: app\n    newName: ghcr.io/org/app\n    newTag: v1\n", "ghcr.io/org/new", "v1", "",
			"images:\n  - name: app\n    newName: ghcr.io/org/new\n    newTag: v1\n"},
		{"quoted newName", "images:\n  - name: app\n    newName: \"ghcr.io/org/app\"\n    newTag: 'v1'\n", "ghcr.io/org/new", "v2", "",
			"images:\n  - name: app\n    newName: \"ghcr.io/org/new\"\n    newTag: 'v2'\n"},
		{"insert", "images:\n  - name: nginx\n    newTag: 1.27.3\n  - name: x\n", "cgr.dev/chainguard/nginx", "1.28.0", "",
			"images:\n  - name: nginx\n    newName: cgr.dev/chainguard/nginx\n    newTag: 1.28.0\n  - name: x\n"},
		{"insert after quoted name", "images:\n- newTag: \"1.0\"\n  name: 'nginx' # web\n", "ghcr.io/org/nginx", "1.0", "",
			"images:\n- newTag: \"1.0\"\n  name: 'nginx' # web\n  newName: ghcr.io/org/nginx\n"},
		{"insert at end of file", "images:\n    - newTag: v1\n      name: a", "b", "v1", "",
			"images:\n    - newTag: v1\n      name: a\n      newName: b"},
		{"insert before CRLF", "images:\r\n  - name: a\r\n    newTag: v1\r\n", "b", "v1", "",
			"images:\r\n  - name: a\n    newName: b\r\n    newTag: v1\r\n"},
		{"insert with tag digest", "images:\n  - name: redis\n    newTag: 7.4@" + digestA + "\n", "valkey/valkey", "8.0", digestB,
			"images:\n  - name: redis\n    newName: valkey/valkey\n    newTag: 8.0@" + digestB + "\n"},
		{"newName with digest field", "images:\n  - name: pg\n    newName: postgres\n    newTag: '16.4'\n    digest: " + digestA + "\n", "ghcr.io/org/pg", "17.0", digestB,
			"images:\n  - name: pg\n    newName: ghcr.io/org/pg\n    newTag: '17.0'\n    digest: " + digestB + "\n"},
	} {
		deps, err := m.Extract("kustomization.yaml", []byte(tt.src), manager.Env{})
		if err != nil || len(deps) == 0 {
			t.Fatalf("%s: %+v, %v", tt.name, deps, err)
		}
		d, err := m.(manager.Renamer).Rename(deps[0], tt.newName)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		edits, err := m.Edits(dep.Update{Dep: deps[0], NewName: tt.newName, NewVersion: tt.newVersion, NewDigest: tt.newDigest})
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		out, err := patch.Apply([]byte(tt.src), edits)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if string(out) != tt.want {
			t.Errorf("%s: got\n%q\nwant\n%q", tt.name, out, tt.want)
			continue
		}
		// The result extracts as the renamed image.
		again, err := m.Extract("kustomization.yaml", out, manager.Env{})
		if err != nil || len(again) == 0 || again[0].Name != d.Name || again[0].Source != d.Source || again[0].Skip != "" {
			t.Errorf("%s: re-extracted %+v, %v; want %s", tt.name, again, err, d.Name)
		}
	}
}

func TestRename(t *testing.T) {
	r := New().(manager.Renamer)
	d := dep.Dep{Manager: Name, Datasource: "docker", Name: "nginx", Source: "docker.io/library/nginx"}
	for name, source := range map[string]string{
		"ghcr.io/org/nginx": "ghcr.io/org/nginx",
		"bitnami/nginx":     "docker.io/bitnami/nginx",
	} {
		got, err := r.Rename(d, name)
		if err != nil || got.Name != name || got.Source != source {
			t.Errorf("Rename(%q) = %s %s, %v; want %s %s", name, got.Name, got.Source, err, name, source)
		}
	}
	for _, name := range []string{"UPPER", "nginx:1.0", "nginx@" + digestA, ""} {
		if _, err := r.Rename(d, name); err == nil {
			t.Errorf("Rename(%q): no error", name)
		}
	}
}

func TestRenameChart(t *testing.T) {
	m := New()
	const src = "helmCharts:\n  - name: redis\n    repo: https://charts.bitnami.com/bitnami/\n    version: 20.1.0\n"
	deps, err := m.Extract("kustomization.yaml", []byte(src), manager.Env{})
	if err != nil || len(deps) != 1 || !deps[0].Renamable() {
		t.Fatalf("got %+v, %v; want one renamable chart", deps, err)
	}
	for _, tt := range []struct{ newName, source, want string }{
		{"valkey", "https://charts.bitnami.com/bitnami", "helmCharts:\n  - name: valkey\n    repo: https://charts.bitnami.com/bitnami/\n    version: 20.2.0\n"},
		{"oci://registry-1.docker.io/bitnamicharts/valkey", "oci://registry-1.docker.io/bitnamicharts",
			"helmCharts:\n  - name: valkey\n    repo: oci://registry-1.docker.io/bitnamicharts\n    version: 20.2.0\n"},
	} {
		d, err := m.(manager.Renamer).Rename(deps[0], tt.newName)
		if err != nil || d.Name != "valkey" || d.Source != tt.source {
			t.Fatalf("Rename(%q) = %s %s, %v", tt.newName, d.Name, d.Source, err)
		}
		edits, err := m.Edits(dep.Update{Dep: deps[0], NewName: tt.newName, NewVersion: "20.2.0"})
		if err != nil {
			t.Fatalf("%s: %v", tt.newName, err)
		}
		out, err := patch.Apply([]byte(src), edits)
		if err != nil || string(out) != tt.want {
			t.Errorf("%s: got\n%s\n%v; want\n%s", tt.newName, out, err, tt.want)
		}
	}
	if _, err := m.(manager.Renamer).Rename(deps[0], "Not a chart!"); err == nil {
		t.Error("invalid chart name accepted")
	}
}

func replaceOnce(t *testing.T, s, from, to string) string {
	t.Helper()
	i := indexOnce(t, s, from)
	return s[:i] + to + s[i+len(from):]
}

func indexOnce(t *testing.T, s, sub string) int {
	t.Helper()
	first, n := -1, 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			if first < 0 {
				first = i
			}
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%q occurs %d times", sub, n)
	}
	return first
}
