package datasource

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestDecodeArray(t *testing.T) {
	type elem struct {
		A string `json:"a"`
		B bool   `json:"b"`
	}
	for _, tc := range []struct {
		name, body string
		want       []elem
		err        string // wanted substring of the error, empty for success
		notArray   bool
	}{
		// Every element starts from a zero T: b of the first must not leak into the second.
		{name: "elements", body: `[{"a":"x","b":true,"files":[1,2]},{"a":"y"}] trailing`, want: []elem{{"x", true}, {"y", false}}},
		{name: "empty", body: `[]`},
		{name: "object", body: `{"a":"x"}`, err: "not an array", notArray: true},
		{name: "not json", body: `<html>`, err: "invalid character", notArray: true},
		{name: "empty body", body: ``, err: "EOF", notArray: true},
		{name: "bad element", body: `[{"a":1}]`, err: "JSON number"},
		{name: "truncated", body: `[{"a":"x"},`, err: "EOF", want: []elem{{"x", false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []elem
			err := DecodeArray(strings.NewReader(tc.body), func(e elem) { got = append(got, e) })
			if tc.err == "" && err != nil || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
				t.Fatalf("err = %v, want %q", err, tc.err)
			}
			if errors.Is(err, ErrNotArray) != tc.notArray {
				t.Errorf("errors.Is(%v, ErrNotArray) = %t", err, !tc.notArray)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("elements = %v, want %v", got, tc.want)
			}
		})
	}
}
