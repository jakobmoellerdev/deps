package renovate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// maxPresetSize bounds a preset file fetched from Gitea, Forgejo or GitLab.
const maxPresetSize = 1 << 20

// gitLabCom is the forge of gitlab> presets, as in Renovate.
var gitLabCom = platform.Info{Kind: platform.GitLab, Host: "gitlab.com", ServerURL: "https://gitlab.com", APIURL: "https://gitlab.com/api/v4"}

// Fetcher fetches remote presets (github>, gitlab>, gitea>, forgejo>, local>).
type Fetcher struct {
	Platform func(ctx context.Context) (platform.Info, error) // the repository's forge; called at most once
	GitHub   *githubapi.Client
	Client   *http.Client
	Auth     *httpx.Auth

	once    sync.Once
	info    platform.Info
	infoErr error
}

// errPresetNotFound marks a missing preset file.
var errPresetNotFound = errors.New("not found")

func (f *Fetcher) platform(ctx context.Context) (platform.Info, error) {
	f.once.Do(func() {
		if f.Platform == nil {
			f.infoErr = errors.New("no platform detection")
			return
		}
		f.info, f.infoErr = f.Platform(ctx)
	})
	return f.info, f.infoErr
}

// fetch returns the preset ref names (Renovate fetchPreset and getPreset): the file is the
// first presetName segment (default.json, falling back to renovate.json, else <name>.json
// unless it has a .json, .json5 or .jsonc suffix) below presetPath, and further segments
// select keys inside it. label names the extends entry in errors.
func (f *Fetcher) fetch(ctx context.Context, ref presetRef, label string, n *notes) (map[string]any, error) {
	if f == nil {
		return nil, configErr("%s: remote presets are not available here", label)
	}
	segs := strings.Split(ref.name, "/")
	file := segs[0]
	switch {
	case file == "default":
		file = "default.json"
	case strings.HasSuffix(file, ".json"), strings.HasSuffix(file, ".json5"), strings.HasSuffix(file, ".jsonc"):
	default:
		file += ".json"
	}
	withPath := func(name string) string {
		if ref.path != "" {
			return ref.path + "/" + name
		}
		return name
	}
	src, err := f.read(ctx, ref, withPath(file), label)
	if errors.Is(err, errPresetNotFound) && segs[0] == "default" {
		if src, err = f.read(ctx, ref, withPath("renovate.json"), label); err == nil {
			n.info(label, "renovate.json as preset file is deprecated in Renovate")
		}
	}
	if errors.Is(err, errPresetNotFound) {
		return nil, configErr("%s: %s not found in %s", label, withPath(file), ref.repo)
	}
	if err != nil {
		return nil, err
	}
	v, err := parseJSON5(src)
	if err != nil {
		return nil, configErr("%s: %s: %w", label, withPath(file), err)
	}
	for _, key := range segs[1:] {
		obj, _ := v.(map[string]any)
		var ok bool
		if v, ok = obj[key]; !ok {
			return nil, configErr("%s: preset %s not found in %s", label, ref.name, withPath(file))
		}
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, configErr("%s: preset %s in %s is not an object", label, ref.name, withPath(file))
	}
	return obj, nil
}

// read returns file of ref's repository at ref.tag; a missing file is errPresetNotFound.
func (f *Fetcher) read(ctx context.Context, ref presetRef, file, label string) ([]byte, error) {
	switch ref.source {
	case "github":
		info, _ := f.platform(ctx) // github> means github.com unless the repository is on GHES
		if info.Kind != platform.GitHub {
			info = platform.Info{Kind: platform.GitHub, ServerURL: "https://github.com"}
		}
		return f.github(ctx, info, ref, file, label)
	case "gitea", "forgejo":
		info, err := f.platform(ctx)
		if err != nil || (info.Kind != platform.Gitea && info.Kind != platform.Forgejo) {
			return nil, configErr("%s: gitea>/forgejo> presets need a Gitea or Forgejo repository", label)
		}
		return f.gitea(ctx, info, ref, file, label)
	case "gitlab":
		return f.gitlab(ctx, gitLabCom, ref, file, label)
	}
	info, err := f.platform(ctx)
	if err == nil && info.Kind == "" {
		err = errors.New("unknown forge")
	}
	if err != nil {
		return nil, configErr("%s: cannot detect the repository's forge for local> presets: %v", label, err)
	}
	switch info.Kind {
	case platform.GitHub:
		return f.github(ctx, info, ref, file, label)
	case platform.GitLab:
		return f.gitlab(ctx, info, ref, file, label)
	}
	return f.gitea(ctx, info, ref, file, label)
}

func (f *Fetcher) github(ctx context.Context, info platform.Info, ref presetRef, file, label string) ([]byte, error) {
	u, err := url.Parse(info.ServerURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%s: invalid server URL %q", label, info.ServerURL)
	}
	owner, name, _ := strings.Cut(ref.repo, "/")
	b, err := f.GitHub.File(ctx, githubapi.Repo{Scheme: u.Scheme, Host: u.Host, Owner: owner, Name: name}, file, ref.tag)
	if errors.Is(err, githubapi.ErrNotFound) {
		return nil, errPresetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", label, file, err)
	}
	return b, nil
}

// gitea reads file through the Gitea/Forgejo raw file API.
func (f *Fetcher) gitea(ctx context.Context, info platform.Info, ref presetRef, file, label string) ([]byte, error) {
	segs := strings.Split(file, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	u := info.APIURL + "/repos/" + ref.repo + "/raw/" + strings.Join(segs, "/")
	if ref.tag != "" {
		u += "?ref=" + url.QueryEscape(ref.tag)
	}
	return f.get(ctx, info.Host, u, "token ", file, label)
}

// gitlab reads file through the GitLab repository files API, at ref.tag, else HEAD (the
// default branch).
func (f *Fetcher) gitlab(ctx context.Context, info platform.Info, ref presetRef, file, label string) ([]byte, error) {
	tag := ref.tag
	if tag == "" {
		tag = "HEAD"
	}
	u := info.APIURL + "/projects/" + url.PathEscape(ref.repo) + "/repository/files/" + url.PathEscape(file) + "/raw?ref=" + url.QueryEscape(tag)
	return f.get(ctx, info.Host, u, "Bearer ", file, label)
}

// get reads the raw preset file at u, sending the token of host after scheme in the
// Authorization header; a 404 is errPresetNotFound.
func (f *Fetcher) get(ctx context.Context, host, u, scheme, file, label string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	_, pass, ok := f.Auth.Static(host)
	if !ok {
		_, pass, ok = f.Auth.Helper(ctx, host)
	}
	if ok {
		req.Header.Set("Authorization", scheme+pass)
	}
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errPresetNotFound
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, fmt.Errorf("%s: %s: HTTP %d", label, file, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxPresetSize+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", label, file, err)
	}
	if len(b) > maxPresetSize {
		return nil, fmt.Errorf("%s: %s: larger than %d bytes", label, file, maxPresetSize)
	}
	return b, nil
}
