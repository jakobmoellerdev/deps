package httpx

import (
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// A request counts from its start until its body is closed (once, however often Close is
// called); a failed request stops counting at once.
func TestHostCounter(t *testing.T) {
	var h HostCounter
	c := h.Wrap(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/fail" {
			return nil, errors.New("boom")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("x"))}, nil
	})})
	get := func(u string) *http.Response {
		t.Helper()
		resp, err := c.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	a1, a2, b := get("https://a.example:8443/x"), get("https://a.example/y"), get("https://b.example/z")
	if _, err := c.Get("https://b.example/fail"); err == nil {
		t.Fatal("no error")
	}
	if got, want := h.InFlight(), []HostUse{{"a.example", 2}, {"b.example", 1}}; !reflect.DeepEqual(got, want) {
		t.Errorf("InFlight = %v, want %v", got, want)
	}
	_ = a1.Body.Close()
	_ = a1.Body.Close()
	_ = b.Body.Close()
	if got, want := h.InFlight(), []HostUse{{"a.example", 1}}; !reflect.DeepEqual(got, want) {
		t.Errorf("InFlight = %v, want %v", got, want)
	}
	_ = a2.Body.Close()
	if got := h.InFlight(); len(got) != 0 {
		t.Errorf("idle: %v", got)
	}
}
