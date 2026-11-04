package terraform

import (
	"reflect"
	"strings"
	"testing"
)

// Examples copied verbatim from https://developer.hashicorp.com/terraform/cli/config/config-file
// (#credentials-1, #credentials-helpers, #configuration-file-syntax, #explicit-installation-method-configuration)
// and https://developer.hashicorp.com/terraform/internals/credentials-helpers (2026-11-04).
const (
	docCredentials = `credentials "app.terraform.io" {
  token = "xxxxxx.atlasv1.zzzzzzzzzzzzz"
}
`
	docHelper = `credentials_helper "example" {
  args = []
}
`
	docCredstore = `credentials_helper "credstore" {
  args = ["--host=credstore.example.com"]
}
`
	docSyntax = `plugin_cache_dir   = "$HOME/.terraform.d/plugin-cache"
disable_checkpoint = true
`
	docInstallation = `provider_installation {
  filesystem_mirror {
    path    = "/usr/share/terraform/providers"
    include = ["example.com/*/*"]
  }
  direct {
    exclude = ["example.com/*/*"]
  }
}
`
)

func TestParseCLIConfig(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want CLIConfig
	}{
		{"credentials", docCredentials, CLIConfig{Credentials: map[string]string{"app.terraform.io": "xxxxxx.atlasv1.zzzzzzzzzzzzz"}}},
		{"empty helper args", docHelper, CLIConfig{Helper: "example", HelperArgs: []string{}}},
		{"helper args", docCredstore, CLIConfig{Helper: "credstore", HelperArgs: []string{"--host=credstore.example.com"}}},
		{"other settings", docSyntax + docInstallation, CLIConfig{}},
		{"all together", docSyntax + docCredentials + docCredstore + docInstallation, CLIConfig{
			Credentials: map[string]string{"app.terraform.io": "xxxxxx.atlasv1.zzzzzzzzzzzzz"},
			Helper:      "credstore", HelperArgs: []string{"--host=credstore.example.com"},
		}},
		{"multi-line args with escapes", "credentials_helper \"x\" {\n  args = [\n    \"a\\\"b\", # c\n    \"d\",\n  ]\n}\n",
			CLIConfig{Helper: "x", HelperArgs: []string{`a"b`, "d"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCLIConfig([]byte(tt.src))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseCLIConfigErrors(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"args not a list", `credentials_helper "x" { args = "a" }`, "want a list of strings"},
		{"template arg", `credentials_helper "x" { args = ["${a}"] }`, "templates are not supported"},
		{"syntax", `credentials "x" {`, "unterminated block"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseCLIConfig([]byte(tt.src)); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

// credentials.tfrc.json as written by `terraform login`
// (https://developer.hashicorp.com/terraform/cli/commands/login).
func TestParseCLIConfigJSON(t *testing.T) {
	tests := []struct {
		name, src string
		want      CLIConfig
	}{
		{"login", `{
  "credentials": {
    "app.terraform.io": {
      "token": "xxxxxx.atlasv1.zzzzzzzzzzzzz"
    }
  }
}`, CLIConfig{Credentials: map[string]string{"app.terraform.io": "xxxxxx.atlasv1.zzzzzzzzzzzzz"}}},
		{"helper", `{"credentials_helper": {"credstore": {"args": ["--host=credstore.example.com"]}}}`,
			CLIConfig{Helper: "credstore", HelperArgs: []string{"--host=credstore.example.com"}}},
		{"empty", `{}`, CLIConfig{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCLIConfigJSON([]byte(tt.src))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
	if _, err := ParseCLIConfigJSON([]byte(`{`)); err == nil {
		t.Error("malformed JSON: no error")
	}
}
