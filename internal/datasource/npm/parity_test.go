package npmds

import (
	"bytes"
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
)

// The decoders before they consumed members with ReadValue, kept to check that the results
// and errors did not change.

func decodeDocBefore(r io.Reader) (times map[string]string, repo string, err error) {
	dec := jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true)) // see decodeVersions
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return nil, "", cmp.Or(err, errors.New("not an object"))
	}
	seenTime, seenRepo := false, false
	for dec.PeekKind() != '}' && !(seenTime && seenRepo) {
		key, err := dec.ReadToken()
		if err != nil {
			return nil, "", err
		}
		switch name := key.String(); {
		case name == "time" && dec.PeekKind() == '{':
			seenTime = true
			if times, err = decodeTimes(dec); err != nil {
				return nil, "", err
			}
		case name == "repository":
			seenRepo = true
			v, err := dec.ReadValue()
			if err != nil {
				return nil, "", err
			}
			var ref struct {
				URL string `json:"url"`
			}
			if v.Kind() == '"' {
				err = json.Unmarshal(v, &repo)
			} else if v.Kind() == '{' {
				err = json.Unmarshal(v, &ref)
				repo = ref.URL
			}
			if err != nil {
				return nil, "", err
			}
		default:
			if err := dec.SkipValue(); err != nil {
				return nil, "", err
			}
		}
	}
	return times, repo, nil
}
func decodeVersionsBefore(r io.Reader) (rels []datasource.Release, deprecated int, modified time.Time, err error) {
	dec := jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true))
	if err := expectDelimBefore(dec, '{'); err != nil {
		return nil, 0, time.Time{}, err
	}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, 0, time.Time{}, err
		}
		switch tok.String() {
		case "versions": // decoded below
		case "modified":
			if dec.PeekKind() != '"' {
				if err := dec.SkipValue(); err != nil {
					return nil, 0, time.Time{}, err
				}
				continue
			}
			v, err := dec.ReadToken()
			if err != nil {
				return nil, 0, time.Time{}, err
			}
			modified, _ = time.Parse(time.RFC3339, v.String()) // unparsable = unknown bound
			continue
		default:
			if err := dec.SkipValue(); err != nil {
				return nil, 0, time.Time{}, err
			}
			continue
		}
		if err := expectDelimBefore(dec, '{'); err != nil {
			return nil, 0, time.Time{}, err
		}
		for dec.PeekKind() != '}' {
			tok, err := dec.ReadToken()
			if err != nil {
				return nil, 0, time.Time{}, err
			}
			v := tok.String() // tokens are only valid until the next read
			var meta struct {
				Deprecated any `json:"deprecated"`
			}
			if err := json.UnmarshalDecode(dec, &meta); err != nil {
				return nil, 0, time.Time{}, err
			}
			if isDeprecated(meta.Deprecated) {
				deprecated++
				continue
			}
			rels = append(rels, datasource.Release{Version: v})
		}
		if _, err := dec.ReadToken(); err != nil {
			return nil, 0, time.Time{}, err
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, 0, time.Time{}, err
	}
	return rels, deprecated, modified, nil
}

func expectDelimBefore(dec *jsontext.Decoder, d jsontext.Kind) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != d {
		return fmt.Errorf("expected %v, got %v", d, tok.Kind())
	}
	return nil
}

// sameErr reports whether err and want agree. Syntax errors in malformed JSON only need to agree
// on being syntax errors: ReadValue reports some of them at a different offset than the
// token-by-token SkipValue did.
func sameErr(err, want error) bool {
	var syn, wantSyn *jsontext.SyntacticError
	if errors.As(err, &syn) && errors.As(want, &wantSyn) {
		return true
	}
	return fmt.Sprint(err) == fmt.Sprint(want)
}

func checkDecodeParity(t *testing.T, in []byte) {
	t.Helper()
	rels, dep, mod, err := decodeVersions(bytes.NewReader(in))
	wantRels, wantDep, wantMod, wantErr := decodeVersionsBefore(bytes.NewReader(in))
	if !sameErr(err, wantErr) || !reflect.DeepEqual(rels, wantRels) || dep != wantDep || !mod.Equal(wantMod) {
		t.Fatalf("decodeVersions(%q) = %v, %d, %v, %v; before %v, %d, %v, %v", in, rels, dep, mod, err, wantRels, wantDep, wantMod, wantErr)
	}
	times, repo, err := decodeDoc(bytes.NewReader(in))
	wantTimes, wantRepo, wantErr := decodeDocBefore(bytes.NewReader(in))
	if !sameErr(err, wantErr) || !reflect.DeepEqual(times, wantTimes) || repo != wantRepo {
		t.Fatalf("decodeDoc(%q) = %v, %q, %v; before %v, %q, %v", in, times, repo, err, wantTimes, wantRepo, wantErr)
	}
}

var parityInputs = []string{
	packument,
	`{"versions":{"1.0.0":{},"1.1.0":{"deprecated":"use 2"},"1.2.0":{"deprecated":""},"1.3.0":{"deprecated":true},"1.4.0":{"deprecated":false}}}`,
	`{"versions":{"1.0.0":{"deprecated":null},"1.1.0":{"deprecated":{"a":1}},"1.2.0":{"deprecated":1},"1.3.0":{"deprecated":[true]}}}`,
	`{"versions":{"1.0.0":{"deprecated":"x","deprecated":true},"1.1.0":{"deprecated":true,"deprecated":null},"1.2.0":{"deprecated":"x","deprecated":""}}}`,
	`{"versions":{"1.0.0":null,"1.1.0":"x","1.2.0":[]}}`,
	`{"versions":{"1.0.0":{"dist":{"x":[1,2,{"y":null}]}},"1.1.0":{"dist":{"x":[1,2,}}}}`,
	`{"versions":{"1.0.0":{"dist":{"x":1}}`,
	`{"time":[],"versions":{},"repository":"r","time":{"1.0.0":"t"}}`,
	`{"repository":{"url":1}}`,
	`{"modified":"2026-01-01T00:00:00Z","versions":{"1.0.0":{"a":tru}}}`,
	`[]`,
	``,
}

func TestDecodeParity(t *testing.T) {
	for _, in := range parityInputs {
		checkDecodeParity(t, []byte(in))
	}
	for _, full := range []bool{false, true} {
		doc := syntheticPackument(50, full)
		checkDecodeParity(t, doc)
		for _, n := range []int{1, 100, 1000, len(doc) / 2, len(doc) - 1} {
			checkDecodeParity(t, doc[:n]) // truncated
		}
	}
}

func FuzzDecodeParity(f *testing.F) {
	for _, in := range parityInputs {
		f.Add([]byte(in))
	}
	f.Add(syntheticPackument(3, false))
	f.Add(syntheticPackument(3, true))
	f.Fuzz(checkDecodeParity)
}
