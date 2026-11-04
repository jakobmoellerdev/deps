package tfregistry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// CLI config shapes from https://developer.hashicorp.com/terraform/cli/config/config-file#credentials-1
// and #credentials-helpers, and the credentials.tfrc.json written by `terraform login`.
const (
	rcCredentials = `credentials "app.terraform.io" {
  token = "xxxxxx.atlasv1.zzzzzzzzzzzzz"
}
`
	rcHelper = `credentials_helper "credstore" {
  args = ["--host=credstore.example.com"]
}
`
	tfrcJSON = `{
  "credentials": {
    "app.terraform.io": {
      "token": "from-credentials-tfrc-json"
    }
  }
}`
)

// stubHelper writes terraform-credentials-credstore into dir: it logs its arguments to
// <dir>/calls and prints {"token":"helper-<host>"}, {} for none.example.com.
func stubHelper(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$@\" >> '" + filepath.Join(dir, "calls") + "'\n" +
		"case \"$3\" in none.example.com) echo '{}';; fail.example.com) echo 'vault sealed' >&2; exit 1;; *) echo \"{\\\"token\\\":\\\"helper-$3\\\"}\";; esac\n"
	if err := os.WriteFile(filepath.Join(dir, "terraform-credentials-credstore"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testCredentials(env map[string]string) *credentials {
	c := newCredentials(func(k string) string { return env[k] })
	c.goos = "linux"
	c.lookPath = func(string) (string, error) { return "", errors.New("not on PATH") }
	return c
}

func TestCredentials(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string // relative to HOME
		env   map[string]string
		host  string
		want  string
	}{
		{"anonymous", nil, nil, "app.terraform.io", ""},
		{"env", nil, map[string]string{"TF_TOKEN_app_terraform_io": "env"}, "app.terraform.io", "env"},
		{"env double underscore", nil, map[string]string{"TF_TOKEN_my__registry_example_com": "env"}, "my-registry.example.com", "env"},
		{"env hyphen as is", nil, map[string]string{"TF_TOKEN_xn--caf-dma_fr": "env"}, "xn--caf-dma.fr", "env"},
		{"env verbatim punycode", nil, map[string]string{"TF_TOKEN_xn--caf-dma.fr": "env"}, "xn--caf-dma.fr", "env"},
		{"terraformrc", map[string]string{".terraformrc": rcCredentials}, nil, "app.terraform.io", "xxxxxx.atlasv1.zzzzzzzzzzzzz"},
		{"host case-insensitive", map[string]string{".terraformrc": rcCredentials}, nil, "APP.terraform.io", "xxxxxx.atlasv1.zzzzzzzzzzzzz"},
		{"credentials.tfrc.json", map[string]string{".terraform.d/credentials.tfrc.json": tfrcJSON}, nil, "app.terraform.io", "from-credentials-tfrc-json"},
		{"config dir wins over terraformrc", map[string]string{".terraformrc": rcCredentials, ".terraform.d/credentials.tfrc.json": tfrcJSON}, nil,
			"app.terraform.io", "from-credentials-tfrc-json"},
		{"env wins over config", map[string]string{".terraformrc": rcCredentials}, map[string]string{"TF_TOKEN_app_terraform_io": "env"},
			"app.terraform.io", "env"},
		{"TF_CLI_CONFIG_FILE skips config dir", map[string]string{"ci.tfrc": rcCredentials, ".terraform.d/credentials.tfrc.json": tfrcJSON},
			map[string]string{"TF_CLI_CONFIG_FILE": "ci.tfrc"}, "app.terraform.io", "xxxxxx.atlasv1.zzzzzzzzzzzzz"},
		{"TF_CLI_CONFIG_FILE replaces terraformrc", map[string]string{"ci.tfrc": "", ".terraformrc": rcCredentials},
			map[string]string{"TF_CLI_CONFIG_FILE": "ci.tfrc"}, "app.terraform.io", ""},
		{"missing TF_CLI_CONFIG_FILE", nil, map[string]string{"TF_CLI_CONFIG_FILE": "nope.tfrc"}, "app.terraform.io", ""},
		{"credentials block wins over helper", map[string]string{".terraformrc": rcCredentials + rcHelper}, nil, "app.terraform.io", "xxxxxx.atlasv1.zzzzzzzzzzzzz"},
		{"helper", map[string]string{".terraformrc": rcCredentials + rcHelper}, nil, "registry.example.com", "helper-registry.example.com"},
		{"helper from tfrc in config dir", map[string]string{".terraform.d/helper.tfrc": rcHelper}, nil, "registry.example.com", "helper-registry.example.com"},
		{"helper has none", map[string]string{".terraformrc": rcHelper}, nil, "none.example.com", ""},
		{"env wins over helper", map[string]string{".terraformrc": rcHelper}, map[string]string{"TF_TOKEN_registry_example_com": "env"},
			"registry.example.com", "env"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			stubHelper(t, filepath.Join(home, ".terraform.d", "plugins"))
			for p, content := range tt.files {
				writeFile(t, filepath.Join(home, p), content)
			}
			env := map[string]string{"HOME": home}
			for k, v := range tt.env {
				if k == "TF_CLI_CONFIG_FILE" {
					v = filepath.Join(home, v)
				}
				env[k] = v
			}
			got, err := testCredentials(env).token(context.Background(), tt.host)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("token = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCredentialsHelper(t *testing.T) {
	home := t.TempDir()
	plugins := filepath.Join(home, ".terraform.d", "plugins", "linux_"+runtime.GOARCH)
	stubHelper(t, plugins)
	writeFile(t, filepath.Join(home, ".terraformrc"), rcHelper)
	c := testCredentials(map[string]string{"HOME": home})
	for range 3 {
		if tok, err := c.token(context.Background(), "registry.example.com"); err != nil || tok != "helper-registry.example.com" {
			t.Fatalf("token = %q, %v", tok, err)
		}
	}
	calls, err := os.ReadFile(filepath.Join(plugins, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(calls), "--host=credstore.example.com get registry.example.com\n"; got != want {
		t.Errorf("calls = %q, want %q (once per host)", got, want)
	}
	if _, err := c.token(context.Background(), "fail.example.com"); err == nil || !strings.Contains(err.Error(), "vault sealed") {
		t.Errorf("failing helper: err = %v", err)
	}
}

func TestCredentialsHelperOnPath(t *testing.T) {
	home, bin := t.TempDir(), t.TempDir()
	stubHelper(t, bin)
	writeFile(t, filepath.Join(home, ".terraformrc"), rcHelper)
	c := testCredentials(map[string]string{"HOME": home})
	c.lookPath = func(name string) (string, error) { return filepath.Join(bin, name), nil }
	if tok, err := c.token(context.Background(), "registry.example.com"); err != nil || tok != "helper-registry.example.com" {
		t.Errorf("token = %q, %v", tok, err)
	}
}

func TestCredentialsErrors(t *testing.T) {
	tests := []struct {
		name, rc, host, want string
	}{
		{"missing helper", rcHelper, "registry.example.com", "terraform-credentials-credstore not found (needed for registry.example.com credentials)"},
		{"malformed config", `credentials "x" {`, "registry.example.com", "terraform CLI config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			writeFile(t, filepath.Join(home, ".terraformrc"), tt.rc)
			_, err := testCredentials(map[string]string{"HOME": home}).token(context.Background(), tt.host)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCredentialsWindowsPaths(t *testing.T) {
	appdata := t.TempDir()
	writeFile(t, filepath.Join(appdata, "terraform.rc"), rcCredentials)
	writeFile(t, filepath.Join(appdata, "terraform.d", "credentials.tfrc.json"), strings.ReplaceAll(tfrcJSON, "app.terraform.io", "other.example.com"))
	c := testCredentials(map[string]string{"APPDATA": appdata})
	c.goos = "windows"
	for host, want := range map[string]string{"app.terraform.io": "xxxxxx.atlasv1.zzzzzzzzzzzzz", "other.example.com": "from-credentials-tfrc-json"} {
		if tok, err := c.token(context.Background(), host); err != nil || tok != want {
			t.Errorf("%s: token = %q, %v; want %q", host, tok, err, want)
		}
	}
}

// TestBearerFromCLIConfig checks that a token of the CLI config reaches the registry, and that
// a credentials error fails the request.
func TestBearerFromCLIConfig(t *testing.T) {
	r := serve(t, map[string]string{"/v1/providers/hashicorp/random/versions": providerVersions})
	host, _, _ := strings.Cut(strings.TrimPrefix(r.srv.URL, "http://"), ":")
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".terraformrc"), strings.ReplaceAll(rcCredentials, "app.terraform.io", host))
	reg := newRegistry(r, map[string]string{"HOME": home})
	if _, err := reg.Providers().Releases(context.Background(), dep.Dep{Name: "hashicorp/random", Source: r.srv.URL}, ""); err != nil {
		t.Fatal(err)
	}
	if got := r.auth.Load(); got != "Bearer xxxxxx.atlasv1.zzzzzzzzzzzzz" {
		t.Errorf("Authorization = %v", got)
	}

	home = t.TempDir()
	writeFile(t, filepath.Join(home, ".terraformrc"), rcHelper)
	reg = newRegistry(r, map[string]string{"HOME": home, "PATH": ""})
	reg.creds.lookPath = func(string) (string, error) { return "", errors.New("not on PATH") }
	_, err := reg.Providers().Releases(context.Background(), dep.Dep{Name: "hashicorp/random", Source: r.srv.URL}, "")
	if err == nil || !strings.Contains(err.Error(), "terraform-credentials-credstore not found (needed for "+host+" credentials)") {
		t.Errorf("err = %v", err)
	}
}
