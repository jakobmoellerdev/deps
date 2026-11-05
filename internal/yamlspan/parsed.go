package yamlspan

import "go.yaml.in/yaml/v4"

// Parsed memoizes the single-document parse (yaml.Unmarshal into a Node) of one file's bytes
// for the managers extracting that file, so a workflow read by both the github-actions and
// the docker manager is parsed once. The zero value is ready; a nil *Parsed, or a call with
// other bytes than the previous call's, parses anew (reusing the node of the previous bytes).
// A Parsed is not safe for concurrent use, and callers must not modify the returned node.
type Parsed struct {
	src  []byte
	doc  yaml.Node
	err  error
	done bool
}

// Document returns the document node of src and the parse error, as yaml.Unmarshal would.
func (p *Parsed) Document(src []byte) (*yaml.Node, error) {
	if p == nil {
		var doc yaml.Node
		err := yaml.Unmarshal(src, &doc)
		return &doc, err
	}
	if !p.done || !sameBytes(p.src, src) {
		p.src, p.doc, p.done = src, yaml.Node{}, true
		p.err = yaml.Unmarshal(src, &p.doc)
	}
	return &p.doc, p.err
}

// sameBytes reports whether a and b are the same slice (start and length), not just equal.
func sameBytes(a, b []byte) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}
