package custom

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/recolabs/gnata"
	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jsonc"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// JSONata mode runs a query over the parsed file, so its results carry no byte positions.
// locate finds them: every occurrence of a result's version (or digest) in the file gets a
// distinct numeric marker appended, the marked file is parsed and queried again, and the
// result that comes back as value+marker came from that occurrence. Digit markers keep marked
// numbers, plain YAML scalars and TOML bare keys what they were, and a value the query cuts
// out of a longer string ("foo@1.2.3" split at "@") keeps the marker that follows it.

const (
	// jsonataTimeout bounds one evaluation: a query is user input and may not terminate.
	jsonataTimeout = 10 * time.Second
	// markerBase is the first marker; every marker has markerWidth digits.
	markerBase  = 1_000_000_000
	markerWidth = 10
	// maxJSONataDoc caps the JSON a YAML file expands to through aliases.
	maxJSONataDoc = 64 << 20
)

// Result fields of a JSONata dep object; the first present key of each list wins, as for the
// regex groups (packageName over depName, both over name).
var (
	versionKeys = []string{"version", "currentValue"}
	digestKeys  = []string{"digest", "currentDigest"}
	nameKeys    = []string{"packageName", "depName", "name"}
)

// withJSONata compiles the entry's query and returns m in JSONata mode.
func (m *customManager) withJSONata(c config.Custom) (*customManager, error) {
	switch {
	case c.Match != "":
		return nil, errors.New("match: exclusive with jsonata")
	case c.Checksum != nil:
		return nil, errors.New("checksum: requires match (regex mode)")
	case !slices.Contains(config.CustomFileFormats, c.FileFormat):
		return nil, fmt.Errorf("fileFormat: must be one of %s, got %q", strings.Join(config.CustomFileFormats, ", "), c.FileFormat)
	}
	q, err := gnata.Compile(c.JSONata, gnata.WithTimeout(jsonataTimeout))
	if err != nil {
		return nil, fmt.Errorf("jsonata: %w", err)
	}
	m.query, m.format = q, c.FileFormat
	return m, nil
}

// jsonata returns the deps of the query's results over src, in result order.
func (m *customManager) jsonata(file string, src []byte) ([]dep.Dep, error) {
	objs, err := m.queryFile(src)
	if err != nil || len(objs) == 0 {
		return nil, err
	}
	deps := make([]dep.Dep, len(objs))
	for i, o := range objs {
		deps[i] = m.jsonataDep(file, o)
	}
	m.locate(src, objs, deps)
	for i := range deps {
		m.finishJSONataDep(&deps[i])
	}
	return deps, nil
}

// queryFile parses src as the entry's fileFormat and returns the query's dep objects.
func (m *customManager) queryFile(src []byte) ([]map[string]any, error) {
	doc, err := jsonataDocument(m.format, src)
	if err != nil {
		return nil, fmt.Errorf("parse as %s: %w", m.format, err)
	}
	r, err := m.query.EvalBytes(context.Background(), doc)
	if err != nil {
		return nil, fmt.Errorf("jsonata: %w", err)
	}
	switch r := gnata.NormalizeValue(r).(type) {
	case nil:
		return nil, nil
	case map[string]any:
		return []map[string]any{r}, nil
	case []any:
		out := make([]map[string]any, len(r))
		for i, e := range r {
			o, ok := e.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("jsonata: result [%d] is %s, not an object", i, jsonType(e))
			}
			out[i] = o
		}
		return out, nil
	default:
		return nil, fmt.Errorf("jsonata: result is %s, not an object or a list of objects", jsonType(r))
	}
}

// jsonType names the JSON type of a normalized gnata value.
func jsonType(v any) string {
	switch v.(type) {
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case map[string]any:
		return "an object"
	case []any:
		return "a list"
	case nil:
		return "null"
	}
	return "a number"
}

// jsonataDep builds the dep of one result object: the entry's datasource, name, versioning
// and registryUrl unless the object sets them (versioning defaults per datasource, see
// depVersioning), and the version and digest that locate looks for. Non-string fields and
// missing values skip the dep.
func (m *customManager) jsonataDep(file string, o map[string]any) dep.Dep {
	d := dep.Dep{Manager: Name, DepType: DepTypeJSONata, File: file, Line: 1, Datasource: m.datasource, Name: m.name}
	var bad string
	get := func(keys ...string) string {
		for _, k := range keys {
			switch v := o[k].(type) {
			case nil:
				continue
			case string:
				return v
			}
			bad = cmp.Or(bad, k)
			return ""
		}
		return ""
	}
	version, digest := get(versionKeys...), get(digestKeys...)
	d.Name = cmp.Or(get(nameKeys...), d.Name)
	d.Datasource = cmp.Or(get("datasource"), d.Datasource)
	d.Versioning = cmp.Or(get("versioning"), m.depVersioning(d.Datasource))
	d.ExtractVersion = get("extractVersion")
	registry := cmp.Or(get("registryUrl"), m.registry)
	switch {
	case bad != "":
		d.Skip = "not a string: " + bad
	case version == "":
		d.Skip = "empty version"
	case d.Name == "":
		d.Skip = "empty name"
	case d.Datasource == "":
		d.Skip = "empty datasource"
	default:
		d.Version, d.Digest = version, digest
		setRegistry(&d, registry)
	}
	return d
}

// finishJSONataDep checks a dep that jsonataDep did not skip once locate has run.
func (m *customManager) finishJSONataDep(d *dep.Dep) {
	if d.Skip != "" {
		return
	}
	switch {
	case d.RefSpan.End <= d.RefSpan.Start:
		d.Skip = "version not found in file"
		return
	case d.Digest != "" && d.DigestSpan.End <= d.DigestSpan.Start:
		d.Skip = "digest not found in file"
		return
	}
	m.classify(d)
	if d.Skip == "" && !m.knownVersioning(d.Versioning) {
		d.Skip = fmt.Sprintf("unknown versioning %q", d.Versioning)
	}
	if _, err := versioning.CompileExtract(d.ExtractVersion); d.Skip == "" && d.ExtractVersion != "" && err != nil {
		d.Skip = "invalid extractVersion: " + err.Error()
	}
}

// occurrence is one place a wanted value is spelled in the file.
type occurrence struct {
	start, end int
	value      string
}

// locate sets the spans and lines of the versions and digests of deps, the deps of objs. The
// first pass marks only occurrences not glued to letters or digits on either side, so that
// unrelated keys and words stay intact; a second pass marks every occurrence of the values
// still unlocated. A dep is located only when its marked result also has its name, so a query
// that reorders its results by value cannot swap two deps' spans.
func (m *customManager) locate(src []byte, objs []map[string]any, deps []dep.Dep) {
	for _, glued := range []bool{false, true} {
		want := map[string]bool{}
		for _, d := range deps {
			if d.Version != "" && d.RefSpan.End <= d.RefSpan.Start {
				want[d.Version] = true
			}
			if d.Digest != "" && d.DigestSpan.End <= d.DigestSpan.Start {
				want[d.Digest] = true
			}
		}
		if len(want) == 0 {
			break
		}
		occ := occurrences(src, want, glued)
		if len(occ) == 0 {
			continue
		}
		marked, err := m.queryFile(mark(src, occ))
		if err != nil || len(marked) != len(objs) {
			continue
		}
		for i := range deps {
			d := &deps[i]
			if firstString(marked[i], nameKeys) != firstString(objs[i], nameKeys) {
				continue
			}
			if s, ok := markedSpan(d.Version, firstString(marked[i], versionKeys), occ); ok && d.RefSpan.End <= d.RefSpan.Start {
				d.RefSpan = s
			}
			if s, ok := markedSpan(d.Digest, firstString(marked[i], digestKeys), occ); ok && d.DigestSpan.End <= d.DigestSpan.Start {
				d.DigestSpan = s
			}
		}
	}
	var lines []int
	for i := range deps {
		if d := &deps[i]; d.RefSpan.End > d.RefSpan.Start {
			if lines == nil {
				lines = yamlspan.LineStarts(src)
			}
			l, found := slices.BinarySearch(lines, d.RefSpan.Start)
			if !found {
				l-- // lines[l-1] < Start: the line before
			}
			d.Line = l + 1
		}
	}
}

// firstString returns the first present string field of keys in o, or "".
func firstString(o map[string]any, keys []string) string {
	for _, k := range keys {
		if s, ok := o[k].(string); ok {
			return s
		}
	}
	return ""
}

// occurrences returns the non-overlapping places src spells a wanted value, in file order;
// at one offset the longest value wins. Unless glued, the bytes before and after an
// occurrence must not be ASCII letters or digits.
func occurrences(src []byte, want map[string]bool, glued bool) []occurrence {
	var occ []occurrence
	for v := range want {
		b := []byte(v)
		for off := 0; ; {
			i := bytes.Index(src[off:], b)
			if i < 0 {
				break
			}
			s, e := off+i, off+i+len(b)
			if glued || (s == 0 || !isAlnum(src[s-1])) && (e == len(src) || !isAlnum(src[e])) {
				occ = append(occ, occurrence{s, e, v})
			}
			off = s + 1
		}
	}
	slices.SortFunc(occ, func(a, b occurrence) int { return cmp.Or(a.start-b.start, b.end-a.end) })
	out := occ[:0]
	end := 0
	for _, o := range occ {
		if o.start >= end {
			out = append(out, o)
			end = o.end
		}
	}
	return out
}

func isAlnum(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// mark returns src with marker k appended to occurrence k.
func mark(src []byte, occ []occurrence) []byte {
	out := make([]byte, 0, len(src)+len(occ)*markerWidth)
	at := 0
	for k, o := range occ {
		out = append(out, src[at:o.end]...)
		out = strconv.AppendInt(out, int64(markerBase+k), 10)
		at = o.end
	}
	return append(out, src[at:]...)
}

// markedSpan returns the span of the occurrence of v whose marker follows v in marked.
func markedSpan(v, marked string, occ []occurrence) (dep.Span, bool) {
	suffix, ok := strings.CutPrefix(marked, v)
	if v == "" || !ok || len(suffix) != markerWidth {
		return dep.Span{}, false
	}
	k, err := strconv.Atoi(suffix)
	k -= markerBase
	if err != nil || k < 0 || k >= len(occ) || occ[k].value != v {
		return dep.Span{}, false
	}
	return dep.Span{Start: occ[k].start, End: occ[k].end}, true
}

// jsonataDocument returns src parsed as format, as JSON whose keys keep the file's order (gnata
// evaluates raw JSON in key order, so $each, $keys and * list deps as the file does): JSON
// with comments and trailing commas blanked, a YAML stream as the list of its documents (as
// Renovate parses YAML for JSONata managers) and TOML as its root table.
func jsonataDocument(format string, src []byte) ([]byte, error) {
	switch format {
	case "json":
		doc := jsonc.Strip(src)
		var raw json.RawMessage
		if err := json.Unmarshal(doc, &raw); err != nil {
			return nil, err
		}
		return doc, nil
	case "yaml":
		return yamlDocument(src)
	case "toml":
		root, err := tomlspan.Parse(src)
		if err != nil {
			return nil, err
		}
		var b bytes.Buffer
		tomlJSON(&b, root)
		return b.Bytes(), nil
	}
	return nil, fmt.Errorf("unknown file format %q", format)
}

// yamlDocument converts a YAML stream to the JSON list of its documents.
func yamlDocument(src []byte) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('[')
	dec := yaml.NewDecoder(bytes.NewReader(src))
	for n := 0; ; n++ {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if n > 0 {
			b.WriteByte(',')
		}
		if err := yamlJSON(&b, &doc); err != nil {
			return nil, err
		}
	}
	b.WriteByte(']')
	return b.Bytes(), nil
}

// yamlJSON writes n as JSON. Aliases are expanded (bounded by maxJSONataDoc); `<<` is a plain
// key, as in YAML 1.2. Mapping keys are their scalar text. Strings, timestamps and other
// tagged scalars are strings; null, booleans and finite numbers keep their type.
func yamlJSON(b *bytes.Buffer, n *yaml.Node) error {
	if b.Len() > maxJSONataDoc {
		return fmt.Errorf("document exceeds %d MiB as JSON", maxJSONataDoc>>20)
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			b.WriteString("null")
			return nil
		}
		return yamlJSON(b, n.Content[0])
	case yaml.AliasNode:
		return yamlJSON(b, n.Alias)
	case yaml.MappingNode:
		b.WriteByte('{')
		for i := 0; i+1 < len(n.Content); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			k := n.Content[i]
			if k.Kind == yaml.AliasNode {
				k = k.Alias
			}
			writeJSONString(b, k.Value)
			b.WriteByte(':')
			if err := yamlJSON(b, n.Content[i+1]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case yaml.SequenceNode:
		b.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := yamlJSON(b, c); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case yaml.ScalarNode:
		switch n.ShortTag() {
		case "!!null":
			b.WriteString("null")
			return nil
		case "!!bool", "!!int", "!!float":
			var v any
			if n.Decode(&v) == nil {
				if j, err := json.Marshal(v); err == nil { // NaN and ±Inf fail: strings
					b.Write(j)
					return nil
				}
			}
		}
		writeJSONString(b, n.Value)
	default:
		b.WriteString("null")
	}
	return nil
}

// tomlJSON writes v as JSON. Booleans and finite integers and floats keep their type;
// datetimes, inf and nan are strings.
func tomlJSON(b *bytes.Buffer, v *tomlspan.Value) {
	switch v.Kind {
	case tomlspan.Table:
		b.WriteByte('{')
		for i, k := range v.Keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONString(b, k)
			b.WriteByte(':')
			tomlJSON(b, v.Fields[k])
		}
		b.WriteByte('}')
	case tomlspan.Array:
		b.WriteByte('[')
		for i, it := range v.Items {
			if i > 0 {
				b.WriteByte(',')
			}
			tomlJSON(b, it)
		}
		b.WriteByte(']')
	case tomlspan.Scalar:
		b.Write(tomlScalar(v.Str))
	default:
		writeJSONString(b, v.Str)
	}
}

// tomlScalar returns the JSON of a raw TOML scalar.
func tomlScalar(s string) []byte {
	if s == "true" || s == "false" {
		return []byte(s)
	}
	n := strings.ReplaceAll(s, "_", "")
	if i, err := strconv.ParseInt(n, 0, 64); err == nil {
		return strconv.AppendInt(nil, i, 10)
	}
	if f, err := strconv.ParseFloat(n, 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
		return strconv.AppendFloat(nil, f, 'g', -1, 64)
	}
	j, _ := json.Marshal(s)
	return j
}

func writeJSONString(b *bytes.Buffer, s string) {
	j, _ := json.Marshal(s) // a string always marshals
	b.Write(j)
}
