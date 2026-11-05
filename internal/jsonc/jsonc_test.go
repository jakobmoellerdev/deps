package jsonc

import (
	"encoding/json"
	"testing"
)

func TestStrip(t *testing.T) {
	for src, want := range map[string]string{
		// Trailing commas before } and ], also across whitespace and comments.
		`{"a": "x,}\"", "b": [1, 2,  ], "c": {"d": 1,
},}`: `{"a": "x,}\"", "b": [1, 2   ], "c": {"d": 1 
} }`,
		"{\"a\": 1, // one\n\"b\": \"//x\" /* c\nd */,}": "{\"a\": 1,       \n\"b\": \"//x\"     \n     }",
		"\ufeff{}":              "   {}",
		"{} // end":             "{}       ",
		"{\"a\": \"\\\\\"/**/}": "{\"a\": \"\\\\\"    }",
		"{/* open":              "{       ",
		`{"a": "unterminated`:   `{"a": "unterminated`,
	} {
		got := string(Strip([]byte(src)))
		if got != want {
			t.Errorf("Strip(%q) = %q, want %q", src, got, want)
		}
		if len(got) != len(src) {
			t.Errorf("Strip(%q) changed the length", src)
		}
	}
}

func TestStripDecodes(t *testing.T) {
	src := `// devcontainer
{
	"image": "mcr.microsoft.com/devcontainers/base:ubuntu", // base
	/* features */
	"features": {"ghcr.io/devcontainers/features/node:1": {},},
}`
	var v struct {
		Image    string         `json:"image"`
		Features map[string]any `json:"features"`
	}
	if err := json.Unmarshal(Strip([]byte(src)), &v); err != nil || v.Image != "mcr.microsoft.com/devcontainers/base:ubuntu" || len(v.Features) != 1 {
		t.Fatalf("decoded %+v, %v", v, err)
	}
}
