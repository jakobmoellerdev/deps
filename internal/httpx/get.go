package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// DefaultMaxBody is the body cap of a Getter whose MaxBody is 0: far above any registry
// document a datasource reads whole, small enough that a runaway response cannot exhaust
// memory.
const DefaultMaxBody = 64 << 20

// Getter sends the GET requests of a datasource: each one holds a slot of Sem, carries the
// deps User-Agent and whatever Prepare adds, and succeeds only with HTTP 200 (any 2xx with
// Accept2xx). Another status is a *StatusError; call sites decide what it means (a 404 or
// 410 as "not found", a 401 as "ask the credential helper") with errors.As or errors.Is
// ErrNotFound. A Getter is a value: copy it to vary Prepare or MaxBody per request.
type Getter struct {
	Client *http.Client
	// Sem bounds concurrent requests (nil = unbounded); a request holds its slot until its
	// body is closed.
	Sem chan struct{}
	// Prepare, if set, adds headers or credentials to each request (the User-Agent is already
	// set); an error aborts the request.
	Prepare func(*http.Request) error
	// MaxBody caps the bytes of a response body: reading past it fails with "response larger
	// than N bytes". 0 means DefaultMaxBody; a negative value is unbounded, for package
	// indexes (rpm primary.xml, deb Packages, Helm index.yaml) that run to hundreds of MB.
	MaxBody int64
	// Accept2xx accepts every 2xx status, not only 200.
	Accept2xx bool
}

// Open GETs target and returns the response. Its body reads at most MaxBody bytes; closing
// it drains a little (see DrainBody), closes it and frees the request slot. Another status
// than 200 is a *StatusError, with the body already closed.
func (g Getter) Open(ctx context.Context, target string) (*http.Response, error) {
	release, err := Acquire(ctx, g.Sem)
	if err != nil {
		return nil, err
	}
	resp, err := g.do(ctx, target)
	if err != nil {
		release()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && (!g.Accept2xx || resp.StatusCode < 200 || resp.StatusCode > 299) {
		DrainBody(resp)
		_ = resp.Body.Close()
		release()
		return nil, &StatusError{URL: Redact(target), Code: resp.StatusCode}
	}
	limit := g.MaxBody
	if limit == 0 {
		limit = DefaultMaxBody
	}
	resp.Body = &body{rc: resp.Body, left: limit, limit: limit, release: release}
	return resp, nil
}

func (g Getter) do(ctx context.Context, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		// A parse error quotes the URL, credentials included.
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return nil, fmt.Errorf("GET %s: %w", Redact(target), err)
	}
	req.Header.Set("User-Agent", UserAgent())
	if g.Prepare != nil {
		if err := g.Prepare(req); err != nil {
			return nil, err
		}
	}
	return g.Client.Do(req) // a *url.Error carries the URL without its password
}

// Stream GETs target (see Open) and passes the body to read, so large documents are decoded
// as they arrive. Errors of read, and a body larger than MaxBody even when read stopped at
// the cap without an error, get the "GET <url>: " prefix.
func (g Getter) Stream(ctx context.Context, target string, read func(io.Reader) error) error {
	resp, err := g.Open(ctx, target)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	err = read(resp.Body)
	if b, ok := resp.Body.(*body); ok && b.over {
		err = tooLargeError(b.limit) // whatever the decoder made of the cut-off stream
	}
	if err != nil {
		return fmt.Errorf("GET %s: %w", Redact(target), err)
	}
	return nil
}

// tooLargeError is the read error of a body longer than its cap.
type tooLargeError int64

func (e tooLargeError) Error() string {
	return "response larger than " + strconv.FormatInt(int64(e), 10) + " bytes"
}

// body is a response body of a Getter: it fails once more than limit bytes (negative = no
// limit) arrive, and Close frees the request slot.
type body struct {
	rc      io.ReadCloser
	left    int64 // bytes still allowed
	limit   int64
	over    bool
	release func()
}

func (b *body) Read(p []byte) (int, error) {
	if b.limit < 0 {
		return b.rc.Read(p)
	}
	if b.over {
		return 0, tooLargeError(b.limit)
	}
	if b.left == 0 {
		// The cap is reached: one more byte means the body is too large.
		var one [1]byte
		n, err := b.rc.Read(one[:])
		if n > 0 {
			b.over = true
			return 0, tooLargeError(b.limit)
		}
		return 0, err
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	return n, err
}

// Close drains a little of the body so the connection can be reused, closes it and frees the
// request slot; closing again only closes the underlying body again.
func (b *body) Close() error {
	_, _ = io.Copy(io.Discard, io.LimitReader(b.rc, 4<<10))
	err := b.rc.Close()
	if b.release != nil {
		b.release()
		b.release = nil
	}
	return err
}
