package datasource

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
)

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
