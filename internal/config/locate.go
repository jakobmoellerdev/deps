package config

import (
	"errors"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"
)

// decodeProblems locates the problems of a yaml decode error: each construct error (unknown
// field, wrong type) or the syntax error at its line.
func decodeProblems(err error) []Problem {
	if les, ok := errors.AsType[*yaml.LoadErrors](err); ok && len(les.Errors) > 0 {
		out := make([]Problem, len(les.Errors))
		for i, e := range les.Errors {
			out[i] = Problem{Line: e.Mark.Line, Msg: e.Message}
		}
		return out
	}
	if le, ok := errors.AsType[*yaml.LoadError](err); ok {
		return []Problem{{Line: le.Mark.Line, Msg: le.Message}}
	}
	return []Problem{{Msg: err.Error()}}
}

// fieldProblems locates each *FieldError of a Validate error at the line of its field in
// the document whose top-level node is root (nil = none). A field that is not in the
// document (a default, or a key the path names only in part) takes the line of its nearest
// written ancestor. extended returns the line of an error of another file (FieldError.Path,
// an extended config) in the document, ok=false for the document's own.
func fieldProblems(root *yaml.Node, err error, extended func(path string) (line int, ok bool)) []Problem {
	var errs []error
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		errs = j.Unwrap()
	} else {
		errs = []error{err}
	}
	out := make([]Problem, 0, len(errs))
	for _, e := range errs {
		var fe *FieldError
		if !errors.As(e, &fe) {
			out = append(out, Problem{Msg: e.Error()})
			continue
		}
		if line, ok := extended(fe.Path); ok {
			out = append(out, Problem{Line: line, Msg: fe.Error()})
			continue
		}
		out = append(out, Problem{Line: fieldLine(root, fe.Field), Msg: fe.Field + ": " + fe.Msg})
	}
	return out
}

// fieldLine walks a field path ("rules[0].manager", "customDatasources.x.url") from the
// document's root mapping and returns the line of the deepest node found (0 = none).
func fieldLine(n *yaml.Node, field string) int {
	line := 0
	for step := range strings.SplitSeq(field, ".") {
		key, idx := step, -1
		if i := strings.IndexByte(step, '['); i >= 0 && strings.HasSuffix(step, "]") {
			if v, err := strconv.Atoi(step[i+1 : len(step)-1]); err == nil {
				key, idx = step[:i], v
			}
		}
		if key != "" {
			next := mapValue(n, key)
			if next == nil {
				return line
			}
			n, line = next.val, next.key.Line
		}
		if idx >= 0 {
			if n.Kind != yaml.SequenceNode || idx >= len(n.Content) {
				return line
			}
			n, line = n.Content[idx], n.Content[idx].Line
		}
	}
	return line
}

type mapEntry struct{ key, val *yaml.Node }

func mapValue(n *yaml.Node, key string) *mapEntry {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return &mapEntry{n.Content[i], n.Content[i+1]}
		}
	}
	return nil
}
