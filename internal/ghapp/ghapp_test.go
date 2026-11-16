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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestLoadKeyBothPEMForms(t *testing.T) {
	key := testKey(t)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	for name, b := range map[string][]byte{"pkcs1": pkcs1, "pkcs8": pkcs8} {
		got, err := LoadKey(b)
		if err != nil || !got.Equal(key) {
			t.Errorf("%s: LoadKey = %v", name, err)
		}
	}
	if _, err := LoadKey([]byte("not a key")); err == nil {
		t.Error("LoadKey accepted a non-PEM input")
	}
}

// TestJWTVerifies checks the assertion against the public key, the way GitHub does.
func TestJWTVerifies(t *testing.T) {
	key := testKey(t)
	a := &App{ID: 123456, PrivateKey: key}
	now := time.Now()
	tok, err := a.JWT(now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT has %d parts", len(parts))
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature: %v", err)
	}
	var claims struct {
		IAT, EXP int64
		ISS      string
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.ISS != "123456" {
		t.Errorf("iss = %q", claims.ISS)
	}
	if claims.IAT > now.Unix() || claims.EXP-claims.IAT > 600 {
		t.Errorf("iat/exp = %d/%d; GitHub rejects an expiry more than 10 minutes out", claims.IAT, claims.EXP)
	}
}

// memCache is a TokenCache shared between two Apps, as Valkey is between two replicas.
type memCache struct {
	mu      sync.Mutex
	entries map[string]cached
	gets    int
}

func (c *memCache) Get(_ context.Context, key string) (string, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	e, ok := c.entries[key]
	return e.token, e.exp, ok
}

func (c *memCache) Put(_ context.Context, key, token string, exp time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]cached{}
	}
	c.entries[key] = cached{token: token, exp: exp}
	return nil
}

// tokenServer answers access_tokens with a counter, so reuse is visible.
func tokenServer(t *testing.T, life time.Duration) (*httptest.Server, *int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/access_tokens") || r.Method != http.MethodPost {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "installation tokens need the app JWT", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		n++
		token := fmt.Sprintf("ghs_token%d", n)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": token, "expires_at": time.Now().Add(life).Format(time.RFC3339),
		})
	}))
	t.Cleanup(s.Close)
	return s, &n
}

func TestInstallationTokenIsCachedAndShared(t *testing.T) {
	s, minted := tokenServer(t, time.Hour)
	cache := &memCache{}
	key := testKey(t)
	app := func() *App {
		return &App{ID: 7, PrivateKey: key, APIURL: s.URL, Client: s.Client(), Cache: cache, MinLifetime: 30 * time.Minute}
	}
	a := app()
	tok, exp, err := a.InstallationToken(t.Context(), 42)
	if err != nil || tok != "ghs_token1" {
		t.Fatalf("token = %q, %v", tok, err)
	}
	if time.Until(exp) < 50*time.Minute {
		t.Errorf("expiry = %v", exp)
	}
	// The same App answers from its own memory, without touching the shared cache again.
	gets := cache.gets
	if tok, _, err := a.InstallationToken(t.Context(), 42); err != nil || tok != "ghs_token1" || cache.gets != gets {
		t.Errorf("second call = %q, %v, %d cache reads", tok, err, cache.gets-gets)
	}
	// Another replica reads it from the shared cache instead of minting a second one.
	if tok, _, err := app().InstallationToken(t.Context(), 42); err != nil || tok != "ghs_token1" {
		t.Errorf("other replica = %q, %v", tok, err)
	}
	if *minted != 1 {
		t.Errorf("minted %d tokens, want 1", *minted)
	}
}

// A token whose remaining life is shorter than a cycle must not be handed out: the cycle
// would lose its credential halfway through and fail its pushes.
func TestInstallationTokenRefreshesBeforeItExpires(t *testing.T) {
	s, minted := tokenServer(t, 20*time.Minute)
	a := &App{ID: 7, PrivateKey: testKey(t), APIURL: s.URL, Client: s.Client(), Cache: &memCache{}, MinLifetime: 35 * time.Minute}
	for range 3 {
		if _, _, err := a.InstallationToken(t.Context(), 42); err != nil {
			t.Fatal(err)
		}
	}
	if *minted != 3 {
		t.Errorf("minted %d tokens, want one per call: a 20 minute token is too short for a 35 minute cycle", *minted)
	}
}
