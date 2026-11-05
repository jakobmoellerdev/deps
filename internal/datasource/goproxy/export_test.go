package goproxy

import (
	"context"
	"net/http"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gotool"
)

// NewForProxy returns a Source on GOPROXY=proxy (without running `go env`) whose major path
// probe requests end after probeTimeout.
func NewForProxy(client *http.Client, proxy string, probeTimeout time.Duration) *Source {
	s := New(client, make(chan struct{}, 4))
	s.env = func(context.Context) (gotool.GoEnv, error) { return gotool.GoEnv{Proxy: proxy}, nil }
	s.probeTimeout = probeTimeout
	return s
}
