package mavends

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// maxPOM bounds the size of a POM document.
const maxPOM = 4 << 20

// maxParentHops bounds how many <parent> POMs SourceRepo reads.
const maxParentHops = 3

// pomXML is the part of a POM SourceRepo reads.
type pomXML struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
	URL        string `xml:"url"`
	SCM        struct {
		URL                 string `xml:"url"`
		Connection          string `xml:"connection"`
		DeveloperConnection string `xml:"developerConnection"`
		Tag                 string `xml:"tag"`
	} `xml:"scm"`
	Parent       coords   `xml:"parent"`
	Dependencies []coords `xml:"dependencies>dependency"`
	Properties   props    `xml:"properties"`
}

type coords struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
}

// props collects the child elements of <properties>.
type props map[string]string

func (p *props) UnmarshalXML(dec *xml.Decoder, _ xml.StartElement) error {
	m := props{}
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			var v string
			if err := dec.DecodeElement(&v, &t); err != nil {
				return err
			}
			m[t.Name.Local] = strings.TrimSpace(v)
		case xml.EndElement:
			*p = m
			return nil
		}
	}
}

// pomEntry is the POM at a path in one repository; err is the lookup's failure.
type pomEntry struct {
	pom      *pomXML
	notFound bool
	err      error
}

// SourceRepo implements datasource.SourceRepo from the POM of version: `<scm><url>`, else
// `<scm><connection>`/`<developerConnection>` without `scm:git:`/`scm:`, else `<url>` on
// github.com or gitlab.com; when the POM has none, its <parent>'s (up to 3 hops). A Gradle
// plugin marker POM is followed to the plugin artifact it depends on. "" when unknown.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, version string) (string, error) {
	group, artifact, ok := strings.Cut(d.Name, ":")
	if !ok {
		artifact = d.Name + ".gradle.plugin"
		group = d.Name
	}
	if _, _, err := artifactPath(d.Name); err != nil {
		return "", err
	}
	repos, err := s.located(d)
	if err != nil {
		return "", err
	}
	p, err := s.findPOM(ctx, repos, coords{group, artifact, version})
	if p == nil || err != nil {
		return "", err
	}
	if strings.HasSuffix(artifact, ".gradle.plugin") && len(p.Dependencies) == 1 {
		// A plugin marker: its only dependency is the plugin implementation.
		if p, err = s.findPOM(ctx, repos, p.Dependencies[0]); p == nil || err != nil {
			return "", err
		}
	}
	leaf := p
	for hop := 0; ; hop++ {
		if r := scmRepo(p, leaf); r != "" {
			return r, nil
		}
		if hop == maxParentHops || p.Parent.ArtifactID == "" {
			return "", nil
		}
		if p, err = s.findPOM(ctx, repos, p.Parent); p == nil || err != nil {
			return "", err
		}
	}
}

// scmRepo returns the repository URL p declares, interpolated in the context of leaf.
func scmRepo(p, leaf *pomXML) string {
	if u := interpolate(p.SCM.URL, p, leaf); u != "" {
		return u
	}
	for _, c := range [...]string{p.SCM.Connection, p.SCM.DeveloperConnection} {
		c = interpolate(c, p, leaf)
		if rest, ok := strings.CutPrefix(c, "scm:git:"); ok {
			c = rest
		} else {
			c = strings.TrimPrefix(c, "scm:")
		}
		if c != "" {
			return c
		}
	}
	if u := interpolate(p.URL, p, leaf); u != "" {
		if pu, err := url.Parse(u); err == nil {
			switch strings.TrimPrefix(strings.ToLower(pu.Hostname()), "www.") {
			case "github.com", "gitlab.com":
				return u
			}
		}
	}
	return ""
}

// interpolate expands `${project.groupId|artifactId|version}` (of leaf, the POM being
// inherited into), `${project.scm.tag}` and p's <properties>, and cuts the value at the first
// expression left, so a placeholder never ends up in a repository path.
func interpolate(v string, p, leaf *pomXML) string {
	v = strings.TrimSpace(v)
	if !strings.Contains(v, "${") {
		return v
	}
	v = propRe.ReplaceAllStringFunc(v, func(m string) string {
		k := m[2 : len(m)-1]
		switch strings.TrimPrefix(k, "project.") {
		case "groupId":
			return first(leaf.GroupID, leaf.Parent.GroupID)
		case "artifactId":
			return leaf.ArtifactID
		case "version":
			return first(leaf.Version, leaf.Parent.Version)
		case "scm.tag":
			return strings.TrimSpace(p.SCM.Tag)
		}
		if val, ok := p.Properties[k]; ok && !strings.Contains(val, "${") {
			return val
		}
		return m
	})
	v, _, _ = strings.Cut(v, "${")
	return v
}

func first(a, b string) string {
	if a = strings.TrimSpace(a); a != "" {
		return a
	}
	return strings.TrimSpace(b)
}

// findPOM returns the POM of c from the first repository having it; nil when none has it or c
// is incomplete. Failures are returned only when no repository has the POM.
func (s *Source) findPOM(ctx context.Context, repos []repository, c coords) (*pomXML, error) {
	c = coords{strings.TrimSpace(c.GroupID), strings.TrimSpace(c.ArtifactID), strings.TrimSpace(c.Version)}
	if c.GroupID == "" || c.ArtifactID == "" || c.Version == "" || strings.Contains(c.GroupID+c.ArtifactID+c.Version, "${") {
		return nil, nil
	}
	p := strings.ReplaceAll(c.GroupID, ".", "/") + "/" + c.ArtifactID + "/" + c.Version + "/" + c.ArtifactID + "-" + c.Version + ".pom"
	var errs []error
	for _, r := range repos {
		e := s.pomEntry(ctx, r, p)
		switch {
		case e.err != nil:
			errs = append(errs, e.err)
		case !e.notFound:
			return e.pom, nil
		}
	}
	return nil, errors.Join(errs...)
}

func (s *Source) pomEntry(ctx context.Context, r repository, p string) pomEntry {
	e, err := s.poms.Do(ctx, reqKey{r.url, r.auth, p}, func() (pomEntry, error) {
		u := r.url + p
		pom, notFound, err := s.fetchPOM(ctx, u, r.auth)
		slog.DebugContext(ctx, "maven pom fetched", "url", u, "authenticated", r.auth != nil, "notFound", notFound, "err", err)
		return pomEntry{pom: pom, notFound: notFound}, err
	})
	e.err = err
	return e
}

func (s *Source) fetchPOM(ctx context.Context, u string, a *auth) (*pomXML, bool, error) {
	resp, err := s.do(ctx, http.MethodGet, u, a)
	if err != nil {
		return nil, false, err
	}
	defer closeBody(resp)
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return nil, true, nil
	case resp.StatusCode != http.StatusOK:
		return nil, false, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	p := &pomXML{}
	dec := xml.NewDecoder(io.LimitReader(resp.Body, maxPOM))
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := dec.Decode(p); err != nil {
		return nil, false, fmt.Errorf("GET %s: malformed POM: %w", u, err)
	}
	return p, false, nil
}
