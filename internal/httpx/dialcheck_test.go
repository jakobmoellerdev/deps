package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

// A dial check sees the address connected to and refuses the connection with its error.
func TestDialCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	c := NewClient()
	refused := errors.New("refused")
	var seen netip.Addr
	ctx := WithDialCheck(t.Context(), func(a netip.Addr) error { seen = a; return refused })
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
	if _, err := c.Do(req); !errors.Is(err, refused) {
		t.Errorf("err = %v, want the check's error", err)
	}
	if !seen.IsLoopback() {
		t.Errorf("checked %v, want the loopback address", seen)
	}
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unchecked request: %v", err)
	}
	resp.Body.Close()
}
