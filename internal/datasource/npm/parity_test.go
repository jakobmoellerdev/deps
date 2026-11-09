package npmds

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
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jsonscan"
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

// sameErrText reports whether err and want read the same; encoding/json/v2 randomly says
// "cannot" or "unable to" in semantic errors. Syntax errors only need to agree on the offset:
// how jsontext quotes the bad input and how much of the JSON pointer it knows depends on
// what its buffer held, so on how the input arrived.
func sameErrText(err, want error) bool {
	syn, ok := errors.AsType[*jsontext.SyntacticError](err)
	wantSyn, wantOk := errors.AsType[*jsontext.SyntacticError](want)
	if ok || wantOk {
		return ok && wantOk && syn.ByteOffset == wantSyn.ByteOffset
	}
	norm := func(err error) string { return strings.ReplaceAll(fmt.Sprint(err), "unable to", "cannot") }
	return norm(err) == norm(want)
}

// checkMetadataParity checks that decodeMetadata returns what decodeMetadataTokens does, errors
// included, reading in through the readers wrap makes.
func checkMetadataParity(t *testing.T, in []byte, wrap func(io.Reader) io.Reader) {
	t.Helper()
	m, dep, err := decodeMetadata(wrap(bytes.NewReader(in)))
	want, wantDep, wantErr := decodeMetadataTokens(wrap(bytes.NewReader(in)))
	if !sameErrText(err, wantErr) || !reflect.DeepEqual(m, want) || dep != wantDep {
		t.Fatalf("decodeMetadata(%.300q) = %+v, %d, %v; tokens %+v, %d, %v", in, m, dep, err, want, wantDep, wantErr)
	}
}

// failingReader reads r and then fails with errBroken instead of io.EOF.
type failingReader struct{ r io.Reader }

var errBroken = errors.New("connection reset")

func (f failingReader) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if errors.Is(err, io.EOF) {
		err = errBroken
	}
	return n, err
}

var readerWraps = map[string]func(io.Reader) io.Reader{
	"whole":    func(r io.Reader) io.Reader { return r },
	"one byte": iotest.OneByteReader,
	"data err": iotest.DataErrReader,
	"failing":  func(r io.Reader) io.Reader { return failingReader{r} },
}

var metadataParityInputs = []string{
	`{"name":"x","dist-tags":{"latest":"1.0.0","next":"2.0.0-rc.1"},"versions":{"1.0.0":{"name":"x","dist":{"n":-1.5e+3,"a":[0,true,false,null,{}]}},"2.0.0-rc.1":{}},"modified":"2026-01-01T00:00:00.000Z"}`,
	` { "dist-tags" : { "latest" : "1.0.0" , "latest" : 1 } , "versions" : { "1.0.0" : { "deprecated" : "x" } , "1.1.0" : null } , "modified" : "bad" } `,
	`{"dist-tags":[],"modified":1,"versions":{"1.0.0":{}},"versions":{"2.0.0":{}},"dist-tags":{"lat\u0065st":"\u00e9\ud83d\ude00"}}`,
	`{"vers\u0069ons":{"1.\u0030.0":{"deprec\u0061ted":"a\nb"},"é":{"d":"日本"}},"modified":"2026-01-01T00:00:00\u002e000Z"}`,
	`{"versions":{"1.0.0":{"deprecated":"x"}},"versions":{"1.0.0":{"deprecated":"x","deprecated":1e400}}}`,
	`{"versions":{"1.0.0":{"deprecated":{"a":1},"deprecated":"x"}}}`,
	`{"versions":{"1.0.0":{"deprecated":[1],"deprecated":"y"},"2":{"deprecated":true,"deprecated":"y"}}}`,
	`{"versions":{"1.0.0":{"deprecated":1e400}}}`,
	`{"versions":{"1.0.0":{"deprecated":0}},"modified":"x"}`,
	`{"versions":{"a~/b":12}}`,
	`{"versions":{"1":"x"}}`,
	`{"versions":"x"}`,
	`{"versions":1x}`,
	`{"versions":[}`,
	`{"versions":nul}`,
	`{"a":1,}`,
	`{"a":1 "b":2}`,
	`{"versions":{"1":{},}}`,
	`{"versions":{"1":{}}x`,
	`{"versions":{"1":{"a":"\ud800"}}}`,
	`{"versions":{"1":{"a":"\ud800\u0041"}}}`,
	`{"versions":{"1":{"a":"\udc00\udc00"}}}`,
	`{"versions":{"1":{"a":"\x"}}}`,
	"{\"versions\":{\"1\":{\"a\":\"\x01\"}}}",
	"{\"versions\":{\"1\":{\"a\":\"\xff\"}}}",
	"{\"versions\":{\"1\":{\"a\":\"\xed\xa0\x80\"}}}",
	`{"versions":{"1":{"a":01}}}`,
	`{"versions":{"1":{"a":-}}}`,
	`{"versions":{"1":{"a":1.}}}`,
	`{"versions":{"1":{"a":1e}}}`,
	`{"versions":{"1":{"a":[1,]}}}`,
	`{"versions":{"1":{"a":{"b"}}}}`,
	`{"versions":{"1":{"a":{1:2}}}}`,
	`{"versions":{"1":{"a":]}}}`,
	`{"versions":{"1":{"a":tru}}}`,
	`{"versions":{"1":{"a":[}]}}}`,
	`{}`,
	`[]`,
	`"x`,
	`1`,
	`  `,
	``,
	`x`,
}

func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeMetadataParity(t *testing.T) {
	docs := [][]byte{readFixture(t, "lodash.json"), syntheticPackument(60, false), []byte(eslintDoc)}
	for _, in := range metadataParityInputs {
		docs = append(docs, []byte(in))
	}
	// Real documents of busy packages, when fetched to NPM_PARITY_DIR (see the README of the
	// perf PR): `curl -H 'Accept: application/vnd.npm.install-v1+json' ...`.
	if dir := os.Getenv("NPM_PARITY_DIR"); dir != "" {
		files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			docs = append(docs, b)
		}
	}
	for name, wrap := range readerWraps {
		t.Run(name, func(t *testing.T) {
			for _, in := range docs {
				checkMetadataParity(t, in, wrap)
			}
		})
	}
	// Truncated at every byte, and nested past jsontext's depth limit.
	doc := syntheticPackument(3, false)
	for n := range doc {
		checkMetadataParity(t, doc[:n], readerWraps["whole"])
		checkMetadataParity(t, doc[:n], readerWraps["failing"])
	}
	for depth := jsonscan.MaxDepth - 5; depth <= jsonscan.MaxDepth+1; depth++ {
		in := `{"versions":{"1":{"a":` + strings.Repeat("[", depth-3) + strings.Repeat("]", depth-3) + `}}}`
		checkMetadataParity(t, []byte(in), readerWraps["whole"])
	}
}

func TestDecodeMetadataLodash(t *testing.T) {
	m, dep, err := decodeMetadata(bytes.NewReader(readFixture(t, "lodash.json")))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.rels) != 116 || dep != 1 || m.deprecated["4.18.0"] != "Bad release. Please use lodash@4.17.21 instead." ||
		m.latest != "4.18.1" || !m.modified.Equal(time.Date(2026, 11, 9, 0, 39, 3, 555e6, time.UTC)) {
		t.Fatalf("decodeMetadata(lodash) = %d releases, %v, %q, %v", len(m.rels), m.deprecated, m.latest, m.modified)
	}
}

func FuzzDecodeMetadataParity(f *testing.F) {
	for _, in := range metadataParityInputs {
		f.Add([]byte(in))
	}
	f.Add(syntheticPackument(3, false))
	f.Fuzz(func(t *testing.T, in []byte) {
		checkMetadataParity(t, in, readerWraps["whole"])
		checkMetadataParity(t, in, readerWraps["one byte"])
	})
}
