package docker

import (
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/imageref"
)

// TypeInstall is the dep type of the packages a Dockerfile `RUN` installs with a version
// pin (Renovate's `install`).
const TypeInstall = "install"

// Skip reasons of installed packages.
const skipConstraint = "version constraint"

var (
	// alpineTagRe matches an Alpine release in the tag of an image built on Alpine:
	// `3.21-alpine3.20`, `alpine3.21`, `1.23-alpine3.21.3-slim`.
	alpineTagRe = regexp.MustCompile(`(?:^|[-_])alpine(\d+\.\d+)(?:\.\d+)?(?:[-_]|$)`)
	// alpineReleaseRe matches the tag of the alpine image naming a release: `3.21`, `3.21.3`.
	alpineReleaseRe = regexp.MustCompile(`^(\d+\.\d+)(?:\.\d+)?$`)
)

// apkArches maps docker platforms (`--platform=linux/arm64`) to apk architectures.
var apkArches = map[string]string{
	"linux/amd64": "x86_64", "linux/arm64": "aarch64", "linux/arm64/v8": "aarch64", "linux/arm/v7": "armv7",
	"linux/arm/v6": "armhf", "linux/386": "x86", "linux/ppc64le": "ppc64le", "linux/s390x": "s390x",
	"linux/riscv64": "riscv64", "linux/loong64": "loongarch64",
}

// apkRegistry returns the apk registry URL (apk datasource form) the base image ref of a
// FROM installs from: the alpine image's release (`alpine:3.21` → branch v3.21, `alpine:edge`
// → edge), an `alpine<X.Y>` tag of another image (`node:22-alpine3.21`), or Wolfi for
// Chainguard images; "" when unknown (`alpine:latest`, `alpine:3`, `node:22-alpine`).
// platform is the FROM's `--platform` value; other platforms than linux ones known, and
// none, read the x86_64 index.
func apkRegistry(ref, platform string) string {
	r, err := imageref.Parse(ref)
	if err != nil {
		return ""
	}
	arch := apkArches[platform]
	if arch == "" {
		arch = "x86_64"
	}
	if strings.HasPrefix(r.Name, "cgr.dev/chainguard/") || r.Name == "docker.io/chainguard/wolfi-base" {
		return "https://packages.wolfi.dev/os?arch=" + arch
	}
	branch := ""
	if m := alpineTagRe.FindStringSubmatch(r.Tag); m != nil {
		branch = "v" + m[1]
	} else if r.Name == "docker.io/library/alpine" {
		if m := alpineReleaseRe.FindStringSubmatch(r.Tag); m != nil {
			branch = "v" + m[1]
		} else if r.Tag == "edge" {
			branch = "edge"
		}
	}
	if branch == "" {
		return ""
	}
	return "https://dl-cdn.alpinelinux.org/alpine?branch=" + branch + "&components=main,community&arch=" + arch
}

// expand replaces the ARG references of ref (`$X`, `${X}`) that have a literal default in
// scope by it; others stay as written, for a registry alias to resolve.
func expand(ref string, st *stageState) string {
	if !strings.Contains(ref, "$") {
		return ref
	}
	var b strings.Builder
	for i := 0; i < len(ref); {
		n, name := varRefAt(ref[i:])
		a, ok := st.lookup(name)
		if n == 0 || !ok || a.skip {
			n = max(n, 1)
			b.WriteString(ref[i : i+n])
		} else {
			b.WriteString(a.val)
		}
		i += n
	}
	return b.String()
}

// stageRegistry sets the apk registry of the stage a FROM starts (image is its base as
// written, platform its `--platform`, name its `AS` name or ""): an earlier stage's, else
// apkRegistry of the image with its ARGs and registry alias resolved ("" with variables left).
func (x *extractor) stageRegistry(image, platform, name string, st *stageState) {
	reg, ok := st.registries[strings.ToLower(image)]
	if !ok {
		ref := expand(image, st)
		if v, rest, ok := alias(ref, x.aliases); ok {
			ref = v + ref[rest:]
		}
		if !strings.Contains(ref, "$") {
			reg = apkRegistry(ref, expand(platform, st))
		}
	}
	st.apkRegistry = reg
	if name != "" {
		st.registries[strings.ToLower(name)] = reg
	}
}

// installs extracts the version-pinned packages of `apk add` and `apt-get install` / `apt
// install` commands in the shell form arguments of a RUN (flags already skipped). apk
// packages are looked up in the stage's base image registry (stageRegistry); Debian
// packages, whose suite deps does not derive, are skipped as unknown registry until a rule
// sets registryUrls.
func (x *extractor) installs(args []token, st *stageState) {
	for len(args) > 0 {
		end := 0
		for end < len(args) && !separator(x.text(args[end])) {
			end++
		}
		cmd := args[:end]
		// A token ending in ";" ends the command after itself: `add x=1;`.
		for i, t := range cmd {
			if s := x.text(t); len(s) > 1 && strings.HasSuffix(s, ";") {
				cmd, end = cmd[:i+1], i
				cmd[i].end--
				break
			}
		}
		x.command(cmd, st)
		args = args[min(end+1, len(args)):]
	}
}

// separator reports whether s is a shell token separating commands.
func separator(s string) bool {
	switch s {
	case "&&", "||", ";", "|", "&":
		return true
	}
	return false
}

// apkValueOpts are the apk options taking the next token as their value; those in
// apkRepoOpts change where packages come from.
var (
	apkValueOpts = map[string]bool{"-t": true, "--virtual": true, "-X": true, "--repository": true, "-p": true, "--root": true,
		"--arch": true, "--repositories-file": true, "--keys-dir": true, "--cache-dir": true, "--cache-max-age": true, "--timeout": true}
	apkRepoOpts  = map[string]bool{"-X": true, "--repository": true, "-p": true, "--root": true, "--arch": true, "--repositories-file": true}
	aptValueOpts = map[string]bool{"-o": true, "--option": true, "-t": true, "--target-release": true, "--default-release": true,
		"-c": true, "--config-file": true}
)

// command extracts the packages of one shell command: `apk [opts] add [opts] pkgs...` or
// `apt-get|apt [opts] install [opts] pkgs...`.
func (x *extractor) command(cmd []token, st *stageState) {
	if len(cmd) < 3 {
		return
	}
	tool := x.text(cmd[0])
	tool = tool[strings.LastIndexByte(tool, '/')+1:]
	var sub string
	var valueOpts map[string]bool
	switch tool {
	case "apk":
		sub, valueOpts = "add", apkValueOpts
	case "apt-get", "apt":
		sub, valueOpts = "install", aptValueOpts
	default:
		return
	}
	otherRepo := false
	seenSub := false
	for i := 1; i < len(cmd); i++ {
		s := x.text(cmd[i])
		if strings.HasPrefix(s, "-") {
			opt, _, inline := strings.Cut(s, "=")
			if tool == "apk" && apkRepoOpts[opt] {
				otherRepo = true
			}
			if valueOpts[opt] && !inline {
				i++
			}
			continue
		}
		if !seenSub {
			if s != sub {
				return
			}
			seenSub = true
			continue
		}
		if tool == "apk" {
			x.apkPackage(cmd[i], otherRepo, st)
		} else {
			x.debPackage(cmd[i])
		}
	}
}

// unquote strips one pair of matching quotes around the token's text; ok is false for
// unbalanced quotes.
func (x *extractor) unquote(t token) (token, bool) {
	s := x.text(t)
	if s == "" || s[0] != '"' && s[0] != '\'' {
		return t, !strings.ContainsAny(s, `"'`)
	}
	if len(s) < 2 || s[len(s)-1] != s[0] || strings.ContainsAny(s[1:len(s)-1], `"'`) {
		return t, false
	}
	return token{t.start + 1, t.end - 1}, true
}

// pkgDep returns the dep of package name pinned to the version at t[off:].
func (x *extractor) pkgDep(t token, name string, off int, datasource string) dep.Dep {
	start := t.start + off
	return dep.Dep{Manager: Name, Datasource: datasource, Versioning: datasource, DepType: TypeInstall, File: x.file,
		Line: x.lineOf(start), Name: name, Version: string(x.src[start:t.end]), RefSpan: dep.Span{Start: start, End: t.end}}
}

// validName reports whether name is a literal package name: no variable, path, provider
// (`so:`, `cmd:`), virtual (`.build-deps`) or apt pattern (`^gnome`).
func validName(name string) bool {
	if name == "" || !(name[0] >= 'a' && name[0] <= 'z' || name[0] >= 'A' && name[0] <= 'Z' || name[0] >= '0' && name[0] <= '9') {
		return false
	}
	return !strings.ContainsAny(name, "$/:*?[]{}()`\\")
}

// apkPackage extracts an `apk add` argument `name=<version>`. Other constraints (`~`, `=~`,
// `<`, `>`, `><`) are skipped as version constraint; a repository tag (`name@edge=1`) or a
// command reading other repositories (otherRepo) as unknown registry. Packages without
// a version, files and providers are not deps.
func (x *extractor) apkPackage(t token, otherRepo bool, st *stageState) {
	t, ok := x.unquote(t)
	if !ok {
		return
	}
	s := x.text(t)
	i := strings.IndexAny(s, "=<>~")
	if i < 0 {
		return
	}
	name, tagged := s[:i], false
	if n, _, ok := strings.Cut(name, "@"); ok {
		name, tagged = n, true
	}
	if !validName(name) {
		return
	}
	d := x.pkgDep(t, name, i+1, "apk")
	switch {
	case s[i] != '=' || strings.ContainsAny(s[i+1:], "=<>~"):
		d.Skip = skipConstraint
	case strings.Contains(d.Version, "$"):
		d.Skip = skipVariable
	case d.Version == "":
		return
	case tagged || otherRepo || st.apkRegistry == "":
		d.Skip = dep.SkipUnknownRegistry
	default:
		d.Source = st.apkRegistry
	}
	x.deps = append(x.deps, d)
}

// debPackage extracts an `apt-get install` argument `name=<version>`, skipped as unknown
// registry (a rule's registryUrls supplies the suite). Wildcard versions (`8.14.*`) are
// skipped as version constraint; packages without a version, release pins (`curl/trixie`),
// architecture-qualified names (`libc6:i386`), removals (`vim-`), patterns and files are not
// deps.
func (x *extractor) debPackage(t token) {
	t, ok := x.unquote(t)
	if !ok {
		return
	}
	s := x.text(t)
	name, version, ok := strings.Cut(s, "=")
	if !ok || version == "" || !validName(name) {
		return
	}
	d := x.pkgDep(t, name, len(name)+1, "deb")
	switch {
	case strings.Contains(version, "$"):
		d.Skip = skipVariable
	case strings.ContainsAny(version, "*?"):
		d.Skip = skipConstraint
	default:
		d.Skip = dep.SkipUnknownRegistry
	}
	x.deps = append(x.deps, d)
}
