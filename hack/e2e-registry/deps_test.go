//go:build e2e

package e2eregistry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// binDir holds the deps binary; TestMain creates and removes it.
var binDir string

func TestMain(m *testing.M) {
	// The compose module connects the stack's containers to the reaper (Ryuk) only after
	// `compose up` returns, about a minute for Harbor. A reaper started before (TestZot) that
	// loses its last connection meanwhile would prune the half-started stack after its
	// reconnection timeout (default 10s). The stack is removed by t.Cleanup anyway.
	if os.Getenv("RYUK_RECONNECTION_TIMEOUT") == "" && os.Getenv("TESTCONTAINERS_RYUK_RECONNECTION_TIMEOUT") == "" {
		os.Setenv("TESTCONTAINERS_RYUK_RECONNECTION_TIMEOUT", "5m")
	}
	var err error
	if binDir, err = os.MkdirTemp("", "deps-e2e-registry-bin-"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(binDir)
	os.Exit(code)
}

// depsBinary builds cmd/deps of the enclosing repository once per test binary.
var depsBinary = sync.OnceValues(func() (string, error) {
	bin := filepath.Join(binDir, "deps")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/deps")
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build ./cmd/deps: %w\n%s", err, out)
	}
	return bin, nil
})

// cred is one auths entry of the Docker config.
type cred struct{ user, pass string }

// depsResult is one deps plan run: its exit code, output and parsed --report.
type depsResult struct {
	exitCode       int
	stdout, stderr string
	report         report
}

// report is the part of deps' JSON report (deps help output) the tests assert on.
type report struct {
	Dependencies int `json:"dependencies"`
	PRs          []struct {
		Branch  string `json:"branch"`
		Updates []struct {
			Name    string `json:"name"`
			Manager string `json:"manager"`
			File    string `json:"file"`
			From    string `json:"from"`
			To      string `json:"to"`
			Type    string `json:"type"`
		} `json:"updates"`
	} `json:"prs"`
	Errors []struct {
		Message string `json:"message"`
		File    string `json:"file"`
		Line    int    `json:"line"`
	} `json:"errors"`
	Counts map[string]int `json:"counts"`
}

// update returns the from/to versions of the update of the dependency named name.
func (r report) update(name string) (from, to string, ok bool) {
	for _, pr := range r.PRs {
		for _, u := range pr.Updates {
			if u.Name == name {
				return u.From, u.To, true
			}
		}
	}
	return "", "", false
}

// errorFor returns the report's error message naming dependency name.
func (r report) errorFor(name string) (string, bool) {
	for _, e := range r.Errors {
		if strings.Contains(e.Message, name+": ") {
			return e.Message, true
		}
	}
	return "", false
}

// runDeps writes files into a scratch repository and a Docker config with auths, then runs
// `deps plan` on it. The deps output is logged when the calling test fails.
func runDeps(t *testing.T, files map[string]string, auths map[string]cred) depsResult {
	t.Helper()
	bin, err := depsBinary()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	for path, content := range files {
		p := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dockerConfig := filepath.Join(tmp, "docker")
	if err := os.MkdirAll(dockerConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]map[string]map[string]string{"auths": {}}
	for host, c := range auths {
		cfg["auths"][host] = map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(c.user + ":" + c.pass))}
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dockerConfig, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	reportFile := filepath.Join(tmp, "report.json")

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "plan", "--dir", repo, "--report", reportFile,
		"--log-level", "debug", "--log-format", "text", "--color", "never", "-v")
	// A clean environment: no platform tokens, no ~/.docker, no CI detection.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + tmp, "DOCKER_CONFIG=" + dockerConfig, "TMPDIR=" + tmp}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	res := depsResult{}
	err = cmd.Run()
	res.stdout, res.stderr = stdout.String(), stderr.String()
	t.Cleanup(func() {
		if t.Failed() || os.Getenv("E2E_LOG_DEPS") != "" {
			t.Logf("deps plan exit code %d\n--- Docker config auths: %v\n--- stdout:\n%s\n--- stderr:\n%s", res.exitCode, hosts(auths), res.stdout, res.stderr)
		}
	})
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		res.exitCode = exitErr.ExitCode()
	default:
		t.Fatalf("deps plan: %v", err)
	}
	if res.exitCode != 0 && res.exitCode != 1 || strings.Contains(res.stderr, "panic:") {
		t.Fatalf("deps plan crashed or failed before resolving (exit code %d)", res.exitCode)
	}
	data, err = os.ReadFile(reportFile)
	if err != nil {
		t.Fatalf("reading report: %v", err)
	}
	if err := json.Unmarshal(data, &res.report); err != nil {
		t.Fatalf("parsing report: %v\n%s", err, data)
	}
	if want := min(len(res.report.Errors), 1); res.exitCode != want {
		t.Errorf("exit code %d with %d errors, want %d", res.exitCode, len(res.report.Errors), want)
	}
	return res
}

func hosts(auths map[string]cred) []string {
	out := make([]string, 0, len(auths))
	for h, c := range auths {
		out = append(out, h+"="+c.user)
	}
	return out
}

// authError matches the error deps reports when a registry refuses the credentials.
var authError = regexp.MustCompile(`(?i)unauthori[sz]ed|authentication|denied|credential|\b401\b|\b403\b`)

// expectUpdate asserts that the report proposes name from -> to.
func expectUpdate(t *testing.T, res depsResult, name, from, to string) {
	t.Helper()
	gotFrom, gotTo, ok := res.report.update(name)
	switch {
	case !ok:
		t.Errorf("no update for %s", name)
	case gotFrom != from || gotTo != to:
		t.Errorf("update %s %s -> %s, want %s -> %s", name, gotFrom, gotTo, from, to)
	}
	if msg, ok := res.report.errorFor(name); ok {
		t.Errorf("unexpected error for %s: %s", name, msg)
	}
}

// expectAuthError asserts that the report has an authentication error for name and no update.
func expectAuthError(t *testing.T, res depsResult, name string) {
	t.Helper()
	msg, ok := res.report.errorFor(name)
	switch {
	case !ok:
		t.Errorf("no error for %s", name)
	case !authError.MatchString(msg):
		t.Errorf("error for %s is not an authentication error: %s", name, msg)
	}
	if _, _, ok := res.report.update(name); ok {
		t.Errorf("unexpected update for %s", name)
	}
}

// expectPinned asserts that the diff on stdout rewrites the image line to ref@digest.
func expectPinned(t *testing.T, res depsResult, ref string, digest v1.Hash) {
	t.Helper()
	if want := "+FROM " + ref + "@" + digest.String(); !strings.Contains(res.stdout, want) {
		t.Errorf("diff does not contain %q", want)
	}
}

// pusher writes test artifacts to a registry over plain HTTP.
type pusher struct {
	t    *testing.T
	auth authn.Authenticator
}

func (p pusher) ref(s string) name.Reference {
	ref, err := name.ParseReference(s, name.Insecure)
	if err != nil {
		p.t.Fatal(err)
	}
	return ref
}

// image pushes a random single-platform Docker schema 2 image and returns its digest.
func (p pusher) image(ref string) v1.Hash {
	p.t.Helper()
	img, err := random.Image(256, 1)
	if err != nil {
		p.t.Fatal(err)
	}
	if err := remote.Write(p.ref(ref), img, remote.WithAuth(p.auth), remote.WithContext(p.t.Context())); err != nil {
		p.t.Fatalf("pushing %s: %v", ref, err)
	}
	d, err := img.Digest()
	if err != nil {
		p.t.Fatal(err)
	}
	return d
}

// index pushes an OCI index of random OCI images for linux/amd64 and linux/arm64 and returns
// the index digest.
func (p pusher) index(ref string) v1.Hash {
	p.t.Helper()
	idx := mutate.IndexMediaType(empty.Index, types.OCIImageIndex)
	for _, arch := range []string{"amd64", "arm64"} {
		img, err := random.Image(256, 1)
		if err != nil {
			p.t.Fatal(err)
		}
		img = mutate.MediaType(img, types.OCIManifestSchema1)
		img = mutate.ConfigMediaType(img, types.OCIConfigJSON)
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{
			Add:        img,
			Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: arch}},
		})
	}
	if err := remote.WriteIndex(p.ref(ref), idx, remote.WithAuth(p.auth), remote.WithContext(p.t.Context())); err != nil {
		p.t.Fatalf("pushing %s: %v", ref, err)
	}
	d, err := idx.Digest()
	if err != nil {
		p.t.Fatal(err)
	}
	return d
}

// chart pushes a Helm chart OCI artifact (Helm's config and content media types) for chart
// version version.
func (p pusher) chart(ref, chart, version string) {
	p.t.Helper()
	img := mutate.MediaType(empty.Image, types.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, "application/vnd.cncf.helm.config.v1+json")
	content := static.NewLayer([]byte(chart+"-"+version+" "+randomHex(p.t, 8)), "application/vnd.cncf.helm.chart.content.v1.tar+gzip")
	img, err := mutate.Append(img, mutate.Addendum{Layer: content})
	if err != nil {
		p.t.Fatal(err)
	}
	if err := remote.Write(p.ref(ref), img, remote.WithAuth(p.auth), remote.WithContext(p.t.Context())); err != nil {
		p.t.Fatalf("pushing %s: %v", ref, err)
	}
}

func randomHex(t *testing.T, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
