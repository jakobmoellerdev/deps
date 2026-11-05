//go:build e2e

package e2eregistry

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/bcrypt"
)

const zotImage = "ghcr.io/project-zot/zot:v2.1.23"

// zotConfig enables htpasswd Basic auth; without accessControl every authenticated user may
// read and write, anonymous requests get 401. compat accepts Docker schema 2 manifests next to
// OCI ones.
const zotConfig = `{
  "distSpecVersion": "1.1.1",
  "storage": {"rootDirectory": "/var/lib/registry"},
  "http": {"address": "0.0.0.0", "port": "5000", "compat": ["docker2s2"], "auth": {"htpasswd": {"path": "/etc/zot/htpasswd"}}},
  "log": {"level": "info"}
}`

// TestZot runs deps against zot with htpasswd Basic auth.
func TestZot(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)
	t.Parallel()
	ctx := t.Context()

	good := cred{user: "deps", pass: randomHex(t, 16)}
	hash, err := bcrypt.GenerateFromPassword([]byte(good.pass), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	ctr, err := testcontainers.Run(ctx, zotImage,
		testcontainers.WithExposedPorts("5000/tcp"),
		testcontainers.WithFiles(
			testcontainers.ContainerFile{Reader: strings.NewReader(zotConfig), ContainerFilePath: "/etc/zot/config.json", FileMode: 0o644},
			testcontainers.ContainerFile{Reader: strings.NewReader(good.user + ":" + string(hash) + "\n"), ContainerFilePath: "/etc/zot/htpasswd", FileMode: 0o644},
		),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/v2/").WithPort("5000/tcp").
			WithStatusCodeMatcher(func(status int) bool { return status == http.StatusUnauthorized }).
			WithStartupTimeout(time.Minute)),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := ctr.MappedPort(ctx, "5000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	host := fmt.Sprintf("localhost:%s", port.Port())
	app, charts := host+"/team/app", host+"/charts"

	p := pusher{t: t, auth: &authn.Basic{Username: good.user, Password: good.pass}}
	p.image(app + ":1.0.0")
	digest110 := p.index(app + ":1.1.0")
	p.chart(charts+"/demo:0.1.0", "demo", "0.1.0")
	p.chart(charts+"/demo:0.2.0", "demo", "0.2.0")

	files := map[string]string{
		"Dockerfile": "FROM " + app + ":1.0.0\n",
		"chart/Chart.yaml": "apiVersion: v2\nname: scratch\nversion: 0.0.1\ndependencies:\n" +
			"  - name: demo\n    version: 0.1.0\n    repository: oci://" + charts + "\n",
	}
	pinned := map[string]string{".deps.yaml": "pin: digest\n"}
	for k, v := range files {
		pinned[k] = v
	}
	bad := cred{user: good.user, pass: "wrong"}

	t.Run("host credentials", func(t *testing.T) {
		res := runDeps(t, files, map[string]cred{host: good})
		expectUpdate(t, res, app, "1.0.0", "1.1.0")
		expectUpdate(t, res, "demo", "0.1.0", "0.2.0")
	})
	t.Run("host credentials pin digest", func(t *testing.T) {
		res := runDeps(t, pinned, map[string]cred{host: good})
		expectUpdate(t, res, app, "1.0.0", "1.1.0@"+digest110.Hex[:7])
		expectPinned(t, res, app+":1.1.0", digest110)
	})
	t.Run("no credentials", func(t *testing.T) {
		res := runDeps(t, files, nil)
		expectAuthError(t, res, app)
		expectAuthError(t, res, "demo")
	})
	t.Run("namespaced credentials", func(t *testing.T) {
		// team/app must use the team entry; charts/demo falls back to the bad host entry.
		res := runDeps(t, files, map[string]cred{host + "/team": good, host: bad})
		expectUpdate(t, res, app, "1.0.0", "1.1.0")
		expectAuthError(t, res, "demo")
	})
}
