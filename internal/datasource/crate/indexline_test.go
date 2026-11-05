package crate

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// decodeIndexLinesOracle is the struct-unmarshal decode decodeIndexLine replaced; it is the
// reference for the entries and for whether, and after how many entries, decoding fails.
func decodeIndexLinesOracle(body string) ([]indexLine, error) {
	dec := jsontext.NewDecoder(strings.NewReader(body), jsontext.AllowDuplicateNames(true))
	var out []indexLine
	for {
		var l indexLine
		if err := json.UnmarshalDecode(dec, &l); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
		out = append(out, l)
	}
}

func decodeIndexLinesMembers(body string) ([]indexLine, error) {
	dec := jsontext.NewDecoder(strings.NewReader(body), jsontext.AllowDuplicateNames(true))
	var out []indexLine
	var l indexLine
	for {
		if err := decodeIndexLine(dec, &l); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
		out = append(out, l)
	}
}

func checkIndexLineParity(t *testing.T, body string) {
	t.Helper()
	want, werr := decodeIndexLinesOracle(body)
	got, gerr := decodeIndexLinesMembers(body)
	if (werr == nil) != (gerr == nil) {
		t.Fatalf("error mismatch for %q: oracle %v, members %v", body, werr, gerr)
	}
	if len(got) != len(want) {
		t.Fatalf("entries for %q: got %d, want %d", body, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d for %q: got %+v, want %+v", i, body, got[i], want[i])
		}
	}
}

var indexLineCases = []string{
	"",
	"\n",
	`{"vers":"1.0.0","yanked":true,"pubtime":"2020-01-01T00:00:00Z"}`,
	`{"name":"a","vers":"1.0.0","deps":[{"name":"b","req":"^1","features":[],"target":null}],"features":{"std":[]},"yanked":false}` + "\n" + `{"vers":"1.0.1"}`,
	`{"vers":"1.0.0","vers":"2.0.0"}`,
	`{"vers":null,"yanked":null,"pubtime":null}`,
	`{"Vers":"1.0.0","YANKED":true}`,
	`{}`,
	`null`,
	`{"vers":"1.0.0"} null {"vers":"1.0.1"}`,
	`{"vers":1}`,
	`{"yanked":"no"}`,
	`{"pubtime":false}`,
	`[1,2]`,
	`"x"`,
	`{"vers":"1.0.0"`,
	`{"vers":"1.0.0",`,
	`{"vers":"1.0.0"} {"deps":[}`,
	`{"vers":"1.0.0"}}`,
	`{"v":2,"features2":{"x":["dep:y"]},"rust_version":"1.60","links":null,"vers":"0.1.0"}`,
}

func TestDecodeIndexLineMatchesUnmarshal(t *testing.T) {
	fixture, err := os.ReadFile("testdata/serde")
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := decodeIndexLinesOracle(string(fixture)); len(want) == 0 {
		t.Fatal("fixture decoded to no entries")
	}
	checkIndexLineParity(t, string(fixture))
	for _, c := range indexLineCases {
		checkIndexLineParity(t, c)
	}
}

func FuzzDecodeIndexLine(f *testing.F) {
	fixture, err := os.ReadFile("testdata/serde")
	if err != nil {
		f.Fatal(err)
	}
	first, _, _ := strings.Cut(string(fixture), "\n")
	f.Add(first)
	for _, c := range indexLineCases {
		f.Add(c)
	}
	f.Fuzz(checkIndexLineParity)
}
