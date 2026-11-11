package nuget

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReleasesConfigCredentials(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	root := t.TempDir()
	// The root's credentials (as `dotnet nuget add source --store-password-in-clear-text`
	// writes them, the key's space XML-encoded) are overridden by the project directory's.
	writeFile(t, filepath.Join(root, "NuGet.Config"), `<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSourceCredentials>
    <my_x0020_feed>
      <add key="Username" value="wrong" />
      <add key="ClearTextPassword" value="wrong" />
    </my_x0020_feed>
  </packageSourceCredentials>
</configuration>`)
	writeFile(t, filepath.Join(root, "src", "app", "nuget.config"), `<configuration>
  <packageSourceCredentials>
    <my_x0020_feed>
      <add key="Username" value="bot" />
      <add key="ClearTextPassword" value="%FEED_TOKEN%" />
    </my_x0020_feed>
  </packageSourceCredentials>
</configuration>`)
	s := newSource(srv, map[string]string{"FEED_TOKEN": "s3cret"})
	d := dep.Dep{Name: "Internal.Lib", File: "src/app/app.csproj", Source: "my+feed=" + srv.URL + "/private/index.json"}
	rels, err := s.Releases(t.Context(), d, filepath.Join(root, "src", "app"))
	if err != nil {
		t.Fatal(err)
	}
	if got := versionsOf(rels); !slices.Equal(got, []string{"1.0.0", "1.1.0", "13.0.5"}) {
		t.Errorf("releases = %v", got)
	}

	// The environment variable wins over nuget.config.
	s = newSource(srv, map[string]string{CredentialsEnv + "my feed": "Username=bot;Password=s3cret"})
	d.File = "app.csproj"
	if _, err := s.Releases(t.Context(), d, root); err != nil {
		t.Errorf("env over config: %v", err)
	}
	// Only the root's (wrong) credentials apply at the root.
	_, err = newSource(srv, nil).Releases(t.Context(), d, root)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("root credentials: %v", err)
	}
}

func TestParseCreds(t *testing.T) {
	env := map[string]string{"USER": "u"}
	got, _, err := parseCreds([]byte("\xef\xbb\xbf"+`<configuration><packageSourceCredentials>
  <Enc><add key="Username" value="x"/><add key="Password" value="AQAAANCMnd8B"/></Enc>
  <Unset><add key="Username" value="%USER%"/><add key="ClearTextPassword" value="%NOPE%"/></Unset>
  <NoPass><add key="Username" value="x"/></NoPass>
  <Ok><add key="username" value="%USER%"/><add key="cleartextpassword" value="p"/></Ok>
</packageSourceCredentials></configuration>`), func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["ok"] != (credential{user: "u", pass: "p"}) {
		t.Errorf("creds = %v", got)
	}
	got, cleared, err := parseCreds([]byte(`<configuration><packageSourceCredentials><A><add key="ClearTextPassword" value="p"/></A><clear/></packageSourceCredentials></configuration>`), func(string) string { return "" })
	if err != nil || len(got) != 0 || !cleared {
		t.Errorf("clear = %v, %v", got, err)
	}
	if _, _, err := parseCreds([]byte("<configuration"), nil); err == nil {
		t.Error("malformed: no error")
	}
}

func TestReadCredsMergesAndClears(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "nuget.config"), `<configuration><packageSourceCredentials><A><add key="ClearTextPassword" value="a"/></A><B><add key="ClearTextPassword" value="b"/></B></packageSourceCredentials></configuration>`)
	writeFile(t, filepath.Join(root, "x", "nuget.config"), `<configuration><packageSourceCredentials><clear/><C><add key="ClearTextPassword" value="c"/></C></packageSourceCredentials></configuration>`)
	writeFile(t, filepath.Join(root, "x", "y", "NUGET.CONFIG"), `<configuration`)
	got := readCreds(filepath.Join(root, "x", "y"), root, func(string) string { return "" })
	if len(got) != 1 || got["c"].pass != "c" {
		t.Errorf("creds = %v", got)
	}
	if got := readCreds(filepath.Join(root, "missing"), root, func(string) string { return "" }); len(got) != 2 {
		t.Errorf("missing dir: %v", got)
	}
}

// TestConfigCredentialsNotShared checks that a feed is not read with the cached result of a
// directory whose nuget.config holds other credentials: the memos cache failures, so the 401
// of a directory with the wrong credentials would otherwise be handed to one with the right
// credentials.
func TestConfigCredentialsNotShared(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	root := t.TempDir()
	config := func(pass string) string {
		return `<configuration>
  <packageSourceCredentials>
    <my_x0020_feed>
      <add key="Username" value="bot" />
      <add key="ClearTextPassword" value="` + pass + `" />
    </my_x0020_feed>
  </packageSourceCredentials>
</configuration>`
	}
	writeFile(t, filepath.Join(root, "bad", "nuget.config"), config("wrong"))
	writeFile(t, filepath.Join(root, "good", "nuget.config"), config("s3cret"))
	s := newSource(srv, nil)
	source := "my+feed=" + srv.URL + "/private/index.json"
	_, err := s.Releases(t.Context(), dep.Dep{Name: "Internal.Lib", File: "bad/app.csproj", Source: source}, filepath.Join(root, "bad"))
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("wrong credentials: %v", err)
	}
	rels, err := s.Releases(t.Context(), dep.Dep{Name: "Internal.Lib", File: "good/app.csproj", Source: source}, filepath.Join(root, "good"))
	if err != nil {
		t.Fatalf("right credentials after wrong ones: %v", err)
	}
	if got := versionsOf(rels); !slices.Equal(got, []string{"1.0.0", "1.1.0", "13.0.5"}) {
		t.Errorf("releases = %v", got)
	}
}
