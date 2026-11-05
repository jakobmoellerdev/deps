//go:build e2e

package e2eregistry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/compose"
	"github.com/testcontainers/testcontainers-go/wait"
)

// harborAdmin is the admin account of harbor/docker-compose.yaml.
var harborAdmin = cred{user: "admin", pass: "Harbor12345"}

// TestHarbor runs deps against Harbor (core's token service, Bearer auth) with admin, robot and
// anonymous credentials.
func TestHarbor(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)
	t.Parallel()
	ctx := t.Context()

	// Harbor advertises EXT_ENDPOINT as the token realm, so the host port is chosen up front.
	port := freePort(t)
	base := fmt.Sprintf("http://localhost:%d", port)
	stack, err := compose.NewDockerComposeWith(
		compose.WithStackFiles("harbor/docker-compose.yaml"),
		compose.StackIdentifier("deps-e2e-harbor-"+randomHex(t, 4)),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := stack.Down(ctx, compose.RemoveOrphans(true), compose.RemoveVolumes(true)); err != nil {
			t.Errorf("compose down: %v", err)
		}
	})
	err = stack.
		WithEnv(map[string]string{"HARBOR_PORT": strconv.Itoa(port), "HARBOR_URL": base}).
		WaitForService("proxy", wait.ForHTTP("/api/v2.0/ping").WithPort("8080/tcp").WithStartupTimeout(5*time.Minute)).
		Up(ctx, compose.Wait(true))
	if err != nil {
		logServices(t, stack)
		t.Fatalf("compose up: %v", err)
	}

	h := harborAPI{t: t, base: base}
	h.call(http.MethodPost, "/projects", map[string]any{"project_name": "alpha", "metadata": map[string]string{"public": "false"}}, nil)
	h.call(http.MethodPost, "/projects", map[string]any{"project_name": "beta", "metadata": map[string]string{"public": "false"}}, nil)
	robotAlpha, robotBeta := h.robot("alpha"), h.robot("beta")

	host := fmt.Sprintf("localhost:%d", port)
	alpha, beta := host+"/alpha/app", host+"/beta/app"
	p := pusher{t: t, auth: &authn.Basic{Username: harborAdmin.user, Password: harborAdmin.pass}}
	p.image(alpha + ":1.0.0")
	digests := map[string]v1.Hash{
		alpha: p.index(alpha + ":1.1.0"),
	}
	p.image(beta + ":2.0.0")
	digests[beta] = p.image(beta + ":2.1.0")

	files := map[string]string{
		".deps.yaml":       "pin: digest\n",
		"alpha/Dockerfile": "FROM " + alpha + ":1.0.0\n",
		"beta/Dockerfile":  "FROM " + beta + ":2.0.0\n",
	}
	succeeds := func(t *testing.T, res depsResult, ref, from, to string) {
		t.Helper()
		expectUpdate(t, res, ref, from, to+"@"+digests[ref].Hex[:7])
		expectPinned(t, res, ref+":"+to, digests[ref])
	}

	t.Run("admin host credentials", func(t *testing.T) {
		res := runDeps(t, files, map[string]cred{host: harborAdmin})
		succeeds(t, res, alpha, "1.0.0", "1.1.0")
		succeeds(t, res, beta, "2.0.0", "2.1.0")
	})
	t.Run("namespaced robot credentials", func(t *testing.T) {
		res := runDeps(t, files, map[string]cred{host + "/alpha": robotAlpha, host + "/beta": robotBeta})
		succeeds(t, res, alpha, "1.0.0", "1.1.0")
		succeeds(t, res, beta, "2.0.0", "2.1.0")
	})
	t.Run("swapped robot credentials", func(t *testing.T) {
		res := runDeps(t, files, map[string]cred{host + "/alpha": robotBeta, host + "/beta": robotAlpha})
		expectAuthError(t, res, alpha)
		expectAuthError(t, res, beta)
	})
	t.Run("anonymous private project", func(t *testing.T) {
		res := runDeps(t, files, nil)
		expectAuthError(t, res, alpha)
		expectAuthError(t, res, beta)
	})
	t.Run("anonymous public project", func(t *testing.T) {
		h.call(http.MethodPut, "/projects/beta", map[string]any{"metadata": map[string]string{"public": "true"}}, nil)
		res := runDeps(t, files, nil)
		expectAuthError(t, res, alpha)
		succeeds(t, res, beta, "2.0.0", "2.1.0")
	})
}

// freePort returns a TCP port that is free on the loopback interface.
func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// harborAPI calls Harbor's v2.0 API as admin.
type harborAPI struct {
	t    *testing.T
	base string
}

// call sends body as JSON and decodes the response into out (if not nil); a non-2xx status
// fails the test.
func (h harborAPI) call(method, path string, body, out any) {
	h.t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		h.t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(h.t.Context(), method, h.base+"/api/v2.0"+path, bytes.NewReader(data))
	if err != nil {
		h.t.Fatal(err)
	}
	req.SetBasicAuth(harborAdmin.user, harborAdmin.pass)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		h.t.Fatalf("%s %s: %s\n%s", method, path, resp.Status, respBody)
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			h.t.Fatalf("%s %s: decoding %s: %v", method, path, respBody, err)
		}
	}
}

// robot creates a robot account that may pull and push in project only.
func (h harborAPI) robot(project string) cred {
	h.t.Helper()
	var created struct {
		Name   string `json:"name"`
		Secret string `json:"secret"`
	}
	h.call(http.MethodPost, "/robots", map[string]any{
		"name":     "deps",
		"level":    "project",
		"duration": -1,
		"permissions": []map[string]any{{
			"kind":      "project",
			"namespace": project,
			"access": []map[string]string{
				{"resource": "repository", "action": "pull"},
				{"resource": "repository", "action": "push"},
			},
		}},
	}, &created)
	return cred{user: created.Name, pass: created.Secret}
}

// logServices logs the last lines of every service's container log.
func logServices(t *testing.T, stack *compose.DockerCompose) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, svc := range stack.Services() {
		ctr, err := stack.ServiceContainer(ctx, svc)
		if err != nil {
			t.Logf("%s: %v", svc, err)
			continue
		}
		logs, err := ctr.Logs(ctx)
		if err != nil {
			t.Logf("%s logs: %v", svc, err)
			continue
		}
		data, _ := io.ReadAll(logs)
		logs.Close()
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		t.Logf("--- %s (last %d lines):\n%s", svc, min(len(lines), 40), strings.Join(lines[max(len(lines)-40, 0):], "\n"))
	}
}
