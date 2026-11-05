package helmds

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"
)

// indexVersion is what deps keeps of one chart version of an index.yaml.
type indexVersion struct {
	Version    string   `yaml:"version"`
	AppVersion string   `yaml:"appVersion"`
	Created    string   `yaml:"created"`
	Digest     string   `yaml:"digest"`
	URLs       []string `yaml:"urls"`
}

// scanIndex streams a repository index.yaml and returns the releases of the wanted charts;
// every other chart is skipped line by line without being parsed or kept. Index files are
// written by `helm repo index` in block style: `entries:` at column 0, one key per chart at a
// common indentation, its version list below. Only the lines of a wanted chart are buffered and
// decoded. A JSON (flow style) index is decoded whole. Versions without download URLs are
// dropped, as Helm ignores them.
func scanIndex(r io.Reader, wanted map[string]bool) (map[string][]Release, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	if first, err := firstByte(br); err != nil {
		return nil, err
	} else if first == '{' {
		return decodeIndex(br, wanted)
	}
	out := make(map[string][]Release, len(wanted))
	var (
		inEntries bool
		keyIndent = -1
		chart     string // chart being captured, "" = none
		block     []byte // its lines, dedented by keyIndent
		scratch   []byte
	)
	flush := func() error {
		if chart == "" {
			return nil
		}
		rels, err := decodeChart(block, chart)
		if err != nil {
			return fmt.Errorf("chart %s: %w", chart, err)
		}
		out[chart] = rels
		chart, block = "", block[:0]
		return nil
	}
	for {
		line, err := readLine(br, &scratch)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if len(line) > 0 || err == nil {
			indent := len(line) - len(bytes.TrimLeft(line, " "))
			content := line[indent:]
			switch {
			case len(content) == 0 || content[0] == '#':
				if chart != "" {
					block = append(append(block, content...), '\n')
				}
			case indent == 0:
				if err := flush(); err != nil {
					return nil, err
				}
				inEntries = false
				if rest, ok := bytes.CutPrefix(content, []byte("entries:")); ok {
					switch rest = bytes.TrimSpace(rest); {
					case len(rest) == 0 || rest[0] == '#':
						inEntries, keyIndent = true, -1
					case string(rest) != "{}":
						return nil, errors.New("unsupported index.yaml layout: flow-style entries")
					}
				}
			case !inEntries:
			case keyIndent < 0 && content[0] == '-', keyIndent >= 0 && indent < keyIndent:
				return nil, errors.New("unsupported index.yaml layout: misindented entries")
			case keyIndent < 0 || indent == keyIndent && content[0] != '-':
				keyIndent = indent
				if err := flush(); err != nil {
					return nil, err
				}
				if name := chartKey(content); wanted[name] {
					chart = name
					block = append(append(block, content...), '\n')
				}
			case chart != "":
				block = append(append(block, line[keyIndent:]...), '\n')
			}
		}
		if errors.Is(err, io.EOF) {
			return out, flush()
		}
	}
}

// firstByte returns the first byte of br that is neither whitespace nor in a comment line, without
// consuming it; 0 for an empty index.
func firstByte(br *bufio.Reader) (byte, error) {
	for n := 1; ; n++ {
		b, err := br.Peek(n)
		if len(b) < n {
			if errors.Is(err, io.EOF) {
				return 0, nil
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				return 0, nil
			}
			return 0, err
		}
		switch c := b[n-1]; c {
		case ' ', '\t', '\r', '\n':
		default:
			return c, nil
		}
	}
}

// readLine returns the next line of br without its line break; at the end of input it returns
// the unterminated last line (possibly empty) with io.EOF. Lines longer than br's buffer are
// assembled in *scratch.
func readLine(br *bufio.Reader, scratch *[]byte) ([]byte, error) {
	line, err := br.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		*scratch = append((*scratch)[:0], line...)
		for errors.Is(err, bufio.ErrBufferFull) {
			line, err = br.ReadSlice('\n')
			*scratch = append(*scratch, line...)
		}
		line = *scratch
	}
	line = bytes.TrimRight(line, "\r\n")
	return line, err
}

// chartKey returns the chart name of an entries key line `name:` (optionally quoted).
func chartKey(content []byte) string {
	k, _, _ := bytes.Cut(content, []byte(":"))
	k = bytes.TrimSpace(k)
	if n := len(k); n >= 2 && (k[0] == '"' || k[0] == '\'') && k[n-1] == k[0] {
		k = k[1 : n-1]
	}
	return string(k)
}

// decodeChart decodes one captured `name:` block of entries.
func decodeChart(block []byte, chart string) ([]Release, error) {
	var m map[string][]indexVersion
	if err := yaml.Unmarshal(block, &m); err != nil {
		return nil, err
	}
	return releases(m[chart]), nil
}

// decodeIndex decodes a whole (JSON) index and keeps the wanted charts.
func decodeIndex(r io.Reader, wanted map[string]bool) (map[string][]Release, error) {
	var idx struct {
		Entries map[string][]indexVersion `yaml:"entries"`
	}
	if err := yaml.NewDecoder(r).Decode(&idx); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	out := make(map[string][]Release, len(wanted))
	for name, vs := range idx.Entries {
		if wanted[name] {
			out[name] = releases(vs)
		}
	}
	return out, nil
}

// releases converts the index versions with download URLs.
func releases(vs []indexVersion) []Release {
	out := make([]Release, 0, len(vs))
	for _, v := range vs {
		if v.Version == "" || len(v.URLs) == 0 {
			continue
		}
		created, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(v.Created))
		out = append(out, Release{Version: v.Version, AppVersion: v.AppVersion, Created: created, Digest: v.Digest})
	}
	return out
}
