package yamlspan

import (
	"bytes"
	"errors"
	"io"
	"slices"

	"go.yaml.in/yaml/v4"
)

// Documents calls fn with every document of src that keep accepts, and returns the error of
// the first document that does not parse, after fn ran for the documents before it (as a
// decoder of the whole file does). keep sees a document's bytes including its marker line,
// like KeepDocuments'. Empty documents are none: no manager reads one.
//
// The documents are split once per file and parsed on demand: a document two managers keep
// (a Flux bundle with image keys, which both the docker and the helm manager read) is parsed
// once, its parse error included, and one no manager keeps (a large CustomResourceDefinition)
// is never parsed. A nil *Parsed, or a call with other bytes than the previous call's, splits
// and parses anew.
//
// The nodes are the file's: their lines are the lines of src, so Locate works against it.
// Callers must not modify them.
func (p *Parsed) Documents(src []byte, keep func(doc []byte) bool, fn func(doc *yaml.Node)) error {
	docs := &documents{}
	if p != nil {
		docs = &p.docs
	}
	docs.split(src)
	docs.kept = slices.Grow(docs.kept[:0], len(docs.chunks))
	for i := range docs.chunks {
		docs.kept = append(docs.kept, keep(docs.bytes(i)))
	}
	for i := range docs.chunks {
		if !docs.kept[i] {
			continue
		}
		if !docs.chunks[i].done {
			docs.decodeRun(i)
		}
		c := &docs.chunks[i]
		for _, n := range c.docs {
			fn(n)
		}
		if c.err != nil {
			return c.err
		}
		if c.transient {
			// Beyond the bound the documents are dropped once the caller has seen them, so
			// the next manager parses them again, as every manager did before.
			docs.cached -= c.end - c.off
			c.docs, c.done, c.transient = nil, false, false
		}
	}
	return nil
}

// documents holds the document chunks of one file's bytes.
type documents struct {
	src    []byte
	chunks []chunk
	kept   []bool // of the running call, as long as its chunks (scratch)
	cached int    // document bytes whose nodes are kept, against maxCached
	done   bool
}

// maxCached bounds the document bytes of a file whose nodes are kept for the next manager.
// A document's nodes are a multiple of its bytes, and a manifest of several megabytes every
// manager reads would hold all of them at once.
const maxCached = 1 << 20

// chunk is the byte range of one document of a file, and of the documents following it when a
// directive or a document-level block scalar makes them one unit (see split). Its documents
// and parse error are kept from the first parse on, unless it is transient.
type chunk struct {
	off, end  int
	line      int  // lines before off, added to the lines of the nodes parsed from the chunk
	directive bool // has a line starting with "%", a directive of the next document
	transient bool // parsed beyond maxCached: its documents are dropped after the handout
	docs      []*yaml.Node
	err       error
	done      bool
}

func (d *documents) bytes(i int) []byte { return d.src[d.chunks[i].off:d.chunks[i].end] }

// split fills d with the document ranges of src, unless it holds them already. A chunk that
// ends in a directive takes the next document in: the directive applies to it, and a document
// parsed without the directive it needs has other tags, or does not parse. Bytes a parser may
// split into documents differently (unsplittable) are one chunk.
func (d *documents) split(src []byte) {
	if d.done && sameBytes(d.src, src) {
		return
	}
	d.src, d.chunks, d.done = src, nil, true
	if unsplittable(src) {
		d.chunks = []chunk{{end: len(src)}}
		return
	}
	line, prev := 0, 0
	eachDocument(src, func(start, end int) {
		line += bytes.Count(src[prev:start], []byte{'\n'})
		prev = start
		directive := hasDirective(src[start:end])
		if n := len(d.chunks) - 1; n >= 0 && d.chunks[n].directive {
			d.chunks[n].end, d.chunks[n].directive = end, directive
			return
		}
		d.chunks = append(d.chunks, chunk{off: start, end: end, line: line, directive: directive})
	})
}

// hasDirective reports whether doc has a line starting with "%".
func hasDirective(doc []byte) bool {
	return bytes.HasPrefix(doc, []byte("%")) || bytes.Contains(doc, []byte("\n%"))
}

// decodeRun parses chunk i and the chunks after it the running call keeps and has not parsed
// yet, in one pass: a file of many small documents costs one parser, not one per document.
// Each document goes to the chunk its first node is in.
func (d *documents) decodeRun(i int) {
	to := i + 1
	for to < len(d.chunks) && d.kept[to] && !d.chunks[to].done {
		to++
	}
	at, line := i, d.chunks[i].line
	dec := yaml.NewDecoder(bytes.NewReader(d.src[d.chunks[i].off:d.chunks[to-1].end]))
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				for ; at < to; at++ {
					d.parsed(at)
				}
				return
			}
			if at == to-1 { // the error is in the last chunk of the run, the one left
				d.parsed(at)
				d.chunks[at].err = err
				return
			}
			// The run stopped at the error, which is in the chunk it reached or a later one.
			// Those are parsed one by one, up to the one the error is in, so that it reaches
			// that chunk only: a manager skipping that document still extracts from the
			// others, and one stopping at it leaves the rest unparsed.
			for ; at < to; at++ {
				d.decodeChunk(at)
				if d.chunks[at].err != nil {
					return
				}
			}
			return
		}
		if len(doc.Content) == 0 {
			continue
		}
		shiftLines(&doc, line)
		for at+1 < to && doc.Content[0].Line > d.chunks[at+1].line {
			d.parsed(at)
			at++
		}
		d.chunks[at].docs = append(d.chunks[at].docs, &doc)
	}
}

// parsed marks chunk i parsed and accounts its bytes against maxCached: beyond the bound its
// documents are transient, kept only until the caller has seen them.
func (d *documents) parsed(i int) {
	c := &d.chunks[i]
	c.done = true
	d.cached += c.end - c.off
	c.transient = d.cached > maxCached
}

// decodeChunk parses chunk i on its own, dropping what a run parsed of it.
func (d *documents) decodeChunk(i int) {
	c := &d.chunks[i]
	c.docs, c.err = nil, nil
	d.parsed(i)
	dec := yaml.NewDecoder(bytes.NewReader(d.src[c.off:c.end]))
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			if !errors.Is(err, io.EOF) {
				c.err = err
			}
			return
		}
		if len(doc.Content) == 0 {
			continue
		}
		shiftLines(&doc, c.line)
		c.docs = append(c.docs, &doc)
	}
}

// shiftLines adds by to the line of n and of every node below it, so that a document parsed
// in a part of a file has the lines of the whole file. An alias is not followed: the node it
// points at is a node of the document already.
func shiftLines(n *yaml.Node, by int) {
	if by == 0 {
		return
	}
	n.Line += by
	for _, c := range n.Content {
		shiftLines(c, by)
	}
}
