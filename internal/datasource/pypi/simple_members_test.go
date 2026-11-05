package pypi

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

// decodeSimpleStruct is decodeSimple as it unmarshaled each file into a struct, the oracle
// for the member-by-member decodeSimpleFile.
func decodeSimpleStruct(r io.Reader, name string) (p project, yanked int, err error) {
	dec := jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true))
	if err := expectDelim(dec, '{'); err != nil {
		return project{}, 0, err
	}
	pg := newSimplePage(name)
	var versions []string
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return project{}, 0, err
		}
		switch tok.String() {
		case "files":
			if err := expectDelim(dec, '['); err != nil {
				return project{}, 0, err
			}
			for dec.PeekKind() != ']' {
				var f struct {
					Filename       string         `json:"filename"`
					Yanked         jsontext.Value `json:"yanked"`
					Upload         string         `json:"upload-time"`
					RequiresPython string         `json:"requires-python"`
				}
				if err := json.UnmarshalDecode(dec, &f); err != nil {
					return project{}, 0, err
				}
				k := f.Yanked.Kind()
				pg.add(simpleFile{Filename: f.Filename, Upload: f.Upload, RequiresPython: f.RequiresPython, Yanked: k == 't' || k == '"'})
			}
			if _, err := dec.ReadToken(); err != nil {
				return project{}, 0, err
			}
		case "versions":
			versions = []string{}
			if err := json.UnmarshalDecode(dec, &versions); err != nil {
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
	p, yanked = pg.project(versions)
	return p, yanked, nil
}

var simpleMembersSeeds = []string{
	`{"files":[null,{"filename":"x-1.0.tar.gz","yanked":"bad","hashes":{"sha256":"ab"}},{"filename":"x-1.0-py3-none-any.whl","yanked":false,"requires-python":">=3","upload-time":"2020-01-01T00:00:00Z"}]}`,
	`{"files":[{"filename":"x-2.0.tar.gz","filename":"x-3.0.tar.gz","yanked":null,"Yanked":true,"core-metadata":{"sha256":"cd"},"size":3}],"versions":["3.0","2.0"]}`,
	`{"meta":{"api-version":"1.1"},"files":[{"filename":"x-1.0.zip","yanked":true,"provenance":null,"url":"https://e/x"}]}`,
	`{"files":[{"filename":1}]}`,
	`{"files":[{"yanked":tru}]}`,
	`{"files":[3]}`,
	`{"files":[{"filename":"x-1.0.tar.gz"`,
}

func checkSimpleMembersParity(t *testing.T, data []byte, name string) {
	t.Helper()
	wp, wy, werr := decodeSimpleStruct(bytes.NewReader(data), name)
	gp, gy, gerr := decodeSimple(bytes.NewReader(data), name)
	if (werr == nil) != (gerr == nil) {
		t.Fatalf("error: got %v, want %v", gerr, werr)
	}
	if werr != nil {
		return
	}
	if gy != wy || !reflect.DeepEqual(gp, wp) {
		t.Fatalf("got %+v %d, want %+v %d", gp, gy, wp, wy)
	}
}

func TestDecodeSimpleMembersParity(t *testing.T) {
	for _, tc := range []struct{ file, name string }{
		{"testdata/requests-simple.json", "requests"},
		{"testdata/zope.interface-simple.json", "zope-interface"},
	} {
		data, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(tc.file, func(t *testing.T) { checkSimpleMembersParity(t, data, tc.name) })
	}
	for i, s := range simpleMembersSeeds {
		t.Run(fmt.Sprint(i), func(t *testing.T) { checkSimpleMembersParity(t, []byte(s), "x") })
	}
}

func FuzzDecodeSimpleMembersParity(f *testing.F) {
	for _, s := range simpleMembersSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) { checkSimpleMembersParity(t, data, "x") })
}

// simpleMembersPage is a synthetic simple API page of 500 versions with 10 files each.
func simpleMembersPage() []byte {
	var b strings.Builder
	b.WriteString(`{"meta":{"api-version":"1.1"},"name":"pkg","files":[`)
	for v := range 500 {
		for i := range 10 {
			if v+i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"filename":"pkg-1.%d.0-cp3%d-cp3%d-manylinux_2_17_x86_64.whl","url":"https://files.example/pkg/pkg-1.%d.0-cp3%d.whl","hashes":{"sha256":"%064d"},"requires-python":">=3.8","size":123456,"upload-time":"2023-01-%02dT10:00:00.123456Z","yanked":%t,"core-metadata":{"sha256":"%064d"},"data-dist-info-metadata":{"sha256":"%064d"}}`,
				v, i, i, v, i, v, 1+v%28, v%50 == 0, i, i)
		}
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func BenchmarkDecodeSimpleMembers(b *testing.B) {
	data := simpleMembersPage()
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := decodeSimple(bytes.NewReader(data), "pkg"); err != nil {
			b.Fatal(err)
		}
	}
}
