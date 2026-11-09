// Package customds implements the user-defined datasources of the `customDatasources` config
// section: a version list fetched from one URL as JSON, YAML or plain text, selected with a
// tiny jq subset (Selector) and optionally filtered and extracted with a regexp. Deps refer to
// them as datasource `custom.<name>`.
package customds

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Prefix starts the datasource name of a custom datasource: `custom.<name>`.
const Prefix = "custom."

// Formats of the fetched document.
const (
	FormatJSON  = "json"
	FormatYAML  = "yaml"
	FormatPlain = "plain" // one version per line; empty lines and `#` comments are skipped
)

// Formats are the accepted format values.
var Formats = []string{FormatJSON, FormatYAML, FormatPlain}

// DefaultVersions is the selector of json and yaml documents without `versions`: Renovate's
// custom datasource shape `{"releases": [{"version": "..."}]}`.
const DefaultVersions = ".releases[].version"

// MaxBody caps the fetched document.
const MaxBody = 10 << 20

// Spec configures one custom datasource; empty Format means json, empty Versions
// DefaultVersions (json/yaml only).
type Spec struct {
	URL, Format, Versions, VersionRegex string
}

// Source fetches its document once per run.
type Source struct {
	getter httpx.Getter
	name   string // custom.<name>
	spec   Spec
	sel    Selector
	re     *regexp.Regexp // nil = every value is a version
	group  int            // index of the `version` group in re; 0 = whole match

	releases datasource.Once[[]datasource.Release]
}

// New returns the datasource custom.<name>. sem bounds concurrent requests (nil = unbounded).
func New(client *http.Client, sem chan struct{}, name string, spec Spec) (*Source, error) {
	s := &Source{getter: httpx.Getter{Client: client, Sem: sem, MaxBody: MaxBody}, name: Prefix + name, spec: spec}
	if s.spec.Format == "" {
		s.spec.Format = FormatJSON
	}
	switch s.spec.Format {
	case FormatJSON, FormatYAML:
		var err error
		if s.sel, err = ParseSelector(cmp.Or(s.spec.Versions, DefaultVersions)); err != nil {
			return nil, fmt.Errorf("%s: versions: %w", s.name, err)
		}
	case FormatPlain:
		if s.spec.Versions != "" {
			return nil, fmt.Errorf("%s: versions: not allowed with format plain", s.name)
		}
	default:
		return nil, fmt.Errorf("%s: format: must be one of %s, got %q", s.name, strings.Join(Formats, ", "), s.spec.Format)
	}
	if s.spec.VersionRegex != "" {
		re, err := regexp.Compile(s.spec.VersionRegex)
		if err != nil {
			return nil, fmt.Errorf("%s: versionRegex: %w", s.name, err)
		}
		s.re = re
		s.group = max(re.SubexpIndex("version"), 0)
	}
	return s, nil
}

// Releases returns the selected versions, in document order without duplicates; d and dir
// are ignored, every dep of this datasource shares the one list.
func (s *Source) Releases(ctx context.Context, _ dep.Dep, _ string) ([]datasource.Release, error) {
	return s.releases.Do(ctx, func() ([]datasource.Release, error) {
		start := time.Now()
		rels, err := s.fetch(ctx)
		slog.DebugContext(ctx, "custom datasource fetched", "datasource", s.name, "url", s.spec.URL,
			"releases", len(rels), "duration", time.Since(start), "err", err)
		return rels, err
	})
}

// Digest is unsupported.
func (s *Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(s.name, d)
}

func (s *Source) fetch(ctx context.Context) ([]datasource.Release, error) {
	body, err := s.get(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.name, err)
	}
	values, err := s.values(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", s.name, s.spec.URL, err)
	}
	out := make([]datasource.Release, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if s.re != nil {
			m := s.re.FindStringSubmatchIndex(v)
			if len(m) == 0 || m[2*s.group] < 0 {
				continue
			}
			v = v[m[2*s.group]:m[2*s.group+1]]
		}
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, datasource.Release{Version: v})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: %s: no versions", s.name, s.spec.URL)
	}
	return out, nil
}

func (s *Source) get(ctx context.Context) ([]byte, error) {
	var body []byte
	err := s.getter.Stream(ctx, s.spec.URL, func(r io.Reader) (err error) {
		body, err = io.ReadAll(r)
		return err
	})
	return body, err
}

// values decodes body per format and returns the selected values.
func (s *Source) values(body []byte) ([]string, error) {
	switch s.spec.Format {
	case FormatPlain:
		var out []string
		for line := range strings.Lines(string(body)) {
			if line = strings.TrimSpace(line); line != "" && line[0] != '#' {
				out = append(out, line)
			}
		}
		return out, nil
	case FormatYAML:
		var n yaml.Node
		if err := yaml.Unmarshal(body, &n); err != nil {
			return nil, err
		}
		doc, err := fromYAML(&n)
		if err != nil {
			return nil, err
		}
		return s.sel.Select(doc)
	}
	var doc any
	if err := json.Unmarshal(body, &doc, keepNumbers); err != nil {
		return nil, err
	}
	return s.sel.Select(doc)
}

// keepNumbers decodes JSON numbers into `any` as their source text (jsontext.Value), so `1.20`
// stays `1.20` as in YAML documents, instead of the float64 1.2.
var keepNumbers = json.WithUnmarshalers(json.UnmarshalFromFunc(func(dec *jsontext.Decoder, v *any) error {
	if dec.PeekKind() == '0' {
		*v = jsontext.Value(nil)
	}
	return errors.ErrUnsupported // decode into the preset type, or as usual
}))

// fromYAML converts a yaml node to the selector's document model. Scalars stay text as written
// (`1.20` is not the number 1.2), nulls are nil.
func fromYAML(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return fromYAML(n.Content[0])
	case yaml.AliasNode:
		return fromYAML(n.Alias)
	case yaml.MappingNode:
		m := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			v, err := fromYAML(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			m[n.Content[i].Value] = v
		}
		return m, nil
	case yaml.SequenceNode:
		s := make([]any, len(n.Content))
		for i, c := range n.Content {
			v, err := fromYAML(c)
			if err != nil {
				return nil, err
			}
			s[i] = v
		}
		return s, nil
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			return nil, nil
		}
		return n.Value, nil
	}
	return nil, errors.New("unsupported yaml node")
}
