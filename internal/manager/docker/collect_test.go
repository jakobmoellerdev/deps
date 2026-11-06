package docker

import (
	"testing"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

func TestImages(t *testing.T) {
	src := []byte(`a: &img golang:1.26
b: *img
c: docker://alpine:3.22
d: ${IMAGE}
e: "multi\
  line:1"
f: [x]
g: ""
h: docker://
i: redis@` + sha1 + `
j: $REG/team/app:1.0
`)
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		t.Fatal(err)
	}
	root := doc.Content[0]
	c := Images{Manager: "m", File: "f.yml", Src: src, Lines: yamlspan.LineStarts(src), Aliases: map[string]string{"$REG": "registry.example.com/"}}
	for _, k := range []string{"a", "b", "d", "e", "f", "g", "i", "j"} {
		c.Add("image", yamlspan.Get(root, k))
	}
	c.Add("image", nil)
	c.AddPrefixed("pipe", yamlspan.Get(root, "c"), "docker://")
	c.AddPrefixed("pipe", yamlspan.Get(root, "h"), "docker://")
	c.AddPrefixed("pipe", yamlspan.Get(root, "a"), "docker://") // seen, and without the prefix anyway
	type w struct {
		line                      int
		depT, name, version, skip string
	}
	wants := []w{
		{1, "image", "golang", "1.26", ""},
		{4, "image", "${IMAGE}", "", "variable reference"},
		{5, "image", "multiline:1", "", "cannot locate value"},
		{10, "image", "redis", "latest", ""},
		{11, "image", "registry.example.com/team/app", "1.0", ""},
		{3, "pipe", "alpine", "3.22", ""},
	}
	if len(c.Deps) != len(wants) {
		t.Fatalf("got %d deps: %+v", len(c.Deps), c.Deps)
	}
	for i, x := range wants {
		d := c.Deps[i]
		if d.Manager != "m" || d.File != "f.yml" || d.Datasource != "docker" || d.Line != x.line || d.DepType != x.depT ||
			d.Name != x.name || d.Version != x.version || d.Skip != x.skip {
			t.Errorf("%d: got %+v, want %+v", i, d, x)
		}
		if d.Version != "" {
			want := d.Version
			if Untagged(d) {
				want = ""
			}
			if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != want {
				t.Errorf("%d: src[RefSpan] = %q", i, got)
			}
			if len(d.NameParts) == 0 {
				continue // aliased: the name as written is not the dep's
			}
			if got := string(src[d.NameParts[0].Span.Start:d.NameParts[0].Span.End]); got != d.Name {
				t.Errorf("%d: src[NamePart] = %q", i, got)
			}
		}
	}
}

// Azure Pipelines container resources (`containers` items with a `container` key) belong to
// the azure-pipelines manager; Kubernetes containers in the same file shape are still images.
func TestManifestsSkipAzureContainerResources(t *testing.T) {
	src := `resources:
  containers:
    - container: linux
      image: ubuntu:24.04
spec:
  containers:
    - name: app
      image: nginx:1.27
`
	check(t, []byte(src), extract(t, "ci/build.yml", src), []want{
		{line: 8, name: "nginx", source: "docker.io/library/nginx", version: "1.27", depT: TypeKubernetes, insert: "\n"},
	})
}
