package terraform

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// JSON fixtures, copied unchanged:
//   - testdata/cztack: github.com/chanzuckerberg/cztack@7efb4b1 (MIT):
//     snowflake-user-grant-all/main.tf.json.
//   - testdata/cue-for-devops: github.com/ptMcGit/cue-for-devops@eb4f753 (MIT):
//     projects/saturn/terraform/main.tf.json (a top-level array of bodies).
//   - testdata/cdktf-hybrid: github.com/DanielMSchmidt/terraform-cdk-hybrid-module-publishing-on-gh-packages@adf35a0
//     (Apache-2.0): modules/s3-bucket-trigger-on-file-create/cdk.tf.json (CDK for Terraform output).

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		file string
		want []string
	}{
		{"cztack/main.tf.json", []string{
			// The constraint is written with \u003e escapes.
			"50 required_provider terraform-provider Snowflake-Labs/snowflake https://registry.terraform.io >= 0.20.0, < 1.0.0 - [cannot locate value]",
		}},
		{"cue-for-devops/main.tf.json", []string{
			"18 required_version github-releases hashicorp/terraform https://github.com/hashicorp/terraform 1.2.2 hashicorp",
			"13 required_provider terraform-provider hashicorp/aws https://registry.terraform.io 4.17.1 hashicorp",
			"36 module - ../../../terraform_modules/aws/subnet_public - - - [local module]",
		}},
		{"cdktf-hybrid/cdk.tf.json", []string{
			"34 module terraform-module terraform-aws-modules/s3-bucket/aws https://registry.terraform.io - - [no version constraint]",
			"54 module terraform-module terraform-aws-modules/s3-bucket/aws https://registry.terraform.io - - [no version constraint]",
			"59 required_provider terraform-provider hashicorp/aws https://registry.terraform.io 4.12.1 hashicorp",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			got := extract(t, New(), tt.file, read(t, tt.file))
			if !slices.Equal(got, tt.want) {
				t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestMatchJSON(t *testing.T) {
	for p, want := range map[string]bool{
		"main.tf.json":                   true,
		"infra/cdk.tf.json":              true,
		"main.tofu.json":                 true,
		"main.json":                      false,
		"main.tf.jsonc":                  false,
		".terraform/modules/x/a.tf.json": false,
		"a/.terraform/b.tf.json":         false,
	} {
		if got := New().Match(p); got != want {
			t.Errorf("Match(%q) = %v, want %v", p, got, want)
		}
	}
}

// dumpBody renders b as one line per attr ("a <name>@<off> <value>") and block
// ("b <type> <labels> [<span>] {" ... "}").
func dumpBody(b body, indent string, out *[]string) {
	for _, a := range b.attrs {
		*out = append(*out, fmt.Sprintf("%sa %s@%d %s", indent, a.name, a.off, dumpValue(a.val)))
	}
	for _, blk := range b.blocks {
		*out = append(*out, fmt.Sprintf("%sb %s %v %d-%d {", indent, blk.typ, blk.labels, blk.span.Start, blk.span.End))
		dumpBody(blk.body, indent+"  ", out)
		*out = append(*out, indent+"}")
	}
}

func dumpValue(v value) string {
	switch v.kind {
	case kindString:
		return fmt.Sprintf("%q%v", v.str, v.span)
	case kindObject:
		parts := make([]string, 0, len(v.obj))
		for _, a := range v.obj {
			parts = append(parts, fmt.Sprintf("%s@%d=%s", a.name, a.off, dumpValue(a.val)))
		}
		return "{" + strings.Join(parts, " ") + "}"
	}
	return "other"
}

func TestParseJSON(t *testing.T) {
	tests := []struct {
		name, src string
		want      []string
	}{
		{"labels", `{"resource":{"t":{"n":{"x":"y"}}},"module":{"m":{"source":"s"}},"locals":{"l":1}}`, []string{
			"b resource [t n] 1-31 {", `  a x@23 "y"{28 29}`, "}",
			"b module [m] 34-62 {", `  a source@49 "s"{59 60}`, "}",
			"b locals [] 64-80 {", "  a l@74 other", "}",
		}},
		{"arrays", `{"provider":{"aws":[{"region":"a"},{"alias":"b"}]},"terraform":[{"x":1},{"y":2}]}`, []string{
			"b provider [aws] 1-34 {", `  a region@21 "a"{31 32}`, "}",
			"b provider [aws] 1-48 {", `  a alias@36 "b"{45 46}`, "}",
			"b terraform [] 51-71 {", "  a x@65 other", "}",
			"b terraform [] 51-79 {", "  a y@73 other", "}",
		}},
		{"nested", `{"terraform":{"required_providers":{"aws":{"source":"hashicorp/aws"}},"c":[{"d":"e"}],"f":["g"]}}`, []string{
			"b terraform [] 1-96 {",
			`  a required_providers@14 {aws@36={source@43="hashicorp/aws"{53 66}}}`,
			"  a c@70 other",
			"  a f@86 other",
			"  b required_providers [] 14-69 {",
			`    a aws@36 {source@43="hashicorp/aws"{53 66}}`,
			"    b aws [] 36-68 {",
			`      a source@43 "hashicorp/aws"{53 66}`,
			"    }",
			"  }",
			"  b c [] 70-84 {",
			`    a d@76 "e"{81 82}`,
			"  }",
			"}",
		}},
		{"strings", `{"locals":{"esc":"a\u003e","tpl":"${var.x}","dir":"%{if x}y%{endif}","lit":"$${x}%%{y}","dollar":"$5 {"}}`, []string{
			"b locals [] 1-104 {",
			`  a esc@11 "a>"{0 0}`,
			"  a tpl@27 other",
			"  a dir@44 other",
			`  a lit@69 "${x}%{y}"{0 0}`,
			`  a dollar@88 "$5 {"{98 102}`,
			"}",
		}},
		{"comments", `{"//":"top","module":{"//":"c","m":{"//":{"x":1},"source":"s"}}}`, []string{
			"b module [m] 12-62 {", `  a source@49 "s"{59 60}`, "}",
		}},
		{"root array", `[{"module":{"a":{}}},{"module":{"b":{}}}]`, []string{
			"b module [a] 2-18 {", "}",
			"b module [b] 22-38 {", "}",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := parseJSON([]byte(tt.src))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			dumpBody(b, "", &got)
			if !slices.Equal(got, tt.want) {
				t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestParseJSONSpans(t *testing.T) {
	src := []byte(`{"module":{"m":{"source":"git::https://h/r.git?ref=v1.2.3"}}}`)
	b, err := parseJSON(src)
	if err != nil {
		t.Fatal(err)
	}
	v := b.blocks[0].body.attrs[0].val
	if got := string(src[v.span.Start:v.span.End]); got != v.str {
		t.Errorf("span covers %q, want %q", got, v.str)
	}
	if v.span == (dep.Span{}) {
		t.Error("zero span")
	}
}

func TestParseJSONErrors(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"empty", ``, "line 1: "},
		{"truncated", "{\n\"module\": {\n", "line 2: "},
		{"invalid", "{\n\"a\": tru}", "line 2: "},
		{"trailing", `{} {}`, "line 1: "},
		{"not object", "\n\"x\"", "line 2: top level is not an object"},
		{"array of scalars", `[1]`, "line 1: top level is not an object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseJSON([]byte(tt.src))
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Errorf("err = %v, want prefix %q", err, tt.want)
			}
		})
	}
}
