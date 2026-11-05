package npmds

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
)

// The decoders before they consumed members with ReadValue, kept to check that the results
// and errors did not change.

func decodePackumentBefore(r io.Reader) (p packument, deprecated int, err error) {
	dec := jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true))
	if err := expectObject(dec); err != nil {
		return packument{}, 0, err
	}
	seenVersions, seenTime, seenRepo := false, false, false
	for dec.PeekKind() != '}' {
		if seenVersions && seenTime && seenRepo {
			return p, deprecated, nil
		}
		key, err := dec.ReadToken()
		if err != nil {
			return packument{}, 0, err
		}
		switch name := key.String(); {
		case name == "versions":
			seenVersions = true
			p.rels, deprecated, err = decodeVersionsBefore(dec)
		case name == "time" && dec.PeekKind() == '{':
			seenTime = true
			p.times, err = decodeTimes(dec)
		case name == "repository":
			seenRepo = true
			p.repo, err = decodeRepository(dec)
		default:
			err = dec.SkipValue()
		}
		if err != nil {
			return packument{}, 0, err
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return packument{}, 0, err
	}
	return p, deprecated, nil
}

func decodeVersionsBefore(dec *jsontext.Decoder) (rels []datasource.Release, deprecated int, err error) {
	if err := expectObject(dec); err != nil {
		return nil, 0, err
	}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, 0, err
		}
		v := tok.String()
		var meta struct {
			Deprecated any `json:"deprecated"`
		}
		if err := json.UnmarshalDecode(dec, &meta); err != nil {
			return nil, 0, err
		}
		if _, gone := isDeprecated(meta.Deprecated); gone {
			deprecated++
			continue
		}
		rels = append(rels, datasource.Release{Version: v})
	}
	_, err = dec.ReadToken()
	return rels, deprecated, err
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
	p, dep, err := decodePackument(bytes.NewReader(in))
	want, wantDep, wantErr := decodePackumentBefore(bytes.NewReader(in))
	if !sameErr(err, wantErr) || !reflect.DeepEqual(p, want) || dep != wantDep {
		t.Fatalf("decodePackument(%q) = %+v, %d, %v; before %+v, %d, %v", in, p, dep, err, want, wantDep, wantErr)
	}
}

var parityInputs = []string{
	eslintDoc,
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
