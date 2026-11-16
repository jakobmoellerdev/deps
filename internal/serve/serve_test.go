package serve

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/serve/store"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "serve.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimalConfig = `
valkey: {url: "redis://localhost:6379/0"}
forges:
  - name: f
    kind: forgejo
    apiUrl: https://git.example/api/v1
    serverUrl: https://git.example
    tokenEnv: TOK
    repositories:
      - {match: "org/*"}
`

func TestLoadDefaultsAndValidation(t *testing.T) {
	c, err := Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != DefaultListen || c.Interval.D() != DefaultInterval || c.CycleTimeout.D() != DefaultCycleTimeout {
		t.Errorf("defaults = %+v", c)
	}
	if c.Workers < 4 {
		t.Errorf("workers = %d", c.Workers)
	}
	if c.Forges[0].Repositories[0].Mode != store.ModeRun {
		t.Errorf("default mode = %q, want run", c.Forges[0].Repositories[0].Mode)
	}
	if c.Author.Name != DefaultAuthorName {
		t.Errorf("author = %+v", c.Author)
	}

	for _, tc := range []struct{ name, body, want string }{
		{"unknown key", minimalConfig + "\nworkerz: 3\n", "workerz"},
		{"no valkey", strings.Replace(minimalConfig, `valkey: {url: "redis://localhost:6379/0"}`, "", 1), "valkey.url"},
		{"bad kind", strings.Replace(minimalConfig, "kind: forgejo", "kind: svn", 1), "kind must be one of"},
		{"no credential", strings.Replace(minimalConfig, "    tokenEnv: TOK\n", "", 1), "tokenEnv or app"},
		{"app on a non-github forge", strings.Replace(minimalConfig, "    tokenEnv: TOK",
			"    app: {id: 1, privateKeyFile: /x.pem}", 1), "only on GitHub"},
		{"bad mode", strings.Replace(minimalConfig, `{match: "org/*"}`, `{match: "org/*", mode: apply}`, 1), "mode must be run or plan"},
		{"bad duration", minimalConfig + "\ninterval: daily\n", "no duration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestForgeMatchFirstRuleWins(t *testing.T) {
	f := &Forge{ServerURL: "https://git.example", Repositories: []RepoRule{
		{Match: "org/archive-*", Enabled: new(false)},
		{Match: "org/*", Mode: store.ModeRun},
		{Match: "other/**", Mode: store.ModePlan},
	}}
	for _, tc := range []struct {
		name        string
		wantMatch   bool
		wantManaged bool
		wantMode    store.Mode
	}{
		{"org/archive-2020", true, false, ""},
		{"org/service", true, true, store.ModeRun},
		{"other/group/sub/thing", true, true, store.ModePlan},
		{"org/group/nested", false, false, ""},
		{"elsewhere/thing", false, false, ""},
	} {
		r := f.Match(tc.name)
		if (r != nil) != tc.wantMatch {
			t.Errorf("%s: match = %v, want %v", tc.name, r != nil, tc.wantMatch)
			continue
		}
		if r == nil {
			continue
		}
		if r.Managed() != tc.wantManaged {
			t.Errorf("%s: managed = %v, want %v", tc.name, r.Managed(), tc.wantManaged)
		}
		if tc.wantManaged && r.Mode != tc.wantMode {
			t.Errorf("%s: mode = %q, want %q", tc.name, r.Mode, tc.wantMode)
		}
	}
}
func TestOwnerGlobs(t *testing.T) {
	f := &Forge{Repositories: []RepoRule{{Match: "a/*"}, {Match: "a/x-*"}, {Match: "b/**"}, {Match: "*/c"}}}
	got := f.OwnerGlobs()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("OwnerGlobs = %v, want [a b]", got)
	}
	f.Owners = []string{"explicit"}
	if got := f.OwnerGlobs(); len(got) != 1 || got[0] != "explicit" {
		t.Errorf("explicit owners = %v", got)
	}
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	good := sign("s3cret", body)
	for _, tc := range []struct {
		name   string
		header http.Header
		secret string
		want   bool
	}{
		{"github", http.Header{"X-Hub-Signature-256": {"sha256=" + good}}, "s3cret", true},
		{"gitea", http.Header{"X-Gitea-Signature": {good}}, "s3cret", true},
		{"forgejo", http.Header{"X-Forgejo-Signature": {good}}, "s3cret", true},
		{"gitlab", http.Header{"X-Gitlab-Token": {"s3cret"}}, "s3cret", true},
		{"wrong signature", http.Header{"X-Gitea-Signature": {sign("other", body)}}, "s3cret", false},
		{"not hex", http.Header{"X-Gitea-Signature": {"zz"}}, "s3cret", false},
		{"wrong gitlab token", http.Header{"X-Gitlab-Token": {"nope"}}, "s3cret", false},
		{"no signature at all", http.Header{}, "s3cret", false},
		// Without a configured secret the endpoint refuses everything: an unauthenticated
		// trigger that makes the daemon clone and run repository code is not a default.
		{"no secret configured", http.Header{"X-Gitea-Signature": {good}}, "", false},
	} {
		if got := verifySignature(tc.header, body, tc.secret); got != tc.want {
			t.Errorf("%s: verifySignature = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestWebhookTargets(t *testing.T) {
	d := &Daemon{}
	f := &Forge{Name: "f", ServerURL: "https://git.example", Repositories: []RepoRule{{Match: "org/*", Mode: store.ModeRun}}}
	const repo = `"repository":{"full_name":"org/svc","default_branch":"main"}`
	for _, tc := range []struct {
		name, event, body string
		wantIDs           []string
		wantRefresh       bool
	}{
		{"push to the base branch", "push", `{"ref":"refs/heads/main",` + repo + `}`, []string{"git.example/org/svc"}, false},
		{"push to an update branch", "push", `{"ref":"refs/heads/deps/x",` + repo + `}`, nil, false},
		{"dashboard issue edited", "issues", `{"action":"edited",` + repo + `}`, []string{"git.example/org/svc"}, false},
		{"issue comment", "issue_comment", `{"action":"created",` + repo + `}`, []string{"git.example/org/svc"}, false},
		{"pull request closed", "pull_request", `{"action":"closed",` + repo + `}`, []string{"git.example/org/svc"}, false},
		{"gitlab merge request", "merge_request", `{"project":{"path_with_namespace":"org/svc","default_branch":"main"}}`, []string{"git.example/org/svc"}, false},
		{"unmanaged repository", "push", `{"ref":"refs/heads/main","repository":{"full_name":"other/svc","default_branch":"main"}}`, nil, false},
		{"unrelated event", "star", `{` + repo + `}`, nil, false},
		{"installation", "installation", `{"action":"created","installation":{"id":42}}`, nil, true},
		{"garbage", "push", `not json`, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids, refresh := d.webhookTargets(f, tc.event, []byte(tc.body))
			if refresh != tc.wantRefresh {
				t.Errorf("refresh = %v, want %v", refresh, tc.wantRefresh)
			}
			if len(ids) != len(tc.wantIDs) {
				t.Fatalf("ids = %v, want %v", ids, tc.wantIDs)
			}
			for i := range ids {
				if ids[i] != tc.wantIDs[i] {
					t.Errorf("ids = %v, want %v", ids, tc.wantIDs)
				}
			}
		})
	}
}

func TestEventName(t *testing.T) {
	// net/http canonicalizes header keys, so the test sets them the way a request carries
	// them rather than writing map literals.
	header := func(k, v string) http.Header {
		h := http.Header{}
		h.Set(k, v)
		return h
	}
	for _, k := range []string{"X-GitHub-Event", "X-Gitea-Event", "X-Forgejo-Event"} {
		if got := eventName(header(k, "push")); got != "push" {
			t.Errorf("%s: %q", k, got)
		}
	}
	// GitLab spells its events "Push Hook".
	if got := eventName(header("X-Gitlab-Event", "Push Hook")); got != "push" {
		t.Errorf("gitlab event = %q", got)
	}
	if got := eventName(http.Header{}); got != "" {
		t.Errorf("no event header = %q", got)
	}
}

func TestMetricsExposition(t *testing.T) {
	m := NewMetrics()
	m.Inc("deps_cycles_total", map[string]string{"forge": "f", "mode": "run", "status": "ok"})
	m.Inc("deps_cycles_total", map[string]string{"forge": "f", "mode": "run", "status": "ok"})
	m.Inc("deps_cycles_total", map[string]string{"forge": "f", "mode": "run", "status": "failed"})
	m.Observe("deps_cycle_duration_seconds", map[string]string{"forge": "f", "mode": "run"}, 3)
	m.Observe("deps_cycle_duration_seconds", map[string]string{"forge": "f", "mode": "run"}, 1200)
	m.SetGauge("deps_workers_live", func() float64 { return 2 })

	var b strings.Builder
	if _, err := m.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"# TYPE deps_cycles_total counter\n",
		`deps_cycles_total{forge="f",mode="run",status="ok"} 2` + "\n",
		`deps_cycles_total{forge="f",mode="run",status="failed"} 1` + "\n",
		"deps_workers_live 2\n",
		`deps_cycle_duration_seconds_bucket{forge="f",mode="run",le="5"} 1` + "\n",
		`deps_cycle_duration_seconds_bucket{forge="f",mode="run",le="1800"} 2` + "\n",
		`deps_cycle_duration_seconds_bucket{forge="f",mode="run",le="+Inf"} 2` + "\n",
		`deps_cycle_duration_seconds_sum{forge="f",mode="run"} 1203` + "\n",
		`deps_cycle_duration_seconds_count{forge="f",mode="run"} 2` + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exposition lacks %q:\n%s", want, out)
		}
	}
	// Label values that would break the format are escaped.
	m2 := NewMetrics()
	m2.Inc("deps_http_requests_total", map[string]string{"host": `a"b`, "outcome": "hit"})
	var b2 strings.Builder
	if _, err := m2.WriteTo(&b2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b2.String(), `host="a\"b"`) {
		t.Errorf("label not escaped:\n%s", b2.String())
	}
}

func TestHostOf(t *testing.T) {
	for in, want := range map[string]string{
		"https://git.example.com":          "git.example.com",
		"https://git.example.com/":         "git.example.com",
		"http://localhost:3000":            "localhost:3000",
		"https://git.example.com/sub/path": "git.example.com",
	} {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAuthHeader(t *testing.T) {
	if got := authHeader(""); got != "" {
		t.Errorf("authHeader(\"\") = %q", got)
	}
	got := authHeader("tok")
	if !strings.HasPrefix(got, "AUTHORIZATION: basic ") {
		t.Fatalf("authHeader = %q", got)
	}
	if strings.Contains(got, "tok") {
		t.Error("the raw token leaked into the header")
	}
}

// A run id is unique and carries its millisecond up front, so ids sort by time; ids minted
// within one millisecond are unordered among themselves, which is fine.
func TestNewRunIDIsUniqueAndTimeOrdered(t *testing.T) {
	seen := map[string]bool{}
	prev := int64(0)
	for range 1000 {
		id := NewRunID()
		if seen[id] {
			t.Fatalf("duplicate run id %s", id)
		}
		seen[id] = true
		stamp, rest, ok := strings.Cut(id, "-")
		if !ok || len(rest) != 16 {
			t.Fatalf("run id %q has no <unix-ms>-<random> shape", id)
		}
		ms, err := strconv.ParseInt(stamp, 10, 64)
		if err != nil {
			t.Fatalf("run id %q: %v", id, err)
		}
		if ms < prev {
			t.Errorf("run id %s went back in time from %d", id, prev)
		}
		if d := time.Since(time.UnixMilli(ms)).Abs(); d > time.Minute {
			t.Errorf("run id %s is stamped %v away from now", id, d)
		}
		prev = ms
	}
}
