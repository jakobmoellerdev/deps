package nix

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

func renderBindings(src string) []string {
	var out []string
	for _, b := range parseInputs([]byte(src)) {
		s := fmt.Sprintf("%d %s=%q", b.line, strings.Join(b.path, "."), b.value)
		if b.value != "" && src[b.span.Start:b.span.End] != b.value {
			s += " bad span"
		}
		out = append(out, s)
	}
	return out
}

func TestParseInputs(t *testing.T) {
	for _, c := range []struct {
		name, src string
		want      []string
	}{
		{"not an attribute set", `let x = 1; in { inputs.a.url = "a"; }`, nil},
		{"strings and comments", "{\n  # inputs.c.url = \"c\";\n  inputs.a.url = \"$${a}\"; /* inputs.b.url = \"b\"; */\n  inputs.b.url = ''b'';\n  inputs.c.url = ''c\n'';\n" +
			"  inputs.d.url = ''d''${x}'';\n  inputs.e.url = ''a'''b'';\n  inputs.f.url = \"f${x}\";\n}",
			[]string{`3 a.url="$${a}"`, `4 b.url="b"`, `5 c.url=""`, `7 d.url=""`, `8 e.url=""`, `9 f.url=""`}},
		{"nested sets, quoted and dynamic names", `{ inputs = { "a b".url = "x"; ${"c"}.url = "y"; d = { url = "z"; inputs.e.follows = "a b"; }; }; }`,
			[]string{`1 a b.url="x"`, `1 .url="y"`, `1 d.url="z"`, `1 d.inputs.e.follows="a b"`}},
		{"skipped expressions", `{ inherit (x) inputs; inputs.a = import ./a.nix; inputs.b.url = "b" + "c"; outputs = { self }: let a = 1; in with a; assert true; rec { b = [ (1) ]; }; inputs.c.flake = 0.5; inputs.d.url = github:acme/d; }`,
			[]string{`1 d.url="github:acme/d"`}},
		{"malformed", `{ inputs.a.url "x"; inputs.$b = 1; inputs."c".url = "c"; inputs.d.url = ) ; inputs.e.url = "e" }`, []string{`1 c.url="c"`}},
		{"unterminated string", `{ inputs.a.url = "abc`, nil},
		{"unterminated indented string", `{ inputs.a.url = ''abc`, nil},
		{"unterminated comment", `{ inputs.a.url = "a"; /* inputs.b.url = "b";`, []string{`1 a.url="a"`}},
		{"unterminated interpolation", `{ inputs.a.url = "${x`, nil},
		{"comment at the end", "{ inputs.a.url = x:y; } # end", []string{`1 a.url="x:y"`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := renderBindings(c.src); !slices.Equal(got, c.want) {
				t.Errorf("bindings = %q, want %q", got, c.want)
			}
		})
	}
}

func TestParseRef(t *testing.T) {
	rev := strings.Repeat("a", 40)
	for _, c := range []struct{ url, want string }{
		{"github:acme/x", " - | -"},
		{"github:acme/x/main?dir=sub", "main 14-18 | -"},
		{"github:acme/x/" + rev, " - | " + rev + " 14-54"},
		{"sourcehut:~u/x/v1.0", "v1.0 15-19 | -"},
		{"git+https://e.com/a/x?ref=refs%2Fheads%2Fmain&rev=" + rev, "refs%2Fheads%2Fmain - | " + rev + " 50-90"},
		{"https://e.com/a/x/archive/" + rev + ".tar.gz", " - | " + rev + " 26-66"},
		{"https://e.com/a/x/archive/.tar.gz", " - | -"},
		{"nixpkgs", " - | -"},
	} {
		r := parseRef(c.url, 0)
		span := func(v string, s [2]int) string {
			if s == [2]int{} {
				return v + " -"
			}
			return fmt.Sprintf("%s %d-%d", v, s[0], s[1])
		}
		got := span(r.ref, [2]int{r.refSpan.Start, r.refSpan.End}) + " | " + span(r.rev, [2]int{r.revSpan.Start, r.revSpan.End})
		if r.rev == "" {
			got = span(r.ref, [2]int{r.refSpan.Start, r.refSpan.End}) + " | -"
		}
		if got != c.want {
			t.Errorf("parseRef(%q) = %q, want %q", c.url, got, c.want)
		}
	}
	if r := parseRef("github:acme/x/v1", -1); r.ref != "v1" || r.refSpan.End != 0 {
		t.Errorf("not in the file: %+v", r)
	}
}

// FuzzExtract: any input terminates, and every span Extract reports holds the dep's ref or rev.
func FuzzExtract(f *testing.F) {
	seeds, err := filepath.Glob("testdata/*/flake.nix")
	if err != nil {
		f.Fatal(err)
	}
	for _, s := range seeds {
		b, err := os.ReadFile(s)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		deps, err := New().Extract("flake.nix", src, manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range deps {
			for _, c := range []struct {
				span dep.Span
				want string
			}{{d.RefSpan, d.Version}, {d.DigestSpan, d.Digest}} {
				if c.span != (dep.Span{}) && (c.span.End > len(src) || string(src[c.span.Start:c.span.End]) != c.want) {
					t.Fatalf("%s: span %v does not hold %q", d.Name, c.span, c.want)
				}
			}
		}
	})
}
