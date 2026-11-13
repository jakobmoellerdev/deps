package customds

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Selector is a compiled `versions` expression: a tiny jq subset of pipeline stages separated
// by `|`. A stage is `keys` (the sorted keys of an object) or a path of steps: `.` (identity),
// `.name` (object field) and `[]` (array elements, or the values of an object by sorted key);
// a path may start with `[]` (`[].tag_name`). As in jq, a missing field is null, a field of
// null is null, and iterating null or indexing or iterating a value of the wrong type is an
// error; nulls in the result are dropped.
type Selector struct {
	src    string
	stages []stage
}

type stage struct {
	keys  bool   // `keys`; else steps
	steps []step // empty = identity
}

type step struct {
	field string // "" = iterate
}

// ParseSelector compiles a versions expression, e.g. `.releases | keys`, `.releases[].version`
// or `[].tag_name`.
func ParseSelector(s string) (Selector, error) {
	sel := Selector{src: s}
	for part := range strings.SplitSeq(s, "|") {
		st, err := parseStage(strings.TrimSpace(part))
		if err != nil {
			return Selector{}, fmt.Errorf("selector %q: %w", s, err)
		}
		sel.stages = append(sel.stages, st)
	}
	return sel, nil
}

func parseStage(s string) (stage, error) {
	switch {
	case s == "":
		return stage{}, errors.New("empty stage")
	case s == "keys":
		return stage{keys: true}, nil
	case s == ".":
		return stage{}, nil
	case s[0] != '.' && !strings.HasPrefix(s, "[]"):
		return stage{}, fmt.Errorf("stage %q must be keys or start with . or []", s)
	}
	var st stage
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "[]"):
			st.steps = append(st.steps, step{})
			i += 2
		case strings.HasPrefix(s[i:], ".[]"):
			st.steps = append(st.steps, step{})
			i += 3
		case s[i] == '.':
			j := i + 1
			for j < len(s) && isIdent(s[j], j == i+1) {
				j++
			}
			if j == i+1 {
				return stage{}, fmt.Errorf("expected a field name at offset %d of %q", i+1, s)
			}
			st.steps = append(st.steps, step{field: s[i+1 : j]})
			i = j
		default:
			return stage{}, fmt.Errorf("unexpected %q at offset %d of %q", s[i], i, s)
		}
	}
	return st, nil
}

func isIdent(c byte, first bool) bool {
	return c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || !first && ('0' <= c && c <= '9' || c == '-')
}

// String returns the source expression.
func (s Selector) String() string { return s.src }

// Select evaluates the selector on a decoded document (map[string]any, []any, string,
// jsontext.Value holding a number's source text, bool or nil) and returns the selected scalars
// as text; nulls are dropped.
func (s Selector) Select(doc any) ([]string, error) {
	vals := []any{doc}
	for _, st := range s.stages {
		var next []any
		for _, v := range vals {
			out, err := st.eval(v)
			if err != nil {
				return nil, fmt.Errorf("selector %q: %w", s.src, err)
			}
			next = append(next, out...)
		}
		vals = next
	}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		switch v := v.(type) {
		case nil:
		case string:
			out = append(out, v)
		case jsontext.Value:
			out = append(out, string(v))
		case bool:
			out = append(out, strconv.FormatBool(v))
		default:
			return nil, fmt.Errorf("selector %q: yields %s, want strings", s.src, typeName(v))
		}
	}
	return out, nil
}

func (st stage) eval(v any) ([]any, error) {
	if st.keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("keys of %s", typeName(v))
		}
		ks := make([]string, 0, len(m))
		for k := range m {
			ks = append(ks, k)
		}
		slices.Sort(ks)
		out := make([]any, len(ks))
		for i, k := range ks {
			out[i] = k
		}
		return out, nil
	}
	vals := []any{v}
	for _, sp := range st.steps {
		var next []any
		for _, v := range vals {
			switch c := v.(type) {
			case map[string]any:
				if sp.field == "" {
					// Values by sorted key, as `keys` sorts them: Go maps (and the YAML
					// decoder) do not keep the order of the document, and iterating one
					// would make the result depend on the run.
					for _, k := range slices.Sorted(maps.Keys(c)) {
						next = append(next, c[k])
					}
				} else {
					next = append(next, c[sp.field]) // a missing field is null
				}
			case []any:
				if sp.field != "" {
					return nil, fmt.Errorf("cannot index an array with %q", sp.field)
				}
				next = append(next, c...)
			case nil:
				if sp.field == "" {
					return nil, errors.New("cannot iterate over null")
				}
			default:
				if sp.field == "" {
					return nil, fmt.Errorf("cannot iterate over %s", typeName(v))
				}
				return nil, fmt.Errorf("cannot index %s with %q", typeName(v), sp.field)
			}
		}
		vals = next
	}
	return vals, nil
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	}
	return "a number"
}
