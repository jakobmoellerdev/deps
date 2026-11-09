package bundler

// podDecl is a `pod` statement of a Podfile.
type podDecl struct {
	name string
	line int
	reqs []token // the literal requirement strings
	// nonLiteral: a requirement argument that is not a literal string (`:head`, a variable).
	nonLiteral bool
	skip       string // path, podspec, external or non-literal source
	source     string // own spec repository (`:source`)
	ownSource  bool
	git        *token // the literal `:git` URL; nil = none
	tag        *token // the literal `:tag` of a git pod; nil = none
	gitRef     bool   // a `:branch` or `:commit` option, or a `:git` or `:tag` that is not a literal string
}

// podfile is what parsePodfile finds in a Podfile.
type podfile struct {
	pods    []podDecl
	sources []string // the `source "url"` statements, in order
}

// parsePodfile finds the pods and spec repositories of a Podfile. `pod` statements count at
// any depth (target, abstract_target and def blocks, conditionals); `source` is a global
// setting wherever it is written.
func parsePodfile(src []byte) podfile {
	var f podfile
	for _, s := range statements(lex(src)) {
		if s[0].kind != tIdent {
			continue
		}
		switch s[0].text {
		case "pod":
			if p, ok := parsePod(s); ok {
				f.pods = append(f.pods, p)
			}
		case "source":
			if url, ok := podSourceArg(s); ok && blockDo(s) < 0 {
				f.sources = append(f.sources, url)
			}
		}
	}
	return f
}

// podSourceArg returns the literal string argument of the call s (`source "url"`).
func podSourceArg(s []token) (string, bool) {
	args, paren := callArgs(s)
	parts := splitArgs(args, paren)
	if len(parts) != 1 || len(parts[0]) != 1 || parts[0][0].kind != tString || !parts[0][0].literal || parts[0][0].text == "" {
		return "", false
	}
	return parts[0][0].text, true
}

// parsePod parses `pod "name", <requirements…>, <options…>` (s starts with `pod`). ok is false
// when the name is not a literal string.
func parsePod(s []token) (podDecl, bool) {
	args, paren := callArgs(s)
	var p podDecl
	keywords := false
	for i, arg := range splitArgs(args, paren) {
		switch {
		case i == 0:
			if len(arg) != 1 || arg[0].kind != tString || !arg[0].literal || arg[0].text == "" {
				return podDecl{}, false
			}
			p.name, p.line = arg[0].text, s[0].line
			continue
		case !keywords && isKeyword(arg):
			keywords = true
		}
		if !keywords {
			reqs, ok := literalStrings(arg)
			p.reqs = append(p.reqs, reqs...)
			p.nonLiteral = p.nonLiteral || !ok
			continue
		}
		key, value := keyword(arg)
		var lit *token
		if len(value) == 1 && value[0].kind == tString && value[0].literal && value[0].text != "" {
			lit = &value[0]
		}
		switch key {
		case "git":
			p.git = lit
			p.gitRef = p.gitRef || lit == nil
		case "tag":
			p.tag = lit
			p.gitRef = p.gitRef || lit == nil
		case "branch", "commit":
			p.gitRef = true
		case "path":
			p.skip = skipPath
		case "podspec":
			p.skip = skipPodspec
		case "http", "hg", "svn":
			p.skip = skipExternal
		case "source":
			p.ownSource = true
			if lit != nil {
				p.source = lit.text
			} else if p.skip == "" {
				p.skip = skipNonLiteralSource
			}
		}
	}
	return p, true
}
