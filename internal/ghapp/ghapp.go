// Package ghapp mints GitHub App installation tokens.
//
// A static token's API budget is per account and does not grow with the number of replicas,
// so it is the real ceiling of a fleet that scans many repositories; an App installation has
// its own budget per organization, which is the only way to get more. deps uses the token
// exactly as it uses a static one, including the platform-commit path, which
// platform.IsAppToken already recognizes.
package ghapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// App is one GitHub App: its id, its private key and the API it talks to (api.github.com, or
// the /api/v3 of a GitHub Enterprise Server).
type App struct {
	ID         int64
	PrivateKey *rsa.PrivateKey
	APIURL     string
	Client     *http.Client
	// Cache shares installation tokens between replicas; nil keeps them in this process only.
	Cache TokenCache
	// MinLifetime is how long a cached token must still be valid to be handed out. A cycle
	// that outlives its token would fail its pushes halfway, so callers set this to more than
	// one cycle may take.
	MinLifetime time.Duration

	mu    sync.Mutex
	local map[int64]cached
}

type cached struct {
	token string
	exp   time.Time
}

// TokenCache is where installation tokens are shared; the Valkey store implements it.
type TokenCache interface {
	Get(ctx context.Context, key string) (token string, exp time.Time, ok bool)
	Put(ctx context.Context, key, token string, exp time.Time) error
}

// Installation is one installation of the App.
type Installation struct {
	ID      int64  `json:"id"`
	Account string `json:"-"`
	Raw     struct {
		Login string `json:"login"`
	} `json:"account"`
}

// defaultMinLifetime is how much of a token's hour must be left for it to be reused.
const defaultMinLifetime = 10 * time.Minute

// LoadKey parses a PEM private key as GitHub hands it out (PKCS#1 "RSA PRIVATE KEY", or
// PKCS#8 "PRIVATE KEY").
func LoadKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	for rest := pemBytes; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, errors.New("no PEM private key found")
		}
		switch block.Type {
		case "RSA PRIVATE KEY":
			return x509.ParsePKCS1PrivateKey(block.Bytes)
		case "PRIVATE KEY":
			key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				return nil, err
			}
			rsaKey, ok := key.(*rsa.PrivateKey)
			if !ok {
				return nil, fmt.Errorf("private key is %T, GitHub Apps use RSA", key)
			}
			return rsaKey, nil
		}
	}
}

// JWT is the App's bearer assertion, signed RS256 as GitHub requires. It is backdated a
// minute against clock skew and expires well inside GitHub's ten minute limit.
func (a *App) JWT(now time.Time) (string, error) {
	header := `{"alg":"RS256","typ":"JWT"}`
	claims := fmt.Sprintf(`{"iat":%d,"exp":%d,"iss":"%d"}`,
		now.Add(-60*time.Second).Unix(), now.Add(9*time.Minute).Unix(), a.ID)
	signing := b64(header) + "." + b64(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.PrivateKey, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// InstallationToken returns a token for installation, from the process, from the shared
// cache, or newly minted. The expiry is returned so the caller can see how long it has.
func (a *App) InstallationToken(ctx context.Context, installation int64) (string, time.Time, error) {
	min := a.MinLifetime
	if min <= 0 {
		min = defaultMinLifetime
	}
	key := a.cacheKey(installation)
	now := time.Now()
	a.mu.Lock()
	c, ok := a.local[installation]
	a.mu.Unlock()
	if ok && c.exp.Sub(now) > min {
		return c.token, c.exp, nil
	}
	if a.Cache != nil {
		if token, exp, ok := a.Cache.Get(ctx, key); ok && exp.Sub(now) > min {
			a.remember(installation, token, exp)
			return token, exp, nil
		}
	}
	token, exp, err := a.mint(ctx, installation)
	if err != nil {
		return "", time.Time{}, err
	}
	a.remember(installation, token, exp)
	if a.Cache != nil {
		// A cache failure only costs a mint next time.
		_ = a.Cache.Put(ctx, key, token, exp)
	}
	return token, exp, nil
}

// cacheKey is where the token of installation lives in the shared cache.
func (a *App) cacheKey(installation int64) string {
	return "deps:tok:" + strconv.FormatInt(a.ID, 10) + ":" + strconv.FormatInt(installation, 10)
}

func (a *App) remember(installation int64, token string, exp time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.local == nil {
		a.local = map[int64]cached{}
	}
	a.local[installation] = cached{token: token, exp: exp}
}

// mint asks GitHub for a fresh installation token.
func (a *App) mint(ctx context.Context, installation int64) (string, time.Time, error) {
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	path := "/app/installations/" + strconv.FormatInt(installation, 10) + "/access_tokens"
	if err := a.do(ctx, http.MethodPost, path, &out); err != nil {
		return "", time.Time{}, err
	}
	if out.Token == "" {
		return "", time.Time{}, fmt.Errorf("installation %d: GitHub returned no token", installation)
	}
	return out.Token, out.ExpiresAt, nil
}

// Installations lists every installation of the App, paged.
func (a *App) Installations(ctx context.Context) ([]Installation, error) {
	var out []Installation
	for page := 1; page <= maxPages; page++ {
		var batch []Installation
		if err := a.do(ctx, http.MethodGet, "/app/installations?per_page=100&page="+strconv.Itoa(page), &batch); err != nil {
			return nil, err
		}
		for i := range batch {
			batch[i].Account = batch[i].Raw.Login
		}
		out = append(out, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return out, nil
}

// Repositories lists the repositories of installation as owner/name, using its own token.
func (a *App) Repositories(ctx context.Context, installation int64) ([]string, error) {
	token, _, err := a.InstallationToken(ctx, installation)
	if err != nil {
		return nil, err
	}
	var out []string
	for page := 1; page <= maxPages; page++ {
		var batch struct {
			Repositories []struct {
				FullName string `json:"full_name"`
				Archived bool   `json:"archived"`
				Fork     bool   `json:"fork"`
			} `json:"repositories"`
		}
		url := a.api() + "/installation/repositories?per_page=100&page=" + strconv.Itoa(page)
		if err := a.request(ctx, http.MethodGet, url, "token "+token, &batch); err != nil {
			return nil, err
		}
		for _, r := range batch.Repositories {
			// Archived repositories cannot be pushed to, as platform.ListRepos also skips.
			if !r.Archived && r.FullName != "" {
				out = append(out, r.FullName)
			}
		}
		if len(batch.Repositories) < 100 {
			break
		}
	}
	return out, nil
}

// maxPages bounds the listings, as platform does.
const maxPages = 50

func (a *App) api() string {
	if a.APIURL == "" {
		return "https://api.github.com"
	}
	return strings.TrimSuffix(a.APIURL, "/")
}

func (a *App) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return http.DefaultClient
}

// do sends an App-authenticated (JWT) request.
func (a *App) do(ctx context.Context, method, path string, out any) error {
	jwt, err := a.JWT(time.Now())
	if err != nil {
		return err
	}
	return a.request(ctx, method, a.api()+path, "Bearer "+jwt, out)
}

func (a *App) request(ctx context.Context, method, url, auth string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", httpx.UserAgent())
	resp, err := a.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		httpx.DrainBody(resp)
		return &httpx.StatusError{URL: httpx.Redact(url), Code: resp.StatusCode}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
