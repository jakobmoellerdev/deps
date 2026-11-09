package httpx

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// Each credential source is noted at info level, once per host and source, without the
// secret: the GitHub CLI fallback names itself.
func TestCredentialSourceNotes(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(old) })
	noted.Clear() // NoteSource dedupes per process
	host := "gitlab.example"
	a := NewAuth(platform.Info{}, "", func(string) string { return "" },
		WithToken(host, "oauth2", "secret-tok", "DEPS_GITLAB_TOKEN"),
		func(a *Auth) { a.gh = func() string { return "secret-gh" } })
	for range 3 {
		a.Static(host)
		a.Static("github.com")
	}
	out := buf.String()
	for _, want := range []string{
		"msg=credentials host=" + host + " source=DEPS_GITLAB_TOKEN\n",
		`msg=credentials host=github.com source="gh auth token"` + "\n",
	} {
		if strings.Count(out, want) != 1 {
			t.Errorf("want %q once in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret") {
		t.Errorf("secret logged:\n%s", out)
	}
}
