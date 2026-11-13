package nuget

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"slices"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// dotnetSDK is the dep name of the .NET SDK (datasource dotnet-version).
const dotnetSDK = "dotnet-sdk"

// globalJSON returns the deps of a global.json: `sdk.version` (the .NET SDK) and every
// `msbuild-sdks` entry (`"Microsoft.Build.Traversal": "4.1.0"`).
func globalJSON(src []byte) ([]item, error) {
	var out []item
	err := jsonStrings(src, func(path []string, v value) {
		switch {
		case slices.Equal(path, []string{"sdk", "version"}):
			out = append(out, item{depType: DepTypeDotnetSDK, name: value{text: dotnetSDK, line: v.line}, version: &v})
		case len(path) == 2 && path[0] == "msbuild-sdks":
			out = append(out, item{depType: DepTypeMSBuildSDK, name: value{text: path[1], line: v.line}, version: &v})
		}
	})
	return out, err
}

// dotnetTools returns the tools of a dotnet-tools.json (`tools.<id>.version`).
func dotnetTools(src []byte) ([]item, error) {
	var out []item
	err := jsonStrings(src, func(path []string, v value) {
		if len(path) == 3 && path[0] == "tools" && path[2] == "version" {
			out = append(out, item{depType: DepTypeNuGet, name: value{text: path[1], line: v.line}, version: &v})
		}
	})
	return out, err
}

// jsonStrings calls visit with the member path (object keys; "[]" for array elements) and
// value of every string in the JSON document src. `//` and `/* */` comments, which the .NET
// SDK accepts in global.json, are read as blanks, as is a leading byte order mark.
func jsonStrings(src []byte, visit func(path []string, v value)) error {
	buf := blankComments(blankBOM(src))
	dec := jsontext.NewDecoder(bytes.NewReader(buf), jsontext.AllowDuplicateNames(true))
	ln := newLines(src)
	var walk func(path []string) error
	walk = func(path []string) error {
		tok, err := dec.ReadToken()
		if err != nil {
			return err
		}
		switch tok.Kind() {
		case '{':
			for dec.PeekKind() != '}' {
				k, err := dec.ReadToken()
				if err != nil {
					return err
				}
				if err := walk(append(slices.Clip(path), k.String())); err != nil {
					return err
				}
			}
			_, err = dec.ReadToken()
		case '[':
			for dec.PeekKind() != ']' {
				if err := walk(append(slices.Clip(path), "[]")); err != nil {
					return err
				}
			}
			_, err = dec.ReadToken()
		case '"':
			s := tok.String()
			end := int(dec.InputOffset())
			start := end - len(s) - 2
			v := value{text: s, line: ln.at(max(0, start))}
			if start >= 0 && buf[start] == '"' && string(buf[start+1:end-1]) == s {
				v.span = dep.Span{Start: start + 1, End: end - 1}
			}
			visit(path, v)
		}
		return err
	}
	err := walk(nil)
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF // a truncated document, not a clean end
	}
	return err
}

// blankComments returns src with `//` line comments and `/* */` block comments outside
// strings replaced by spaces (newlines kept), so offsets stay those of src.
func blankComments(src []byte) []byte {
	if !bytes.Contains(src, []byte("/")) {
		return src
	}
	out := bytes.Clone(src)
	inStr := false
	for i := 0; i < len(out); i++ {
		c := out[i]
		switch {
		case inStr:
			switch c {
			case '\\':
				i++ // the escaped byte
			case '"':
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '/' && i+1 < len(out) && (out[i+1] == '/' || out[i+1] == '*'):
			stop := bytes.IndexByte(out[i:], '\n')
			if out[i+1] == '*' {
				stop = bytes.Index(out[i+2:], []byte("*/"))
				if stop >= 0 {
					stop += 4
				}
			}
			if stop < 0 {
				stop = len(out) - i
			}
			for j := i; j < i+stop; j++ {
				if out[j] != '\n' {
					out[j] = ' '
				}
			}
			i += stop - 1
		}
	}
	return out
}

// bom is the UTF-8 byte order mark, which Visual Studio writes before a global.json's first
// byte; the .NET SDK's JSON reader skips it.
var bom = []byte("\ufeff")

// blankBOM returns src with a leading byte order mark replaced by blanks, which the decoder
// reads as the whitespace before the document. Blanking rather than trimming keeps every byte
// offset, and so every span, that of src.
func blankBOM(src []byte) []byte {
	if !bytes.HasPrefix(src, bom) {
		return src
	}
	out := bytes.Clone(src)
	for i := range bom {
		out[i] = ' '
	}
	return out
}
