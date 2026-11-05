package githubapi

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		switch r.URL.RequestURI() {
		case "/repos/o/r/contents/dir/a%20b.json?ref=v1%2B1":
			// GitHub wraps base64 content at 60 columns.
			_, _ = io.WriteString(w, `{"type":"file","encoding":"base64","content":"eyJleHRlbmRzIjpb\nXX0=\n"}`)
		case "/repos/o/r/contents/dir":
			_, _ = io.WriteString(w, `[{"type":"file"}]`)
		case "/repos/o/r/contents/sub":
			_, _ = io.WriteString(w, `{"type":"submodule"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(srv.Client(), tokenAuth(), make(chan struct{}, 1), WithAPI(srv.URL, srv.URL+"/graphql"))

	b, err := c.File(t.Context(), oR, "dir/a b.json", "v1+1")
	if err != nil || string(b) != `{"extends":[]}` {
		t.Errorf("File = %q, %v", b, err)
	}
	if _, err := c.File(t.Context(), oR, "missing.json", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: err = %v, want ErrNotFound", err)
	}
	if _, err := c.File(t.Context(), oR, "sub", ""); err == nil || err.Error() != "o/r: sub is not a file" {
		t.Errorf("submodule: err = %v", err)
	}
	if _, err := c.File(t.Context(), oR, "dir", ""); err == nil {
		t.Error("directory listing: want error")
	}
}
