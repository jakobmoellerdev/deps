package npmds

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jsonscan"
)

// decodeMetadata streams an abbreviated metadata document and keeps the versions that are not
// deprecated, `dist-tags.latest` and the top-level `modified`; every other member is skipped.
//
// It scans the document with jsonscan, which validates like jsontext but skips the values deps
// does not keep faster, holding no more than one version object at a time. Errors, and the
// rare shapes decodeMetadata does not decode itself (a version that is not an object, a
// `deprecated` that is not a string, true, false or null, several `deprecated` in one version),
// are left to the jsontext decoders: decodeMetadataTokens replayed from the last checkpoint, or
// versionDeprecated over the one version. Results and errors are theirs.
func decodeMetadata(r io.Reader) (metadata, int, error) {
	s := metadataScanner{r: stickyReader{r: r}}
	s.sc = jsonscan.New(&s.r)
	defer s.sc.Release()
	m, err := s.metadata()
	if err != nil {
		if exact, ok := errors.AsType[*exactError](err); ok {
			err = exact.err
		} else {
			err = s.replay()
		}
		return metadata{}, 0, err
	}
	return m, len(m.deprecated), nil
}

// exactError is an error of the jsontext decoders, which decodeMetadata returns as it is.
type exactError struct{ err error }

func (e *exactError) Error() string { return e.err.Error() }

// errReplay makes decodeMetadata replay decodeMetadataTokens: the input is not one
// decodeMetadata decodes itself.
var errReplay = errors.New("npm: replay metadata from checkpoint")

// errReplayDiverged is returned should decodeMetadataTokens accept input jsonscan rejected;
// jsonscan accepts exactly what jsontext accepts, so this does not happen.
var errReplayDiverged = errors.New("npm: metadata scanner and decoder disagree")

// scanLevel is where a checkpoint sits: before the document, between top-level members or
// between versions.
type scanLevel uint8

const (
	levelDocument scanLevel = iota
	levelTop
	levelVersions
)

// metadataScanner decodes the abbreviated metadata document with a jsonscan.Scanner, marked
// at the last checkpoint so decodeMetadataTokens can be replayed from there.
type metadataScanner struct {
	r   stickyReader
	sc  *jsonscan.Scanner
	dep deprecation // of the version object scanned last

	// The checkpoint: its stream offset, and the context from which replay rebuilds a prefix
	// that puts the reference decoder in the same state (level, whether a member precedes it,
	// and that member's name).
	at      int64
	level   scanLevel
	first   bool
	prevKey string
}

// stickyReader returns, once a Read returned no data and an error, that error on every later
// Read: a replay then fails on the same read error the scanner did.
type stickyReader struct {
	r   io.Reader
	err error
}

func (s *stickyReader) Read(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	n, err := s.r.Read(p)
	if n == 0 && err != nil {
		s.err = err
	}
	return n, err
}

// checkpoint marks the next unread byte as the point decodeMetadataTokens is replayed from.
func (s *metadataScanner) checkpoint(level scanLevel, first bool, prevKey string) {
	s.sc.Mark()
	s.at, s.level, s.first, s.prevKey = s.sc.Offset(), level, first, prevKey
}

// metadata scans the document; see decodeMetadata.
func (s *metadataScanner) metadata() (m metadata, err error) {
	s.checkpoint(levelDocument, true, "")
	if err := s.sc.Open('{'); err != nil {
		return metadata{}, err
	}
	first := true
	for {
		s.checkpoint(levelTop, first, s.prevKey)
		more, err := s.sc.Next()
		if err != nil || !more {
			return m, err
		}
		first = false
		name := s.sc.Name()
		s.prevKey = name // replayed from a later checkpoint
		c, err := s.sc.Peek()
		if err != nil {
			return metadata{}, err
		}
		switch {
		case name == "versions":
			if c != '{' {
				return metadata{}, errReplay // decodeMetadataTokens reports what it got
			}
			m.rels, m.deprecated, err = s.versions()
			s.prevKey = name
		case name == "dist-tags" && c == '{':
			m.latest, err = s.latest()
		case name == "modified" && c == '"':
			var mod []byte
			if mod, err = s.sc.StrBytes(); err == nil {
				m.modified, _ = time.Parse(time.RFC3339, string(mod)) // unparsable = unknown bound
			}
		default:
			err = s.sc.Skip()
		}
		if err != nil {
			return metadata{}, err
		}
	}
}

// versions scans the `versions` object; see decodeVersions.
func (s *metadataScanner) versions() (rels []datasource.Release, deprecated map[string]string, err error) {
	if err := s.sc.Open('{'); err != nil {
		return nil, nil, err
	}
	first, prev := true, ""
	for {
		s.checkpoint(levelVersions, first, prev)
		more, err := s.sc.Next()
		if err != nil || !more {
			return rels, deprecated, err
		}
		v := s.sc.Name()
		c, err := s.sc.Peek()
		if err != nil {
			return nil, nil, err
		}
		var msg string
		var gone bool
		switch c {
		case '{':
			msg, gone, err = s.version(first)
		case 'n':
			err = s.sc.Null()
		default:
			err = errReplay // not an object: decodeVersions fails
		}
		if err != nil {
			return nil, nil, err
		}
		first, prev = false, v
		if gone {
			if deprecated == nil {
				deprecated = make(map[string]string)
			}
			deprecated[v] = msg
			continue
		}
		rels = append(rels, datasource.Release{Version: v})
	}
}

// version scans one version object and reports whether its `deprecated` member marks it
// deprecated, with the message; see versionDeprecated. first tells whether it is the first
// version. A `deprecated` that is not a string, true, false or null, or a repeated one, is
// decided by versionDeprecated over the version (buffered from the checkpoint).
func (s *metadataScanner) version(first bool) (msg string, gone bool, err error) {
	s.dep = deprecation{}
	if err := s.sc.Open('{'); err != nil {
		return "", false, err
	}
	for {
		found, err := s.sc.Find("deprecated")
		if err != nil {
			return "", false, err
		}
		if !found {
			break
		}
		if err := s.deprecated(); err != nil {
			return "", false, err
		}
	}
	if s.dep.seen > 1 || s.dep.odd {
		return s.replayVersion(first)
	}
	return s.dep.msg, s.dep.gone, nil
}

// deprecation is what version learned of the `deprecated` members of one version object.
type deprecation struct {
	seen int    // number of members
	odd  bool   // a value other than a string, true, false or null
	msg  string // the message of the last
	gone bool   // whether the last marks the version deprecated
}

// deprecated scans the value of a version's `deprecated` member into s.dep.
func (s *metadataScanner) deprecated() (err error) {
	s.dep.seen++
	c, err := s.sc.Peek()
	if err != nil {
		return err
	}
	switch c {
	case '"':
		s.dep.msg, err = s.sc.Str()
		s.dep.gone = s.dep.msg != ""
		return err
	case 't', 'f':
		s.dep.msg = ""
		s.dep.gone, err = s.sc.Bool()
		return err
	case 'n':
		s.dep.msg, s.dep.gone = "", false
		return s.sc.Null()
	}
	s.dep.odd = true
	return s.sc.Skip()
}

// latest scans `dist-tags` and returns its `latest`; see decodeLatest.
func (s *metadataScanner) latest() (latest string, err error) {
	if err := s.sc.Open('{'); err != nil {
		return "", err
	}
	for {
		more, err := s.sc.Next()
		if err != nil || !more {
			return latest, err
		}
		c, err := s.sc.Peek()
		if err != nil {
			return "", err
		}
		if !s.sc.NameIs("latest") || c != '"' {
			if err := s.sc.Skip(); err != nil {
				return "", err
			}
			continue
		}
		if latest, err = s.sc.Str(); err != nil {
			return "", err
		}
	}
}

// replay runs decodeMetadataTokens over the input from the checkpoint on, behind a prefix
// that puts it in the checkpoint's state, and returns its error with offsets made absolute.
func (s *metadataScanner) replay() error {
	prefix := s.prefix()
	r := io.MultiReader(bytes.NewReader(prefix), bytes.NewReader(s.sc.Marked()), bytes.NewReader(s.sc.Buffered()), &s.r)
	_, _, err := decodeMetadataTokens(r)
	if err == nil {
		return errReplayDiverged
	}
	shiftOffsets(err, s.at-int64(len(prefix)))
	return err
}

// prefix returns the JSON that puts decodeMetadataTokens in the checkpoint's state.
func (s *metadataScanner) prefix() []byte {
	var prefix []byte
	switch s.level {
	case levelTop:
		prefix = append(prefix, '{')
		if !s.first {
			prefix, _ = jsontext.AppendQuote(prefix, s.prevKey)
			prefix = append(prefix, ":{}"...) // a value every member accepts
		}
	case levelVersions:
		prefix = append(prefix, `{"versions":{`...)
		if !s.first {
			prefix, _ = jsontext.AppendQuote(prefix, s.prevKey)
			prefix = append(prefix, ":null"...)
		}
	}
	return prefix
}

// replayVersion decides the version scanned last, from the checkpoint before it, with
// versionDeprecated. first tells whether it is the first version.
func (s *metadataScanner) replayVersion(first bool) (string, bool, error) {
	prefix := `{"versions":{`
	if !first {
		prefix += `"":null` // the checkpoint is before the ',' that follows the previous version
	}
	doc := make([]byte, 0, len(prefix)+len(s.sc.Marked())+2)
	doc = append(doc, prefix...)
	doc = append(doc, s.sc.Marked()...)
	doc = append(doc, "}}"...)
	dec := jsontext.NewDecoder(bytes.NewReader(doc), jsontext.AllowDuplicateNames(true))
	tokens := 4 // {, "versions", {, the version's name
	if !first {
		tokens += 2 // "", null
	}
	for range tokens {
		if _, err := dec.ReadToken(); err != nil {
			return "", false, &exactError{err}
		}
	}
	msg, gone, err := versionDeprecated(dec)
	if err != nil {
		shiftOffsets(err, s.at-int64(len(prefix)))
		return "", false, &exactError{err}
	}
	return msg, gone, nil
}

// shiftOffsets adds delta to the byte offsets of err's syntactic and semantic errors.
func shiftOffsets(err error, delta int64) {
	if serr, ok := errors.AsType[*jsontext.SyntacticError](err); ok {
		serr.ByteOffset += delta
	}
	if serr, ok := errors.AsType[*json.SemanticError](err); ok {
		serr.ByteOffset += delta
	}
}
