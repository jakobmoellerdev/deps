package docker

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// manifestsWholeFile is manifests before it skipped documents without image keys, the oracle
// of the parity tests. It reports false when a document of x.src does not parse.
func (x *extractor) manifestsWholeFile() bool {
	dec := yaml.NewDecoder(bytes.NewReader(x.src))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return true
		}
		if err != nil {
			return false
		}
		if len(doc.Content) == 1 && isCompose(doc.Content[0]) {
			x.compose(doc.Content[0])
		} else {
			x.walk(&doc)
		}
	}
}

// checkManifestsParity compares manifests with the oracle on files that parse as a whole.
func checkManifestsParity(t *testing.T, file string, src []byte) {
	t.Helper()
	want := extractor{file: file, src: src, lines: yamlspan.LineStarts(src)}
	if !want.manifestsWholeFile() {
		return
	}
	got := extractor{file: file, src: src, lines: yamlspan.LineStarts(src)}
	got.manifests()
	if !reflect.DeepEqual(got.deps, want.deps) {
		t.Fatalf("%s: manifests = %+v\nwhole file = %+v", file, got.deps, want.deps)
	}
}

var keepDocsSeeds = []string{
	"apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nspec:\n  names: {kind: X}\n---\n" +
		"apiVersion: apps/v1\nkind: Deployment\nspec:\n  template:\n    spec:\n      containers:\n        - image: nginx:1.25\n",
	"kind: ConfigMap\ndata:\n  a: |\n    ---\n    b\n---\nimage:\n  repository: bitnami/redis\n  tag: 7.2.4\n",
	"services:\n  web:\n    image: &i nginx:1\n  db:\n    image: *i\n---\nkind: B\n",
	"%YAML 1.2\n---\ncontainers:\n- image: a:2\n...\n---\nkind: C\r\n",
	"kind: A\n--- |\n  text\n--- x\ncontainers: [{image: \"q:1\"}]\n",
	"sidecarImage:\n  registry: ghcr.io\n  repository: o/r\n  tag: v1\n---\nkind: D\n",
}

func TestManifestsKeepDocumentsParity(t *testing.T) {
	for i, s := range keepDocsSeeds {
		checkManifestsParity(t, "seed"+string(rune('0'+i))+".yaml", []byte(s))
	}
	root := filepath.Join("..", "..", "..")
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".yaml") && !strings.HasSuffix(p, ".yml") {
			return err
		}
		src, err := os.ReadFile(p)
		if err == nil {
			checkManifestsParity(t, p, src)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func FuzzManifestsKeepDocuments(f *testing.F) {
	for _, s := range keepDocsSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		checkManifestsParity(t, "f.yaml", []byte(src))
	})
}

// TestManifestsUnparsableDocuments pins which parse errors still drop a file's images: an
// error in a templated document does (Helm templates), one in a document without an image key
// (never parsed) no longer does.
func TestManifestsUnparsableDocuments(t *testing.T) {
	deployment := "containers:\n  - image: a:1\n"
	for src, want := range map[string]int{
		deployment + "---\n{{ bad\n":              0,
		deployment + "---\nkind: [unterminated\n": 1,
	} {
		deps, err := New().Extract("k8s/d.yaml", []byte(src), manager.Env{})
		if err != nil || len(deps) != want {
			t.Errorf("%q: %+v, %v; want %d deps", src, deps, err, want)
		}
	}
}

// BenchmarkManifestsKeepDocuments extracts an install manifest shaped like Argo CD's: large
// CustomResourceDefinitions without images, then a few Deployments.
func BenchmarkManifestsKeepDocuments(b *testing.B) {
	var src strings.Builder
	for i := range 20 {
		src.WriteString("---\napiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: x" +
			string(rune('a'+i)) + ".example.io\nspec:\n  versions:\n  - name: v1\n    schema:\n      openAPIV3Schema:\n        properties:\n")
		for j := range 200 {
			src.WriteString("          field" + string(rune('a'+j%26)) + string(rune('a'+j/26)) +
				":\n            description: A field of the resource.\n            type: string\n")
		}
	}
	for range 3 {
		src.WriteString("---\napiVersion: apps/v1\nkind: Deployment\nspec:\n  template:\n    spec:\n      containers:\n" +
			"      - name: app\n        image: quay.io/argoproj/argocd:v2.11.0\n")
	}
	data := []byte(src.String())
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		x := extractor{file: "install.yaml", src: data, lines: yamlspan.LineStarts(data)}
		x.manifests()
		if len(x.deps) != 3 {
			b.Fatalf("deps = %d", len(x.deps))
		}
	}
}
