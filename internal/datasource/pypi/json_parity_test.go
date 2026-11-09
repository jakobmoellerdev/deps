package pypi

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
)

// The jsontext JSON API decoder that decodeJSON replaced, kept to check that the results did not
// change and that it fails exactly where its replacement does.

// decodeJSONText streams a PyPI JSON API document. `releases` maps each version to its files; one
// file object is buffered at a time, which keeps projects with thousands of releases (boto3)
// cheap. A version is dropped when it has no files or all of them are yanked.
// Duplicate-name checking is off: it keeps a name set per object.
func decodeJSONText(r io.Reader) (p project, yanked int, err error) {
	dec := jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true))
	if err := expectDelim(dec, '{'); err != nil {
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
				Classifiers []string          `json:"classifiers"`
			}
			if err := json.UnmarshalDecode(dec, &info); err != nil {
				return project{}, 0, err
			}
			p.repo = sourceRepo(info.ProjectURLs, info.HomePage)
			p.inactive = slices.Contains(info.Classifiers, "Development Status :: 7 - Inactive")
		case "releases":
			if yanked, err = decodeReleasesText(dec, &p); err != nil {
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

// decodeReleasesText reads the `releases` object (version -> files) into p.
func decodeReleasesText(dec *jsontext.Decoder, p *project) (yanked int, err error) {
	if err := expectDelim(dec, '{'); err != nil {
		return 0, err
	}
	var f releaseFileText
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return 0, err
		}
		v := tok.String() // tokens are only valid until the next read
		if err := expectDelim(dec, '['); err != nil {
			return 0, err
		}
		files, allYanked, first, py := 0, true, time.Time{}, pyReq{}
		for dec.PeekKind() != ']' {
			if err := decodeFileText(dec, &f); err != nil {
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
			if p.yanked == nil {
				p.yanked = map[string]bool{}
			}
			p.yanked[versionKey(v)] = true
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

// releaseFileText is what deps keeps of one file of a release (unnamed, as in unmarshal errors).
type releaseFileText = struct {
	Yanked         bool   `json:"yanked"`
	Upload         string `json:"upload_time_iso_8601"`
	RequiresPython string `json:"requires_python"`
}

// decodeFileText reads one file object of a release into f, which it resets first; f is reused so
// that unmarshaling its fields does not allocate. The kept members are unmarshaled one by one
// and the others consumed with ReadValue: unmarshaling the whole object into a struct looks up
// every unknown name (also case-folded) and skips nested values (`digests`) token by token.
func decodeFileText(dec *jsontext.Decoder, f *releaseFileText) error {
	*f = releaseFileText{}
	if dec.PeekKind() != '{' {
		return json.UnmarshalDecode(dec, f) // null, or the unmarshal error
	}
	if _, err := dec.ReadToken(); err != nil {
		return err
	}
	for dec.PeekKind() != '}' {
		name, err := dec.ReadToken()
		if err != nil {
			return err
		}
		switch name.String() {
		case "yanked":
			err = json.UnmarshalDecode(dec, &f.Yanked)
		case "upload_time_iso_8601":
			err = json.UnmarshalDecode(dec, &f.Upload)
		case "requires_python":
			err = json.UnmarshalDecode(dec, &f.RequiresPython)
		default:
			_, err = dec.ReadValue()
		}
		if err != nil {
			return err
		}
	}
	_, err := dec.ReadToken()
	return err
}

// checkJSONParity checks that decodeJSON and decodeJSONText agree on in: the same project, or
// both an error.
func checkJSONParity(t *testing.T, in []byte) {
	t.Helper()
	p, yanked, err := decodeJSON(bytes.NewReader(in))
	want, wantYanked, wantErr := decodeJSONText(bytes.NewReader(in))
	if (err == nil) != (wantErr == nil) || yanked != wantYanked || !reflect.DeepEqual(p, want) {
		t.Fatalf("decodeJSON(%q) = %+v, %d, %v; jsontext %+v, %d, %v", in, p, yanked, err, want, wantYanked, wantErr)
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
	`{"info":{"home_page":"https://github.com/a/b","classifiers":["Development Status :: 7 - Inactive"]},"releases":{"1.0":[{"yanked":false}]}}`,
	`{"info":null,"releases":{}} `,
	`{"info":{"project_urls":{"Source":"x"},"project_urls":null}}`,
	`{"info":{"home_page":1}}`,
	`{"info":{"home_page":"x"`,
	`{"releases":{"1.0":[{"yanked":false}]},"releases":{"2.0":[{"yanked":true}]}}`,
	`{"releases":{"1.0":[{"upload_time_iso_8601":"2020-01-01T00:00:00Z"},{"upload_time_iso_8601":"2019-01-01T00:00:00Z","yanked":true}]}}`,
	`{"releases":{"1.0":[{"requires_python":" >=3 "},{"requires_python":">=3"}],"1.1":[{"requires_python":">=3"},{"requires_python":""}]}}`,
	`{"releases":{"1\u002e0":[{}]}}`,
	`{"rel\u0065ases":{"1.0":[{"y\u0061nked":true}]}}`,
	`{"releases":{"1.0":[{"comment_text":"\ud83d\ude00 \"x\" \/ \b\f\n\r\t é"}]}}`,
	`{"releases":{"1.0":[{"comment_text":"\ud83d"}]}}`,
	`{"releases":{"1.0":[{"comment_text":"` + "\x01" + `"}]}}`,
	`{"releases":{"1.0":[{"comment_text":"` + "\xff" + `"}]}}`,
	`{"releases":{"1.0":[{"size":-0.5e+10,"downloads":-1,"n":[0,1.0,2E3,true,false,null,{}]}]}}`,
	`{"releases":{"1.0":[{"size":01}]}}`,
	`{"releases":{"1.0":[{"size":1.}]}}`,
	`{"releases":{"1.0":[{"size":-}]}}`,
	`{"releases":{"1.0":[{},]}}`,
	`{"releases":{"1.0":[,{}]}}`,
	`{"releases":{"1.0":[{}{}]}}`,
	`{"releases":{"1.0":[{"a" "b"}]}}`,
	`{"releases":{}} x`,
	`{"releases":{}}{}`,
	`{"a":` + strings.Repeat("[", 10001) + strings.Repeat("]", 10001) + `}`,
	`{"releases":{"1.0":{}}}`,
	"\t{ \"releases\" :\n{ \"1.0\" : [ { \"yanked\" : false } ] } }\r\n",
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
	checkJSONParity(t, syntheticJSON(50))
	doc := syntheticJSON(2)
	for n := range doc {
		checkJSONParity(t, doc[:n]) // truncated
	}
}

// TestDecodeJSONReadError checks that a read error reaches decodeJSON's caller.
func TestDecodeJSONReadError(t *testing.T) {
	boom := errors.New("boom")
	_, _, err := decodeJSON(io.MultiReader(bytes.NewReader(syntheticJSON(3)[:100]), iotest.ErrReader(boom)))
	if !errors.Is(err, boom) {
		t.Fatalf("decodeJSON error = %v, want %v", err, boom)
	}
}

func FuzzJSONParity(f *testing.F) {
	for _, in := range parityInputs {
		f.Add([]byte(in))
	}
	f.Add(syntheticJSON(3))
	f.Fuzz(checkJSONParity)
}
