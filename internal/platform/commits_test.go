package platform

import (
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeGitData is an in-memory GitHub repository o/r behind the git data API (blobs, trees,
// commits, refs), the commits API and GraphQL viewer/updateRefs. Commits it creates are
// authored by the bot account 7; seeded commits by an email GitHub does not link.
type fakeGitData struct {
	t       *testing.T
	mu      sync.Mutex
	n       int
	blobs   map[string][]byte
	trees   map[string]map[string]fakeEntry // path -> entry
	commits map[string]fakeCommit
	refs    map[string]string // refs/heads/... -> commit
}

type fakeEntry struct{ mode, blob string }

type fakeCommit struct {
	tree, msg, email string
	account          int64 // linked GitHub account; 0 = none
	parents          []string
}

const botEmail = "7+app[bot]@users.noreply.github.com"

func newFakeGitData(t *testing.T) (*fakeGitData, *httptest.Server) {
	f := &fakeGitData{t: t, blobs: map[string][]byte{}, trees: map[string]map[string]fakeEntry{}, commits: map[string]fakeCommit{}, refs: map[string]string{}}
	f.blobs["readme"], f.blobs["old"] = []byte("readme\n"), []byte("old\n") // ids the fake never generates
	f.trees["t0"] = map[string]fakeEntry{"README": {"100644", "readme"}, "sub/a.txt": {"100644", "readme"}, "sub/old.txt": {"100644", "old"}}
	f.commits["c0"] = fakeCommit{tree: "t0", msg: "init", email: "human@example.com"}
	f.refs["refs/heads/main"] = "c0"
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeGitData) id(kind string) string {
	f.n++
	return fmt.Sprintf("%s%d", kind, f.n)
}

func (f *fakeGitData) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var in map[string]any
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		if err := json.Unmarshal(b, &in); err != nil {
			f.t.Errorf("%s %s: %v", r.Method, r.URL.Path, err)
		}
	}
	reply := func(v any) { _ = json.MarshalWrite(w, v) }
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && p == "/repos/o/r":
		reply(map[string]string{"node_id": "R_1"})
	case r.Method == http.MethodGet && p == "/user":
		reply(map[string]any{"login": "jdoe", "id": 5, "name": "J. Doe"})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/repos/o/r/git/commits/"):
		c, ok := f.commits[strings.TrimPrefix(p, "/repos/o/r/git/commits/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		reply(map[string]any{"tree": map[string]string{"sha": c.tree}})
	case r.Method == http.MethodPost && p == "/repos/o/r/git/blobs":
		b, err := base64.StdEncoding.DecodeString(in["content"].(string))
		if err != nil || in["encoding"] != "base64" {
			f.t.Errorf("blob %v: %v", in, err)
		}
		id := f.id("b")
		f.blobs[id] = b
		reply(map[string]string{"sha": id})
	case r.Method == http.MethodPost && p == "/repos/o/r/git/trees":
		base, ok := f.trees[in["base_tree"].(string)]
		if !ok {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		tree := maps.Clone(base)
		for _, e := range in["tree"].([]any) {
			e := e.(map[string]any)
			path := e["path"].(string)
			if e["sha"] == nil {
				delete(tree, path)
				continue
			}
			tree[path] = fakeEntry{e["mode"].(string), e["sha"].(string)}
		}
		id := f.id("t")
		f.trees[id] = tree
		reply(map[string]string{"sha": id})
	case r.Method == http.MethodPost && p == "/repos/o/r/git/commits":
		var parents []string
		for _, p := range in["parents"].([]any) {
			parents = append(parents, p.(string))
		}
		id := f.id("c")
		f.commits[id] = fakeCommit{tree: in["tree"].(string), msg: in["message"].(string), email: botEmail, account: 7, parents: parents}
		reply(map[string]string{"sha": id})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/repos/o/r/commits/refs/heads/"):
		sha, ok := f.refs[strings.TrimPrefix(p, "/repos/o/r/commits/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		c := f.commits[sha]
		var account any
		if c.account != 0 {
			account = map[string]any{"id": c.account}
		}
		parents := []map[string]string{}
		for _, p := range c.parents {
			parents = append(parents, map[string]string{"sha": p})
		}
		reply(map[string]any{"sha": sha, "commit": map[string]any{"author": map[string]string{"email": c.email}, "tree": map[string]string{"sha": c.tree}}, "author": account, "parents": parents})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/repos/o/r/git/ref/heads/"):
		sha, ok := f.refs["refs/"+strings.TrimPrefix(p, "/repos/o/r/git/ref/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		reply(map[string]any{"object": map[string]string{"sha": sha}})
	case r.Method == http.MethodPost && p == "/graphql":
		q, vars := in["query"].(string), in["variables"].(map[string]any)
		switch {
		case strings.Contains(q, "viewer"):
			reply(map[string]any{"data": map[string]any{"viewer": map[string]any{"login": "app[bot]", "databaseId": 7}}})
		case strings.Contains(q, "updateRefs") && strings.Contains(q, "force: true"):
			ref, before, after := vars["ref"].(string), vars["before"].(string), vars["after"].(string)
			cur, exists := f.refs[ref]
			// refs/heads/protected is behind branch protection: never updated, though it exists.
			if _, ok := f.commits[after]; vars["repo"] != "R_1" || !ok || (exists && cur != before) || (!exists && before != zeroOID) || ref == "refs/heads/protected" {
				reply(map[string]any{"errors": []map[string]string{{"message": "Something went wrong while executing your query."}}})
				return
			}
			f.refs[ref] = after
			reply(map[string]any{"data": map[string]any{"updateRefs": map[string]any{"clientMutationId": nil}}})
		default:
			f.t.Errorf("unexpected query %q", q)
		}
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
}

// files returns the paths of commit's tree with mode and content.
func (f *fakeGitData) files(commit string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for p, e := range f.trees[f.commits[commit].tree] {
		out[p] = e.mode + " " + string(f.blobs[e.blob])
	}
	return out
}

func (f *fakeGitData) commit(id string) fakeCommit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commits[id]
}

func (f *fakeGitData) ref(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refs[name]
}

func TestGitHubCommitsAuthor(t *testing.T) {
	_, srv := newFakeGitData(t)
	for _, tc := range []struct {
		token, host, wantName, wantEmail string
	}{
		{"ghs_app", "github.com", "app[bot]", "7+app[bot]@users.noreply.github.com"},
		{"ghp_user", "github.com", "J. Doe", "5+jdoe@users.noreply.github.com"},
		{"ghs_app", "ghe.example", "app[bot]", "7+app[bot]@users.noreply.ghe.example"},
		{"ghs_app", "octo.ghe.com", "app[bot]", "7+app[bot]@users.noreply.ghe.com"},
	} {
		g, err := NewGitHubCommits(t.Context(), Info{Kind: GitHub, APIURL: srv.URL, Repo: "o/r", Host: tc.host}, tc.token, srv.Client(), "")
		if err != nil {
			t.Fatalf("%s: %v", tc.token, err)
		}
		if name, email := g.Author(); name != tc.wantName || email != tc.wantEmail {
			t.Errorf("%s on %s: Author = %q, %q; want %q, %q", tc.token, tc.host, name, email, tc.wantName, tc.wantEmail)
		}
	}
}

func TestGitHubCommits(t *testing.T) {
	f, srv := newFakeGitData(t)
	ctx := t.Context()
	// deps runs in sub/: change paths are relative to it.
	g, err := NewGitHubCommits(ctx, Info{Kind: GitHub, APIURL: srv.URL, Repo: "o/r", Host: "github.com"}, "ghs_app", srv.Client(), "sub/")
	if err != nil {
		t.Fatal(err)
	}
	const branch = "deps/npm/@types-node"

	// New branch: modify, add an executable and a binary file, delete a file.
	c1, err := g.Commit(ctx, "c0", []CommitFile{
		{Path: "a.txt", Content: []byte("changed\n")},
		{Path: "run.sh", Executable: true, Content: []byte("#!/bin/sh\n")},
		{Path: "bin", Content: []byte{0xff, 0x00, 0xfe}},
		{Path: "old.txt"},
	}, "chore(deps): update x\n\nbody\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.ref("refs/heads/" + branch); got != "" {
		t.Fatalf("Commit moved the branch to %s", got)
	}
	if err := g.Push(ctx, c1, branch, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.ref("refs/heads/" + branch); got != c1 {
		t.Fatalf("branch = %q, want %s", got, c1)
	}
	want := map[string]string{
		"README":     "100644 readme\n",
		"sub/a.txt":  "100644 changed\n",
		"sub/run.sh": "100755 #!/bin/sh\n",
		"sub/bin":    "100644 \xff\x00\xfe",
	}
	if got := f.files(c1); !maps.Equal(got, want) {
		t.Errorf("tree = %q, want %q", got, want)
	}
	if c := f.commit(c1); len(c.parents) != 1 || c.parents[0] != "c0" || c.msg != "chore(deps): update x\n\nbody\n" {
		t.Errorf("commit = %+v", c)
	}

	// The tip GitHub links to the token's account is deps' own, whatever its email.
	tip, err := g.BranchTip(ctx, branch)
	if _, email := g.Author(); err != nil || tip.Author != email || len(tip.Parents) != 1 || tip.Parents[0] != "c0" || tip.Tree != f.commit(c1).tree {
		t.Errorf("BranchTip = %+v, %v", tip, err)
	}
	// A tip without a linked account reports its email.
	if tip, err := g.BranchTip(ctx, "main"); err != nil || tip.Author != "human@example.com" || tip.Tree != "t0" {
		t.Errorf("BranchTip(main) = %+v, %v", tip, err)
	}

	// Creating a branch that exists is a compare-and-swap conflict.
	if err := g.Push(ctx, c1, branch, ""); !errors.Is(err, ErrRefStale) || !strings.Contains(err.Error(), "update refs/heads/"+branch+": stale info: the branch is at "+c1+", expected no branch") {
		t.Errorf("create existing branch: err = %v", err)
	}

	// Existing branch: rebuilt on c0 and moved from c1.
	c2, err := g.Commit(ctx, "c0", []CommitFile{{Path: "a.txt", Content: []byte("v2\n")}}, "chore(deps): update x to v2")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Push(ctx, c2, branch, c1); err != nil {
		t.Fatal(err)
	}
	if got := f.ref("refs/heads/" + branch); got != c2 {
		t.Fatalf("branch = %q, want %s", got, c2)
	}
	if got := f.files(c2)["sub/old.txt"]; got != "100644 old\n" {
		t.Errorf("rebuilt commit lost the untouched file: %q", got)
	}

	// The branch moved since c1 was listed: the push is refused and the branch stays.
	c3, err := g.Commit(ctx, "c0", []CommitFile{{Path: "a.txt", Content: []byte("v3\n")}}, "v3")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Push(ctx, c3, branch, c1); !errors.Is(err, ErrRefStale) || !strings.Contains(err.Error(), "stale info: the branch is at "+c2+", expected "+c1) {
		t.Errorf("stale push: err = %v", err)
	}
	if got := f.ref("refs/heads/" + branch); got != c2 {
		t.Errorf("stale push moved the branch to %s", got)
	}
	// The expected branch was deleted meanwhile.
	if err := g.Push(ctx, c3, "deps/gone", c1); !errors.Is(err, ErrRefStale) || !strings.Contains(err.Error(), "stale info: the branch was deleted, expected "+c1) {
		t.Errorf("push to a deleted branch: err = %v", err)
	}
	// A branch GitHub refuses to move although it is at the expected commit.
	f.mu.Lock()
	f.refs["refs/heads/protected"] = "c0"
	f.mu.Unlock()
	if err := g.Push(ctx, c3, "protected", "c0"); !errors.Is(err, ErrRefRejected) || errors.Is(err, ErrRefStale) {
		t.Errorf("push to a protected branch: err = %v", err)
	}

	// An unknown parent fails before anything is created.
	if _, err := g.Commit(ctx, "nope", []CommitFile{{Path: "a.txt", Content: []byte("x")}}, "m"); err == nil || !strings.Contains(err.Error(), "GET /repos/o/r/git/commits/nope: 404") {
		t.Errorf("unknown parent: err = %v", err)
	}
}
