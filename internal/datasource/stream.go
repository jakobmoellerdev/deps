package datasource

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// GetStream GETs url with client while holding a slot of sem (nil = unbounded) and passes
// the body of a 200 response to read, so large indexes are decoded as they arrive. Another
// status is a "GET <url>: HTTP <code>" error; errors of read get the "GET <url>: " prefix.
func GetStream(ctx context.Context, client *http.Client, sem chan struct{}, url string, read func(io.Reader) error) error {
	release, err := httpx.Acquire(ctx, sem)
	if err != nil {
		return err
	}
	defer release()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", req.URL.Redacted(), resp.StatusCode)
	}
	if err := read(resp.Body); err != nil {
		return fmt.Errorf("GET %s: %w", req.URL.Redacted(), err)
	}
	return nil
}

// ErrNotArray matches (errors.Is) the error of DecodeArray for input that does not start
// with a JSON array. Its message is the decoder's error, or "not an array" when the input
// starts with another value.
var ErrNotArray = errors.New("not an array")

// notArrayError is a decoder error at the start of the input; it reads as that error.
type notArrayError struct{ err error }

func (e notArrayError) Error() string   { return e.err.Error() }
func (e notArrayError) Unwrap() []error { return []error{e.err, ErrNotArray} }

// DecodeArray decodes the JSON array r one element at a time, calling each with every
// element, so indexes of large objects (file lists) are never held in memory at once. Each
// element is decoded into a zero T. Content after the closing ']' is not read.
func DecodeArray[T any](r io.Reader, each func(T)) error {
	dec := jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true)) // no per-object name sets
	tok, err := dec.ReadToken()
	if err != nil {
		return notArrayError{err}
	}
	if tok.Kind() != '[' {
		return ErrNotArray
	}
	for dec.PeekKind() != ']' {
		var v T
		if err := json.UnmarshalDecode(dec, &v); err != nil {
			return err
		}
		each(v)
	}
	return nil
}
