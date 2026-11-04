package imageref

import (
	"strings"
	"testing"
)

const dg = "sha256:0123456789012345678901234567890123456789012345678901234567890123"

func TestParse(t *testing.T) {
	for ref, want := range map[string]Ref{
		"alpine":                     {Name: "docker.io/library/alpine", Familiar: "alpine"},
		"alpine:3.20":                {Name: "docker.io/library/alpine", Familiar: "alpine", Tag: "3.20"},
		"library/alpine":             {Name: "docker.io/library/alpine", Familiar: "alpine"},
		"library/a/b":                {Name: "docker.io/library/a/b", Familiar: "library/a/b"},
		"bitnami/redis:7":            {Name: "docker.io/bitnami/redis", Familiar: "bitnami/redis", Tag: "7"},
		"docker.io/alpine":           {Name: "docker.io/library/alpine", Familiar: "alpine"},
		"index.docker.io/org/img":    {Name: "docker.io/org/img", Familiar: "org/img"},
		"ghcr.io/org/img:v1":         {Name: "ghcr.io/org/img", Familiar: "ghcr.io/org/img", Tag: "v1"},
		"localhost/img":              {Name: "localhost/img", Familiar: "localhost/img"},
		"localhost:5000/img:1":       {Name: "localhost:5000/img", Familiar: "localhost:5000/img", Tag: "1"},
		"registry:5000/img":          {Name: "registry:5000/img", Familiar: "registry:5000/img"},
		"[::1]:5000/img":             {Name: "[::1]:5000/img", Familiar: "[::1]:5000/img"},
		"Reg.Example.com/img":        {Name: "Reg.Example.com/img", Familiar: "Reg.Example.com/img"},
		"Foo/bar":                    {Name: "Foo/bar", Familiar: "Foo/bar"}, // upper case: a registry
		"foo:5000":                   {Name: "docker.io/library/foo", Familiar: "foo", Tag: "5000"},
		"postgres@" + dg:             {Name: "docker.io/library/postgres", Familiar: "postgres", Digest: dg},
		"postgres:16@" + dg:          {Name: "docker.io/library/postgres", Familiar: "postgres", Tag: "16", Digest: dg},
		"ghcr.io/a/b:v1@" + dg:       {Name: "ghcr.io/a/b", Familiar: "ghcr.io/a/b", Tag: "v1", Digest: dg},
		"quay.io/a/b/c:x_y-1.0":      {Name: "quay.io/a/b/c", Familiar: "quay.io/a/b/c", Tag: "x_y-1.0"},
		"docker.io/library/alpine:1": {Name: "docker.io/library/alpine", Familiar: "alpine", Tag: "1"},
	} {
		got, err := Parse(ref)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", ref, got, err, want)
		}
	}
}

func TestParseInvalid(t *testing.T) {
	for _, ref := range []string{
		"", "Alpine", "a//b", "img:", "img@", "img:@" + dg, "alpine:-bad", "a.b/C",
		"oci://ghcr.io/a:1", "https://ghcr.io/a", "my_reg.io/a", "-reg.io/a", ".reg/a",
		"postgres@sha256:short",
	} {
		if r, err := Parse(ref); err == nil {
			t.Errorf("Parse(%q) = %+v, want an error", ref, r)
		} else if !strings.Contains(err.Error(), "invalid") {
			t.Errorf("Parse(%q) error %q does not say invalid", ref, err)
		}
	}
}
