// Package pod lists the releases of CocoaPods pods: from the trunk CDN (cdn.cocoapods.org) and
// other spec repositories serving its layout, and from spec repositories on GitHub through the
// contents API. Lookups run once per shard file or (repository, pod) and are shared by every
// Podfile of a run.
package pod

import (
	"bufio"
	"cmp"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "pod"

// DefaultURL is the CocoaPods trunk CDN; DEPS_POD_URL overrides it (a mirror serving its
// layout).
const DefaultURL = "https://cdn.cocoapods.org"

// errNotFound marks a spec repository that does not have the pod.
var errNotFound = errors.New("not found")

// Source is the pod datasource.
type Source struct {
	getter httpx.Getter
	gh     *githubapi.Client
	base   string // the trunk CDN, without a trailing slash

	shards datasource.Memo[string, map[string][]datasource.Release] // by shard file URL: pod → releases
	repos  datasource.Memo[string, []datasource.Release]            // by GitHub repository + "\x00" + pod
}

// New returns the pod datasource. sem bounds concurrent CDN requests (nil = unbounded); gh
// reads spec repositories on GitHub; getenv supplies DEPS_POD_URL.
func New(client *http.Client, sem chan struct{}, gh *githubapi.Client, getenv func(string) string) *Source {
	return &Source{getter: httpx.Getter{Client: client, Sem: sem}, gh: gh, base: strings.TrimSuffix(cmp.Or(getenv("DEPS_POD_URL"), DefaultURL), "/")}
}

// Releases lists every version of the pod of d.Name (a subspec `Firebase/Analytics` is looked
// up as its pod `Firebase`). d.Source holds the spec repositories of the dep in lookup order,
// whitespace-separated ("" = trunk); the first one that has the pod answers, as CocoaPods
// resolves a pod from the first source listing it. The result is shared and must not be
// modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	name := podName(d.Name)
	srcs := strings.Fields(d.Source)
	if len(srcs) == 0 {
		srcs = []string{s.base}
	}
	for _, src := range srcs {
		rels, err := s.lookup(ctx, src, name)
		if !errors.Is(err, errNotFound) {
			return rels, err
		}
	}
	shown := make([]string, len(srcs))
	for i, src := range srcs {
		shown[i] = redact(src)
	}
	return nil, fmt.Errorf("pod %s not found in %s", name, strings.Join(shown, ", "))
}

// Digest is unsupported: pod versions are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// podName returns the pod of a dependency name: the part before the first "/" of a subspec.
func podName(name string) string {
	pod, _, _ := strings.Cut(name, "/")
	return pod
}

// shard returns the first three hex digits of the MD5 of the pod name, the directories
// CocoaPods shards its spec repositories by (Specs/d/a/2/Alamofire).
func shard(pod string) [3]string {
	sum := md5.Sum([]byte(pod))
	h := hex.EncodeToString(sum[:2])
	return [3]string{h[0:1], h[1:2], h[2:3]}
}

// lookup lists the versions of pod in the spec repository src: through the GitHub contents
// API for a repository on github.com (but trunk's CocoaPods/Specs, which is read from the
// CDN), else from the CDN layout at src.
func (s *Source) lookup(ctx context.Context, src, pod string) ([]datasource.Release, error) {
	if isTrunk(src) {
		src = s.base
	}
	if r, ok, err := githubRepo(src); ok || err != nil {
		if err != nil {
			return nil, err
		}
		return s.repos.Do(ctx, r.Scheme+"://"+r.Host+"/"+r.Path()+"\x00"+pod, func() ([]datasource.Release, error) {
			return s.github(ctx, r, pod)
		})
	}
	sh := shard(pod)
	u := strings.TrimSuffix(src, "/") + "/all_pods_versions_" + sh[0] + "_" + sh[1] + "_" + sh[2] + ".txt"
	pods, err := s.shards.Do(ctx, u, func() (map[string][]datasource.Release, error) { return s.cdn(ctx, u) })
	if err != nil {
		return nil, err
	}
	rels, ok := pods[pod]
	if !ok {
		return nil, errNotFound
	}
	return rels, nil
}

// cdn reads the shard file u of a CDN spec repository: one line per pod,
// `<pod>/<version>/<version>…`. A missing file lists no pods.
func (s *Source) cdn(ctx context.Context, u string) (map[string][]datasource.Release, error) {
	start := time.Now()
	pods := map[string][]datasource.Release{}
	err := s.getter.Stream(ctx, u, func(r io.Reader) error {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			fields := strings.Split(strings.TrimSpace(sc.Text()), "/")
			if len(fields) < 2 || fields[0] == "" {
				continue
			}
			rels := make([]datasource.Release, 0, len(fields)-1)
			for _, v := range fields[1:] {
				if v != "" {
					rels = append(rels, datasource.Release{Version: v})
				}
			}
			pods[fields[0]] = rels
		}
		return sc.Err()
	})
	slog.DebugContext(ctx, "pod shard fetched", "url", httpx.Redact(u), "pods", len(pods), "duration", time.Since(start), "err", err)
	if errors.Is(err, httpx.ErrNotFound) {
		return pods, nil
	}
	return pods, err
}

// github lists the version directories of pod in the spec repository r, trying the layouts
// CocoaPods reads, as Renovate does: sharded below Specs/, sharded at the top, then unsharded
// below Specs/ and at the top.
func (s *Source) github(ctx context.Context, r githubapi.Repo, pod string) ([]datasource.Release, error) {
	parts := shard(pod)
	sh := strings.Join(parts[:], "/")
	for _, p := range []string{"Specs/" + sh + "/" + pod, sh + "/" + pod, "Specs/" + pod, pod} {
		entries, err := s.gh.Dir(ctx, r, p, "")
		if errors.Is(err, githubapi.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("spec repository %s: %w", r.Path(), err)
		}
		rels := make([]datasource.Release, len(entries))
		for i, e := range entries {
			rels[i] = datasource.Release{Version: e.Name}
		}
		return rels, nil
	}
	return nil, errNotFound
}

// isTrunk reports whether src names the trunk spec repository: `trunk`, the CDN URL
// (`https://cdn.cocoapods.org/`) or its GitHub repository CocoaPods/Specs, which the CDN
// serves (and spares the GitHub API limits).
func isTrunk(src string) bool {
	if src == "trunk" || strings.EqualFold(strings.TrimSuffix(src, "/"), DefaultURL) {
		return true
	}
	r, ok, err := githubRepo(src)
	return ok && err == nil && strings.EqualFold(r.Owner, "CocoaPods") && strings.EqualFold(r.Name, "Specs")
}

// githubRepo returns the GitHub repository of the spec repository URL src: an http(s) URL on
// github.com or `git@github.com:owner/repo`. Other git URLs (ssh, scp-style, `.git` URLs off
// github.com) are an error: deps reads git spec repositories only from GitHub; ok is false for
// the http(s) URLs of CDN spec repositories.
func githubRepo(src string) (githubapi.Repo, bool, error) {
	u, err := url.Parse(src)
	switch {
	case strings.HasPrefix(src, "git@github.com:"):
		r, err := githubapi.ParseRepo(strings.TrimSuffix(strings.TrimPrefix(src, "git@github.com:"), ".git"))
		return r, true, err
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
		return githubapi.Repo{}, false, fmt.Errorf("unsupported spec repository %s: want an http(s) CDN or a GitHub repository", redact(src))
	case strings.EqualFold(u.Hostname(), "github.com"):
		r, err := githubapi.ParseRepo(src)
		return r, true, err
	case strings.HasSuffix(u.Path, ".git"):
		return githubapi.Repo{}, false, fmt.Errorf("unsupported spec repository %s: deps reads git spec repositories only on github.com", redact(src))
	}
	return githubapi.Repo{}, false, nil
}

// redact hides the password of a URL spec repository; scp-style ones (`git@host:path`) have
// none.
func redact(src string) string {
	if !strings.Contains(src, "://") {
		return src
	}
	return httpx.Redact(src)
}
