package docgen

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const fakeAction = `name: tool
description: Run the tool
inputs:
  token:
    description: >-
      Token for the API. It needs write access, e.g. to push.
    default: "${{ github.token }}"
  args:
    description: "Arguments, e.g. ` + "`run -v`" + `."
    default: run
  target:
    description: Required target
    required: true
  empty:
    description: ""
outputs:
  result:
    description: The result
runs:
  using: composite
  steps:
    - name: Download
      run: echo
    - run: echo unnamed
    - name: Run
      run: echo
`

func TestAction(t *testing.T) {
	b, err := Action([]byte(fakeAction), "Intro *text*.", []ActionUsage{{Title: "Forge", Note: "A note.", Uses: "https://example.com/o/tool@v1"}})
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		Header + "\n# Action reference\n\n`tool`: Run the tool.\n\nIntro *text*.\n\nRuns as `composite` with the steps \"Download\", \"Run\".\n",
		"| `token` | no | `${{ github.token }}` | Token for the API. It needs write access, e.g. to push. |\n",
		"| `args` | no | `run` | Arguments, e.g. `run -v`. |\n",
		"| `target` | yes | none | Required target |\n",
		"| `empty` | no | none |  |\n",
		"## Outputs\n\n| Output | Description |\n|---|---|\n| `result` | The result |\n",
		"## Usage\n\n### Forge\n\nA note.\n\n```yaml\n- uses: https://example.com/o/tool@v1\n  with:\n    # Token for the API.\n    token: \"${{ github.token }}\"\n" +
			"    # Arguments, e.g. `run -v`.\n    args: run\n    # Required target\n    target: <target>\n    empty: \"\"\n```\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks\n%s\n--- output:\n%s", want, out)
		}
	}
	// The snippet is valid YAML that sets every input to its default.
	snippet := out[strings.Index(out, "```yaml\n")+8 : strings.LastIndex(out, "```")]
	var steps []struct {
		Uses string            `yaml:"uses"`
		With map[string]string `yaml:"with"`
	}
	if err := yaml.Unmarshal([]byte(snippet), &steps); err != nil {
		t.Fatalf("snippet: %v\n%s", err, snippet)
	}
	if w := steps[0].With; len(steps) != 1 || w["token"] != "${{ github.token }}" || w["args"] != "run" || w["target"] != "<target>" || w["empty"] != "" {
		t.Errorf("snippet values = %+v", steps)
	}
}

func TestActionWithoutInputs(t *testing.T) {
	b, err := Action([]byte("name: x\ndescription: d\n"), "", []ActionUsage{{Title: "U", Uses: "o/x@v1"}})
	if err != nil {
		t.Fatal(err)
	}
	if out := string(b); !strings.Contains(out, "## Inputs\n\nNone.\n") || !strings.Contains(out, "```yaml\n- uses: o/x@v1\n```") || strings.Contains(out, "Runs as") {
		t.Errorf("output:\n%s", out)
	}
}

func TestActionErrors(t *testing.T) {
	for src, want := range map[string]string{
		"name: [":                           "action.yml: yaml:",
		"- a":                               "not a mapping",
		"":                                  "not a mapping",
		"description: d":                    "no name",
		"name: x\ninputs: [a]":              "inputs: not a mapping",
		"name: x\noutputs: s":               "outputs: not a mapping",
		"name: x\ninputs:\n  a: {}\n  b: s": "", // a scalar input value is tolerated
	} {
		_, err := Action([]byte(src), "", nil)
		if want == "" {
			if err != nil {
				t.Errorf("%q: %v", src, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", src, err, want)
		}
	}
}

func TestFirstSentenceAndYAMLString(t *testing.T) {
	for in, want := range map[string]string{
		"One. Two.":                      "One.",
		"Tag, e.g. v1. Default: latest.": "Tag, e.g. v1.",
		"i.e. this. That.":               "i.e. this.",
		"no period":                      "no period",
		"multi\n  line. rest":            "multi line.",
	} {
		if got := firstSentence(in); got != want {
			t.Errorf("firstSentence(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"run":       "run",
		"":          `""`,
		"true":      `"true"`,
		"a: b":      `"a: b"`,
		"${{ x }}":  `"${{ x }}"`,
		`say "hi"`:  `"say \"hi\""`,
		"# comment": `"# comment"`,
	} {
		if got := yamlString(in); got != want {
			t.Errorf("yamlString(%q) = %q, want %q", in, got, want)
		}
	}
}
