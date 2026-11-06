// Package sbtds lists the versions of Scala libraries (sbt-package) and sbt plugins
// (sbt-plugin) from Maven repositories, through the maven datasource, and from Ivy-layout
// repositories (the sbt plugin releases repository) by their directory listings.
package sbtds

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	mavends "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/maven"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Datasource names (Renovate's).
const (
	PackageName = "sbt-package"
	PluginName  = "sbt-plugin"
)

// PluginReleases is the Ivy-layout repository sbt resolves plugins from besides Maven Central
// (Classpaths.sbtPluginReleases).
const PluginReleases = "https://repo.scala-sbt.org/scalasbt/sbt-plugin-releases/"

// IvyID is the repository id that marks an Ivy-layout repository in a dep's Source
// (`ivy=<url>/`); other entries are Maven repositories.
const IvyID = "ivy"

// maxListing bounds the size of a directory listing.
const maxListing = 4 << 20

// core is shared by both datasources: the maven datasource, the cached Ivy listings and the
// platform artifacts Package.Releases fell back to.
type core struct {
	maven    *mavends.Source
	client   *http.Client
	sem      chan struct{}
	lists    datasource.Memo[string, listing]
	platform sync.Map // dep name + "\x00" + Source -> the Scala.js/Native artifact name found
}

// listing is the entries of one directory listing; notFound when the directory is missing.
type listing struct {
	names    []string
	notFound bool
}

// Package is the sbt-package datasource: a Scala library `group:artifact` whose artifact
// carries its Scala suffix (`cats-core_2.13`).
type Package struct{ *core }

// Plugin is the sbt-plugin datasource: an sbt plugin `group:artifact`, looked up as the
// artifact published for the dep's sbt version.
type Plugin struct{ *core }

// New returns the sbt-package and sbt-plugin datasources. Maven repositories are read through
// maven (its Maven Central override and caches apply); sem bounds the concurrent Ivy listing
// requests (nil = unbounded).
func New(maven *mavends.Source, client *http.Client, sem chan struct{}) (Package, Plugin) {
	c := &core{maven: maven, client: client, sem: sem}
	return Package{c}, Plugin{c}
}

// split returns the Maven repositories of a dep Source (as a maven datasource Source; "" =
// Maven Central) and its Ivy-layout repository URLs, each with a trailing slash. ok is false
// when the Source lists only Ivy repositories.
func split(src string) (maven string, ivy []string, ok bool) {
	fields := strings.Fields(src)
	if len(fields) == 0 {
		return "", nil, true
	}
	var mv []string
	for _, f := range fields {
		if u, isIvy := strings.CutPrefix(f, IvyID+"="); isIvy {
			ivy = append(ivy, withSlash(u))
			continue
		}
		mv = append(mv, f)
	}
	return strings.Join(mv, " "), ivy, len(mv) > 0
}

// coords splits `group:artifact`.
func coords(name string) (group, artifact string, err error) {
	group, artifact, ok := strings.Cut(name, ":")
	if !ok || group == "" || artifact == "" || strings.Contains(artifact, ":") {
		return "", "", fmt.Errorf("%q is not group:artifact", name)
	}
	return group, artifact, nil
}

// lookup merges the versions the maven datasource lists for md (when hasMaven) with those of
// the Ivy directories dirs, in that order. Missing artifacts contribute nothing; errors fail
// the lookup only when nothing lists the artifact.
func (c *core) lookup(ctx context.Context, md dep.Dep, dir string, hasMaven bool, dirs []string) ([]datasource.Release, error) {
	var (
		rels  []datasource.Release
		seen  = map[string]bool{}
		errs  []error
		found bool
	)
	add := func(v string) {
		if !seen[v] {
			seen[v] = true
			rels = append(rels, datasource.Release{Version: v})
		}
	}
	if hasMaven {
		mr, err := c.maven.Releases(ctx, md, dir)
		switch {
		case errors.Is(err, mavends.ErrNotFound):
		case err != nil:
			errs = append(errs, err)
		default:
			found = true
			for _, r := range mr {
				add(r.Version)
			}
		}
	}
	for _, u := range dirs {
		l, err := c.list(ctx, u)
		switch {
		case err != nil:
			errs = append(errs, err)
		case !l.notFound:
			found = true
			for _, v := range l.names {
				add(v)
			}
		}
	}
	switch {
	case found:
		if len(errs) > 0 {
			slog.DebugContext(ctx, "sbt repositories failed", "name", md.Name, "err", errors.Join(errs...))
		}
		return rels, nil
	case len(errs) > 0:
		return nil, errors.Join(errs...)
	}
	return nil, fmt.Errorf("artifact %s %w", md.Name, mavends.ErrNotFound)
}

// Releases lists the versions of the library in the dep's Maven repositories
// (maven-metadata.xml) and Ivy repositories (`<url>/<group>/<artifact>/`). A Scala artifact
// (`<name>_2.13`, `<name>_3`) none of them has is looked up as its Scala.js and Scala Native
// artifacts (`<name>_sjs1_2.13`, `<name>_native0.5_2.13`, `<name>_native0.4_2.13`): `%%%`
// names the platform's artifact, which the build's platform decides. Only an artifact every
// repository lacks falls back; a failing repository returns its error without more requests.
func (p Package) Releases(ctx context.Context, d dep.Dep, dir string) ([]datasource.Release, error) {
	rels, err := p.releases(ctx, d, dir)
	if err == nil || !errors.Is(err, mavends.ErrNotFound) {
		return rels, err
	}
	for _, name := range platformNames(d.Name) {
		pd := d
		pd.Name = name
		prels, perr := p.releases(ctx, pd, dir)
		switch {
		case perr == nil:
			p.platform.Store(d.Name+"\x00"+d.Source, name)
			return prels, nil
		case !errors.Is(perr, mavends.ErrNotFound):
			return nil, err
		}
	}
	return nil, err
}

func (p Package) releases(ctx context.Context, d dep.Dep, dir string) ([]datasource.Release, error) {
	g, a, err := coords(d.Name)
	if err != nil {
		return nil, err
	}
	src, ivy, hasMaven := split(d.Source)
	md := d
	md.Source = src
	dirs := make([]string, len(ivy))
	for i, u := range ivy {
		dirs[i] = u + g + "/" + a + "/"
	}
	return p.lookup(ctx, md, dir, hasMaven, dirs)
}

// platformNames returns the Scala.js and Scala Native artifacts of a Scala artifact name;
// none for a name that already names a platform (`_sjs1_2.13`, `_native0.5_3`).
func platformNames(name string) []string {
	i := strings.LastIndexByte(name, '_')
	if i < 0 || !strings.Contains(name[:i], ":") {
		return nil
	}
	if j := strings.LastIndexByte(name[:i], '_'); j >= 0 {
		if seg := name[j+1 : i]; strings.HasPrefix(seg, "sjs") || strings.HasPrefix(seg, "native") {
			return nil
		}
	}
	switch bin := name[i+1:]; bin {
	case "2.11", "2.12", "2.13", "3":
		base := name[:i]
		return []string{base + "_sjs1_" + bin, base + "_native0.5_" + bin, base + "_native0.4_" + bin}
	}
	return nil
}

// resolved returns d with the platform artifact name Releases found for it, and only its Maven
// repositories; ok is false when it has none.
func (p Package) resolved(d dep.Dep) (dep.Dep, bool) {
	if name, ok := p.platform.Load(d.Name + "\x00" + d.Source); ok {
		d.Name = name.(string)
	}
	src, _, hasMaven := split(d.Source)
	d.Source = src
	return d, hasMaven
}

// ReleaseTime implements datasource.Timer for versions listed by a Maven repository (the
// POM's Last-Modified); zero for versions only an Ivy repository lists.
func (p Package) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	md, hasMaven := p.resolved(d)
	if !hasMaven {
		return time.Time{}, nil
	}
	return p.maven.ReleaseTime(ctx, md, version)
}

// SourceRepo implements datasource.SourceRepo with the POM's scm of a Maven repository.
func (p Package) SourceRepo(ctx context.Context, d dep.Dep, version string) (string, error) {
	md, hasMaven := p.resolved(d)
	if !hasMaven {
		return "", nil
	}
	return p.maven.SourceRepo(ctx, md, version)
}

// Digest is unsupported.
func (Package) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(PackageName, d)
}

// crossVersion returns the Maven artifact suffix and the Ivy `scala_<v>/sbt_<v>/` directory
// ("" = none) of plugins for the sbt version v (sbt 0.13, 1.x by default, 2.x).
func crossVersion(v string) (suffix, ivyDir string) {
	switch {
	case strings.HasPrefix(v, "2."):
		return "_sbt2_3", ""
	case strings.HasPrefix(v, "0.13"):
		return "_2.10_0.13", "scala_2.10/sbt_0.13/"
	}
	return "_2.12_1.0", "scala_2.12/sbt_1.0/"
}

// pluginDep returns d as the Maven artifact of the plugin for its sbt version, with only the
// Maven repositories of its Source, and the Ivy directories to list.
func pluginDep(d dep.Dep) (md dep.Dep, hasMaven bool, dirs []string, err error) {
	g, a, err := coords(d.Name)
	if err != nil {
		return d, false, nil, err
	}
	src, ivy, hasMaven := split(d.Source)
	suffix, ivyDir := crossVersion(d.SbtVersion)
	md = d
	md.Name, md.Source = d.Name+suffix, src
	if ivyDir != "" {
		for _, u := range ivy {
			dirs = append(dirs, u+g+"/"+a+"/"+ivyDir)
		}
	}
	return md, hasMaven, dirs, nil
}

// Releases lists the versions of the plugin published for the dep's sbt version: the Maven
// artifact `<artifact>_2.12_1.0` (sbt 1.x; `_sbt2_3` for sbt 2.x, `_2.10_0.13` for 0.13) in
// the Maven repositories and `<url>/<group>/<artifact>/scala_2.12/sbt_1.0/` in the Ivy ones.
func (p Plugin) Releases(ctx context.Context, d dep.Dep, dir string) ([]datasource.Release, error) {
	md, hasMaven, dirs, err := pluginDep(d)
	if err != nil {
		return nil, err
	}
	return p.lookup(ctx, md, dir, hasMaven, dirs)
}

// ReleaseTime implements datasource.Timer as for Package, with the plugin's Maven artifact.
func (p Plugin) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	md, hasMaven, _, err := pluginDep(d)
	if err != nil || !hasMaven {
		return time.Time{}, err
	}
	return p.maven.ReleaseTime(ctx, md, version)
}

// SourceRepo implements datasource.SourceRepo as for Package, with the plugin's Maven artifact.
func (p Plugin) SourceRepo(ctx context.Context, d dep.Dep, version string) (string, error) {
	md, hasMaven, _, err := pluginDep(d)
	if err != nil || !hasMaven {
		return "", err
	}
	return p.maven.SourceRepo(ctx, md, version)
}

// Digest is unsupported.
func (Plugin) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(PluginName, d)
}

// hrefRe matches the links of a directory listing (Artifactory, Nexus, Maven Central, nginx).
var hrefRe = regexp.MustCompile(`(?i)<a\s[^>]*?href="([^"]+)"`)

// list returns the subdirectories of the directory listing at u once per run.
func (c *core) list(ctx context.Context, u string) (listing, error) {
	return c.lists.Do(u, func() (listing, error) {
		release, err := httpx.Acquire(ctx, c.sem)
		if err != nil {
			return listing{}, err
		}
		defer release()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
		if err != nil {
			return listing{}, err
		}
		req.Header.Set("User-Agent", httpx.UserAgent())
		resp, err := c.client.Do(req)
		if err != nil {
			return listing{}, err
		}
		defer func() { _ = resp.Body.Close() }()
		switch {
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
			return listing{notFound: true}, nil
		case resp.StatusCode != http.StatusOK:
			return listing{}, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxListing))
		if err != nil {
			return listing{}, fmt.Errorf("GET %s: %w", u, err)
		}
		names := subdirs(string(b))
		slog.DebugContext(ctx, "sbt directory listed", "url", u, "entries", len(names))
		return listing{names: names}, nil
	})
}

// subdirs returns the subdirectory names linked from a directory listing page: relative
// links ending in "/", without parent and hidden entries.
func subdirs(page string) []string {
	var out []string
	for _, m := range hrefRe.FindAllStringSubmatch(page, -1) {
		h := m[1]
		name, ok := strings.CutSuffix(h, "/")
		if !ok || name == "" || strings.ContainsAny(name, "/:?#") || strings.HasPrefix(name, ".") {
			continue
		}
		out = append(out, name)
	}
	return out
}

func withSlash(u string) string {
	if strings.HasSuffix(u, "/") {
		return u
	}
	return u + "/"
}
