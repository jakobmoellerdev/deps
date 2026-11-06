// Package azuretasks lists the versions of Azure Pipelines tasks: by default from the public
// task lists Renovate maintains (built-in tasks, then the Visual Studio Marketplace), or from
// the task list of an Azure DevOps organization or collection.
package azuretasks

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
	"strconv"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "azure-pipelines-tasks"

// DefaultURL is the base of the public task lists (renovatebot/azure-devops-marketplace);
// DEPS_AZURE_PIPELINES_TASKS_URL overrides it (mirrors).
const DefaultURL = "https://raw.githubusercontent.com/renovatebot/azure-devops-marketplace/main"

// The public task lists below the base URL: objects mapping lower-case task names, ids and
// `<publisher>.<extension>.<contribution>.<name|id>` to their versions.
const (
	builtinFile     = "/azure-pipelines-builtin-tasks.json"
	marketplaceFile = "/azure-pipelines-marketplace-tasks.json"
)

// Source fetches each task list once per run. The marketplace list (several MB) is fetched
// only for tasks the built-in list lacks, and only the planned tasks are kept of it.
type Source struct {
	client *http.Client
	sem    chan struct{}
	base   string // public lists base, without trailing slash
	org    string // Azure DevOps organization/collection URL, without trailing slash; "" = public lists
	token  string // personal access token for org

	mu     sync.Mutex
	wanted map[string]bool // lower-case names of the planned tasks

	builtin, market, server list
}

// list is one loaded task list: lower-case task key → releases.
type list = datasource.Once[map[string][]datasource.Release]

// New returns the azure-pipelines-tasks datasource. sem bounds concurrent requests (nil =
// unbounded); getenv supplies DEPS_AZURE_PIPELINES_TASKS_URL, and DEPS_AZURE_DEVOPS_URL with
// DEPS_AZURE_DEVOPS_TOKEN to list the tasks of an organization instead.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	s := &Source{
		client: client, sem: sem,
		base:   strings.TrimSuffix(cmp.Or(getenv("DEPS_AZURE_PIPELINES_TASKS_URL"), DefaultURL), "/"),
		wanted: map[string]bool{},
	}
	if org, tok := getenv("DEPS_AZURE_DEVOPS_URL"), getenv("DEPS_AZURE_DEVOPS_TOKEN"); org != "" && tok != "" {
		s.org, s.token = strings.TrimSuffix(org, "/"), tok
	}
	return s
}

// Plan records the tasks the run asks for, so the marketplace list keeps only them
// (datasource.Planner).
func (s *Source) Plan(deps []dep.Dep) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range deps {
		s.wanted[strings.ToLower(d.Name)] = true
	}
}

// Releases lists the versions of task d.Name (a name, id or
// `<publisher>.<extension>.<contribution>.<name|id>`, case-insensitive) as published
// (`3.231.0`), then a derived "X.Y" and "X" per release line, so major-only references
// (`Bash@3`) have candidates of their precision under tag versioning.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	key := strings.ToLower(d.Name)
	if s.org != "" {
		tasks, err := s.load(ctx, &s.server, s.fetchServer)
		if err != nil {
			return nil, err
		}
		if rels, ok := tasks[key]; ok {
			return rels, nil
		}
		return nil, fmt.Errorf("task %s not found in %s", d.Name, s.org)
	}
	tasks, err := s.load(ctx, &s.builtin, func(ctx context.Context) (map[string][]datasource.Release, error) {
		return s.fetchList(ctx, builtinFile, nil)
	})
	if err != nil {
		return nil, err
	}
	if rels, ok := tasks[key]; ok {
		return rels, nil
	}
	tasks, err = s.load(ctx, &s.market, func(ctx context.Context) (map[string][]datasource.Release, error) {
		s.mu.Lock()
		s.wanted[key] = true
		want := make(map[string]bool, len(s.wanted))
		for k := range s.wanted {
			want[k] = true
		}
		s.mu.Unlock()
		return s.fetchList(ctx, marketplaceFile, want)
	})
	if err != nil {
		return nil, err
	}
	if rels, ok := tasks[key]; ok {
		return rels, nil
	}
	return nil, fmt.Errorf("task %s not found", d.Name)
}

// Digest is unsupported: tasks are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

func (s *Source) load(ctx context.Context, l *list, fetch func(context.Context) (map[string][]datasource.Release, error)) (map[string][]datasource.Release, error) {
	return l.Do(ctx, func() (map[string][]datasource.Release, error) {
		start := time.Now()
		tasks, err := fetch(ctx)
		slog.DebugContext(ctx, "azure pipelines tasks", "tasks", len(tasks), "duration", time.Since(start), "err", err)
		return tasks, err
	})
}

// fetchList reads a public task list, keeping the tasks in want (nil = all).
func (s *Source) fetchList(ctx context.Context, file string, want map[string]bool) (map[string][]datasource.Release, error) {
	tasks := map[string][]datasource.Release{}
	err := datasource.GetStream(ctx, s.client, s.sem, s.base+file, func(r io.Reader) error {
		dec := jsontext.NewDecoder(r)
		if tok, err := dec.ReadToken(); err != nil {
			return err
		} else if tok.Kind() != '{' {
			return errors.New("not an object")
		}
		for dec.PeekKind() != '}' {
			tok, err := dec.ReadToken()
			if err != nil {
				return err
			}
			key := strings.ToLower(tok.String())
			if want != nil && !want[key] {
				if err := dec.SkipValue(); err != nil {
					return err
				}
				continue
			}
			var versions []string
			if err := json.UnmarshalDecode(dec, &versions); err != nil {
				return err
			}
			tasks[key] = releases(versions)
		}
		return nil
	})
	return tasks, err
}

// serverTasks is the response of GET <org>/_apis/distributedtask/tasks.
type serverTasks struct {
	Value []struct {
		ID                     string `json:"id"`
		Name                   string `json:"name"`
		ContributionIdentifier string `json:"contributionIdentifier"`
		Version                *struct {
			Major int `json:"major"`
			Minor int `json:"minor"`
			Patch int `json:"patch"`
		} `json:"version"`
	} `json:"value"`
}

// fetchServer reads the tasks installed in the organization; each version is listed under the
// task's name and id, and with a contributionIdentifier (extension tasks) also under
// `<contributionIdentifier>.<name|id>`, as Renovate matches them.
func (s *Source) fetchServer(ctx context.Context) (map[string][]datasource.Release, error) {
	release, err := httpx.Acquire(ctx, s.sem)
	if err != nil {
		return nil, err
	}
	defer release()
	target := s.org + "/_apis/distributedtask/tasks/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	req.SetBasicAuth("deps", s.token)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", target, resp.StatusCode)
	}
	var body serverTasks
	if err := json.UnmarshalRead(resp.Body, &body); err != nil {
		return nil, fmt.Errorf("GET %s: malformed response: %w", target, err)
	}
	versions := map[string][]string{}
	for _, t := range body.Value {
		if t.Version == nil {
			continue
		}
		v := strconv.Itoa(t.Version.Major) + "." + strconv.Itoa(t.Version.Minor) + "." + strconv.Itoa(t.Version.Patch)
		keys := []string{t.ID, t.Name}
		if t.ContributionIdentifier != "" {
			keys = append(keys, t.ContributionIdentifier+"."+t.ID, t.ContributionIdentifier+"."+t.Name)
		}
		for _, k := range keys {
			if k != "" {
				k = strings.ToLower(k)
				versions[k] = append(versions[k], v)
			}
		}
	}
	tasks := make(map[string][]datasource.Release, len(versions))
	for k, vs := range versions {
		tasks[k] = releases(vs)
	}
	return tasks, nil
}

// releases returns versions, then the derived "X.Y" and "X" of every full version once.
func releases(versions []string) []datasource.Release {
	rels := make([]datasource.Release, 0, len(versions)+4)
	seen := map[string]bool{}
	var derived []datasource.Release
	for _, v := range versions {
		if seen[v] {
			continue
		}
		seen[v] = true
		rels = append(rels, datasource.Release{Version: v})
		if strings.Count(v, ".") != 2 || strings.ContainsAny(v, "-+") {
			continue
		}
		for _, line := range []string{v[:strings.LastIndexByte(v, '.')], v[:strings.IndexByte(v, '.')]} {
			if !seen[line] {
				seen[line] = true
				derived = append(derived, datasource.Release{Version: line})
			}
		}
	}
	return append(rels, derived...)
}
