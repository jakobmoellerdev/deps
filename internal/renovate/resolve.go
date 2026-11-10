package renovate

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
)

// originKey is the hidden key holding the provenance of a packageRules or customManagers
// element ("packageRules[3]", "config:recommended packageRules[0]").
const originKey = "\x00origin"

// presetRef is a parsed extends entry (Renovate lib/config/presets/parse.ts).
type presetRef struct {
	source string // "internal", "github", "gitea", "forgejo", "local"
	repo   string // internal: the preset package (default, config, …); else owner/name
	name   string // internal: the preset name; remote: presetName (file[/preset[/sub]])
	path   string // remote: presetPath (directory of the preset file), "" = repository root
	tag    string
	params []string
}

// builtinPackages are the package names of Renovate's built-in presets.
var builtinPackages = []string{"abandonments", "compatibility", "config", "customManagers", "default", "docker", "global", "group", "helpers", "mergeConfidence", "monorepo", "npm", "packages", "preview", "replacements", "schedule", "security", "workarounds"}

// errUnsupportedSource marks presets of sources deps does not fetch.
type errUnsupportedSource struct{}

func (errUnsupportedSource) Error() string { return "unsupported preset source, skipped" }

// parsePreset parses an extends entry; presets of unsupported sources (npm>, npm packages,
// URLs, relative files) return errUnsupportedSource.
func parsePreset(s string) (presetRef, error) {
	var ref presetRef
	str := strings.TrimSpace(s)
	if str == "" {
		return ref, errors.New("empty preset")
	}
	for _, src := range []string{"github", "gitlab", "gitea", "forgejo", "local"} {
		if rest, ok := strings.CutPrefix(str, src+">"); ok {
			ref.source, str = src, rest
			break
		}
	}
	if ref.source == "" {
		switch {
		case strings.HasPrefix(str, "npm>"), strings.HasPrefix(str, "http://"),
			strings.HasPrefix(str, "https://"), strings.HasPrefix(str, "./"), strings.HasPrefix(str, "../"), strings.HasPrefix(str, "@"):
			return ref, errUnsupportedSource{}
		case !strings.HasPrefix(str, ":") && strings.Contains(str, "/"):
			ref.source = "local"
		}
	}
	if i := strings.IndexByte(str, '('); i >= 0 && strings.HasSuffix(str, ")") {
		for p := range strings.SplitSeq(str[i+1:len(str)-1], ",") {
			ref.params = append(ref.params, strings.TrimSpace(p))
		}
		str = str[:i]
	}
	if ref.source == "" {
		if rest, ok := strings.CutPrefix(str, ":"); ok {
			ref.source, ref.repo, ref.name = "internal", "default", rest
			return ref, nil
		}
		pkg, name, ok := strings.Cut(str, ":")
		if !ok || !slices.Contains(builtinPackages, pkg) {
			return ref, errUnsupportedSource{} // a plain npm package preset
		}
		ref.source, ref.repo, ref.name = "internal", pkg, name
		return ref, nil
	}
	if repo, presetPath, ok := strings.Cut(str, "//"); ok {
		presetPath, ref.tag, _ = strings.Cut(presetPath, "#")
		ref.repo = repo
		ref.path, ref.name = path.Dir(presetPath), path.Base(presetPath)
		if ref.path == "." {
			ref.path = ""
		}
	} else {
		str, ref.tag, _ = strings.Cut(str, "#")
		repo, name, _ := strings.Cut(str, ":")
		ref.repo, ref.name = repo, name
		if ref.name == "" {
			ref.name = "default"
		}
	}
	if strings.Count(ref.repo, "/") < 1 || ref.name == "" {
		return ref, fmt.Errorf("invalid preset %q", s)
	}
	return ref, nil
}

// mergeableKeys are the options Renovate merges instead of replacing (mergeable: true in
// lib/config/options/index.ts); every match* key and every manager block is mergeable too.
var mergeableKeys = map[string]bool{
	"packageRules": true, "customManagers": true, "ignoreDeps": true, "hostRules": true, "postUpdateOptions": true,
	"customDatasources": true, "major": true, "minor": true, "patch": true, "pin": true, "digest": true,
	"pinDigest": true, "rollback": true, "replacement": true, "vulnerabilityAlerts": true,
	"lockFileMaintenance": true, "managerFilePatterns": true, "addLabels": true, "commitTrailers": true,
	"prBodyNotes": true,
}

func mergeable(key string) bool {
	_, manager := managerMap[key]
	return mergeableKeys[key] || strings.HasPrefix(key, "match") || manager
}

// merge overlays child on parent like Renovate's mergeChildConfig (lib/config/utils.ts):
// mergeable arrays are concatenated (parent first), mergeable objects merged recursively,
// everything else is replaced.
func merge(parent, child map[string]any) map[string]any {
	out := maps.Clone(parent)
	if out == nil {
		out = make(map[string]any, len(child))
	}
	for k, cv := range child {
		pv, ok := parent[k]
		if ok && pv != nil && cv != nil && mergeable(k) {
			pa, pIsList := pv.([]any)
			ca, cIsList := cv.([]any)
			pm, pIsObj := pv.(map[string]any)
			cm, cIsObj := cv.(map[string]any)
			switch {
			case pIsList && cIsList:
				out[k] = slices.Concat(pa, ca)
				continue
			case pIsObj && cIsObj:
				out[k] = merge(pm, cm)
				continue
			}
		}
		out[k] = cv
	}
	return out
}

// resolver expands extends entries.
type resolver struct {
	ctx       context.Context
	fetcher   *Fetcher
	notes     *notes
	monorepos bool
	// replacements holds the replacements:* presets in the chain (see translator.replacements).
	replacements replacementPresets
	// remoteKeys are the top-level keys of fetched presets (see translator.builtinOnly).
	remoteKeys map[string]bool
}

// extendsList returns the extends entries of m; a string counts as a one-element list.
func extendsList(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// resolve returns m with its extends (and those of nested objects, e.g. packageRules
// elements) merged in: each preset, resolved recursively, in order, then m's own keys.
// ignore is the inherited ignorePresets list; as in Renovate's resolveConfigPresets, a
// non-empty inherited list wins over m's own, and entries match extends strings exactly.
func (r *resolver) resolve(m map[string]any, chain, ignore []string) (map[string]any, error) {
	if len(ignore) == 0 {
		ignore = extendsList(m["ignorePresets"])
	}
	base := map[string]any{}
	for i, e := range extendsList(m["extends"]) {
		if slices.Contains(ignore, e) {
			r.notes.info(e, "listed in ignorePresets, skipped")
			continue
		}
		key, _, _ := strings.Cut(e, "(") // a preset with other params is the same preset
		if slices.Contains(chain, key) {
			r.notes.warn(e, "preset cycle, skipped")
			continue
		}
		p, err := r.preset(e, i)
		if err != nil {
			return nil, err
		}
		if p == nil {
			continue
		}
		next := append(slices.Clip(chain), key)
		if p, err = r.resolve(p, next, ignore); err != nil {
			return nil, err
		}
		base = merge(base, p)
	}
	own := make(map[string]any, len(m))
	for k, v := range m {
		if k == "extends" || k == "ignorePresets" {
			continue
		}
		var err error
		switch v := v.(type) {
		case map[string]any:
			if own[k], err = r.resolve(v, chain, ignore); err != nil {
				return nil, err
			}
			continue
		case []any:
			list := slices.Clone(v)
			for j, el := range list {
				if em, ok := el.(map[string]any); ok {
					if list[j], err = r.resolve(em, chain, ignore); err != nil {
						return nil, err
					}
				}
			}
			own[k] = list
			continue
		}
		own[k] = v
	}
	return merge(base, own), nil
}

var groupMonorepoRe = regexp.MustCompile(`^group:.+Monorepo$`)

// preset loads the fragment of extends entry e (index i of its list); nil means skipped.
func (r *resolver) preset(e string, i int) (map[string]any, error) {
	ref, err := parsePreset(e)
	if err != nil {
		r.notes.warn(e, "%v", err)
		return nil, nil // reported as a note, the preset is skipped
	}
	var frag map[string]any
	if ref.source == "internal" {
		name := ref.repo + ":" + ref.name
		switch {
		case name == "group:monorepos", name == "group:recommended", name == "group:goOpenapi",
			groupMonorepoRe.MatchString(name), ref.repo == "monorepo":
			r.monorepos = true
			r.notes.info(e, "deps' built-in monorepo groups (builtins.monorepos: true)")
			return nil, nil
		case name == "helpers:pinGitHubActionDigests":
		case ref.repo == "replacements":
			r.replacements.add(e, ref.name)
			return nil, nil
		case slices.Contains([]string{"workarounds", "mergeConfidence", "helpers"}, ref.repo),
			name == "security:openssf-scorecard", name == "security:php2shell":
			r.notes.info(e, "no deps equivalent")
			return nil, nil
		}
		src, ok := builtin[name]
		if !ok {
			r.notes.warn(e, "unknown built-in preset, skipped")
			return nil, nil
		}
		v, err := parseJSON5([]byte(src))
		if err != nil {
			return nil, fmt.Errorf("built-in preset %s: %w", name, err) // the table is broken
		}
		frag, _ = v.(map[string]any)
	} else {
		if frag, err = r.fetcher.fetch(r.ctx, ref, fmt.Sprintf("extends[%d] %q", i, e), r.notes); err != nil {
			return nil, err
		}
		if r.remoteKeys == nil {
			r.remoteKeys = map[string]bool{}
		}
		for k := range frag {
			r.remoteKeys[k] = true
		}
	}
	if len(ref.params) > 0 {
		frag, _ = applyParams(frag, ref.params).(map[string]any)
	}
	setOrigins(frag, e+" ")
	return frag, nil
}

var argRe = regexp.MustCompile(`\{\{\s*arg(\d+)\s*\}\}`)

// applyParams replaces {{argN}} in every string of v with params[N].
func applyParams(v any, params []string) any {
	switch v := v.(type) {
	case string:
		return argRe.ReplaceAllStringFunc(v, func(m string) string {
			n, _ := strconv.Atoi(argRe.FindStringSubmatch(m)[1])
			if n < len(params) {
				return params[n]
			}
			return m
		})
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = applyParams(e, params)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = applyParams(e, params)
		}
		return out
	}
	return v
}

// setOrigins records the provenance of the packageRules and customManagers elements of a
// loaded fragment; prefix is "" for the repository file, else "<preset> ".
func setOrigins(frag map[string]any, prefix string) {
	for _, key := range []string{"packageRules", "customManagers", "regexManagers"} {
		list, _ := frag[key].([]any)
		for i, el := range list {
			if m, ok := el.(map[string]any); ok {
				m[originKey] = fmt.Sprintf("%s%s[%d]", prefix, key, i)
			}
		}
	}
}

// configErr wraps a config problem as *config.Error.
func configErr(format string, a ...any) error {
	return &config.Error{Err: fmt.Errorf(format, a...)}
}
