package galaxy

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// ssoClientID is the client ansible-galaxy exchanges an offline token with at a Keycloak SSO
// (Red Hat SSO for Automation Hub).
const ssoClientID = "cloud-services"

// serverAuth is the token of a Galaxy server: sent as `Token <token>`, or, with an SSO
// endpoint (authURL), exchanged there for an access token sent as `Bearer <access token>`.
type serverAuth struct {
	host    string // the server's host, the only one the token is sent to
	token   string
	authURL string
}

// serverAuths reads the tokens of Galaxy servers from the environment: DEPS_GALAXY_TOKEN (with
// DEPS_GALAXY_AUTH_URL) for the default server, then the servers of ansible-galaxy's
// ANSIBLE_GALAXY_SERVER_LIST (`ANSIBLE_GALAXY_SERVER_<NAME>_URL`, `…_TOKEN`, `…_AUTH_URL`).
// Servers without URL or token are left out.
func serverAuths(getenv func(string) string, def string) []serverAuth {
	var out []serverAuth
	add := func(server, token, authURL string) {
		u, err := url.Parse(server)
		if err != nil || u.Host == "" || token == "" {
			return
		}
		out = append(out, serverAuth{host: strings.ToLower(u.Host), token: token, authURL: authURL})
	}
	add(def, getenv("DEPS_GALAXY_TOKEN"), getenv("DEPS_GALAXY_AUTH_URL"))
	for name := range strings.SplitSeq(getenv("ANSIBLE_GALAXY_SERVER_LIST"), ",") {
		if name = strings.ToUpper(strings.TrimSpace(name)); name == "" {
			continue
		}
		p := "ANSIBLE_GALAXY_SERVER_" + name + "_"
		add(getenv(p+"URL"), getenv(p+"TOKEN"), getenv(p+"AUTH_URL"))
	}
	return out
}

// authorization returns the Authorization header for a request to target: that of the first
// configured server on target's host ("" = none), exchanging an SSO token once per run.
func (s *Collections) authorization(ctx context.Context, target string) (string, error) {
	u, err := url.Parse(target)
	if err != nil {
		return "", err
	}
	host := strings.ToLower(u.Host)
	for _, a := range s.auths {
		if a.host != host {
			continue
		}
		if a.authURL == "" {
			return "Token " + a.token, nil
		}
		access, err := s.access.Do(ctx, a.authURL+"\x00"+a.token, func() (string, error) { return s.exchange(ctx, a) })
		if err != nil {
			return "", err
		}
		return "Bearer " + access, nil
	}
	return "", nil
}

// exchange trades a's offline token for an access token at its SSO endpoint, as
// ansible-galaxy's KeycloakToken does: `POST <auth_url>` with grant_type=refresh_token,
// client_id=cloud-services and the token as refresh_token.
func (s *Collections) exchange(ctx context.Context, a serverAuth) (string, error) {
	release, err := httpx.Acquire(ctx, s.getter.Sem)
	if err != nil {
		return "", err
	}
	defer release()
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {ssoClientID}, "refresh_token": {a.token}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.authURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("galaxy SSO %s: invalid URL", httpx.Redact(a.authURL))
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.getter.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("galaxy SSO: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("galaxy SSO %s: token exchange: HTTP %d", httpx.Redact(a.authURL), resp.StatusCode)
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.UnmarshalRead(io.LimitReader(resp.Body, 1<<20), &body); err != nil || body.AccessToken == "" {
		return "", fmt.Errorf("galaxy SSO %s: token exchange: no access_token in the response", httpx.Redact(a.authURL))
	}
	return body.AccessToken, nil
}
