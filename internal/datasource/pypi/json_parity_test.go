package pypi

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
)

// The JSON API decoder before it read file objects member by member, kept to check that the
// results and errors did not change.

func decodeJSONBefore(r io.Reader) (p project, yanked int, err error) {
	dec := jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true))
	if err := expectDelimBefore(dec, '{'); err != nil {
		return project{}, 0, err
	}
	p.times, p.python = map[string]time.Time{}, map[string]string{}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return project{}, 0, err
		}
		switch tok.String() {
		case "info":
			var info struct {
				HomePage    string            `json:"home_page"`
				ProjectURLs map[string]string `json:"project_urls"`
			}
			if err := json.UnmarshalDecode(dec, &info); err != nil {
				return project{}, 0, err
			}
			p.repo = sourceRepo(info.ProjectURLs, info.HomePage)
		case "releases":
			if yanked, err = decodeReleasesBefore(dec, &p); err != nil {
				return project{}, 0, err
			}
		default:
			if err := dec.SkipValue(); err != nil {
				return project{}, 0, err
			}
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return project{}, 0, err
	}
	return p, yanked, nil
}

// decodeReleases reads the `releases` object (version -> files) into p.
func decodeReleasesBefore(dec *jsontext.Decoder, p *project) (yanked int, err error) {
	if err := expectDelimBefore(dec, '{'); err != nil {
		return 0, err
	}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return 0, err
		}
		v := tok.String() // tokens are only valid until the next read
		if err := expectDelimBefore(dec, '['); err != nil {
			return 0, err
		}
		files, allYanked, first, py := 0, true, time.Time{}, pyReq{}
		for dec.PeekKind() != ']' {
			var f struct {
				Yanked         bool   `json:"yanked"`
				Upload         string `json:"upload_time_iso_8601"`
				RequiresPython string `json:"requires_python"`
			}
			if err := json.UnmarshalDecode(dec, &f); err != nil {
				return 0, err
			}
			files++
			allYanked = allYanked && f.Yanked
			first = earliest(first, f.Upload)
			if !f.Yanked {
				py.add(f.RequiresPython)
			}
		}
		if _, err := dec.ReadToken(); err != nil {
			return 0, err
		}
		switch {
		case files == 0:
			continue
		case allYanked:
			yanked++
			continue
		}
		p.rels = append(p.rels, datasource.Release{Version: v})
		k := versionKey(v)
		if !first.IsZero() {
			p.times[k] = first
		}
		if spec, ok := py.get(); ok {
			p.python[k] = spec
		}
	}
	_, err = dec.ReadToken()
	return yanked, err
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

func checkJSONParity(t *testing.T, in []byte) {
	t.Helper()
	p, yanked, err := decodeJSON(bytes.NewReader(in))
	want, wantYanked, wantErr := decodeJSONBefore(bytes.NewReader(in))
	p.yanked, p.inactive = nil, false // deprecation data the earlier decoder did not keep
	if !sameErr(err, wantErr) || yanked != wantYanked || !reflect.DeepEqual(p, want) {
		t.Fatalf("decodeJSON(%q) = %+v, %d, %v; before %+v, %d, %v", in, p, yanked, err, want, wantYanked, wantErr)
	}
}

var parityInputs = []string{
	`{"releases":{"1.0":[{"yanked":true},{"yanked":false,"requires_python":">=3.8"}],"1.1":[{"yanked":true}],"1.2":[]}}`,
	`{"releases":{"1.0":[null,{"yanked":null,"upload_time_iso_8601":null,"requires_python":null}]}}`,
	`{"releases":{"1.0":[{"yanked":true,"yanked":false},{"requires_python":">=3","requires_python":null}]}}`,
	`{"releases":{"1.0":[{"Yanked":true,"REQUIRES_PYTHON":">=3"}]}}`,
	`{"releases":{"1.0":[{"yanked":"true"}]}}`,
	`{"releases":{"1.0":[{"upload_time_iso_8601":1}]}}`,
	`{"releases":{"1.0":[{"requires_python":{"a":1}}]}}`,
	`{"releases":{"1.0":["x"]}}`,
	`{"releases":{"1.0":[{"digests":{"sha256":[1,2,}}}]}}`,
	`{"releases":{"1.0":[{"digests":{"sha256":"x"}`,
	`{"releases":{"1.0":[{"yanked":tru}]}}`,
	`{"releases":{"1.0":[{"upload_time_iso_8601":"2020-01-01T00:00:00Z","requires_python":"\u003e=3"}]}}`,
	`[]`,
	``,
}

func TestJSONParity(t *testing.T) {
	for _, in := range parityInputs {
		checkJSONParity(t, []byte(in))
	}
	fixtures, err := filepath.Glob("testdata/*.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		checkJSONParity(t, b)
	}
	doc := syntheticJSON(50)
	checkJSONParity(t, doc)
	for _, n := range []int{1, 100, 1000, len(doc) / 2, len(doc) - 1} {
		checkJSONParity(t, doc[:n]) // truncated
	}
}

func FuzzJSONParity(f *testing.F) {
	for _, in := range parityInputs {
		f.Add([]byte(in))
	}
	f.Add(syntheticJSON(3))
	f.Fuzz(checkJSONParity)
}
