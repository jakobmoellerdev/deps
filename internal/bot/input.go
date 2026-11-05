package bot

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"io"
	"log/slog"
	"os"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

var inputRe = regexp.MustCompile(`<!-- deps:input=([0-9a-f]+) -->`)

// buildInput is what engine.Build reads of an update; the rest (schedule, labels, automerge,
// advisories) only shapes the PR around the content.
type buildInput struct {
	Dep         dep.Dep
	Type        dep.UpdateType
	NewVersion  string
	NewDigest   string
	NewName     string
	NewChecksum string
	Lockfile    bool
}

// buildID identifies the deps binary, since another one may build other content: its module
// version and VCS revision, else (a build with local modifications or without VCS stamp) the
// sha256 of the executable, else a random value, so that no fingerprint matches.
var buildID = sync.OnceValue(func() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		var rev, modified string
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				modified = s.Value
			}
		}
		if rev != "" && modified != "true" {
			return bi.Main.Version + " " + rev
		}
	}
	if exe, err := os.Executable(); err == nil {
		if f, err := os.Open(exe); err == nil {
			defer f.Close()
			h := sha256.New()
			if _, err := io.Copy(h, f); err == nil {
				return hex.EncodeToString(h.Sum(nil))
			}
		}
	}
	return rand.Text()
})

// inputFingerprints returns the input fingerprint of each group with want set: the first 16
// hex characters of sha256 over the deps build (buildID), the group's updates as Build reads
// them (buildInput, in a canonical order) and the object ids in commit head of the paths
// Build reads (engine.Inputs: the updated files, the post-processing scope directories and
// what their post-processors read outside them). Equal fingerprints mean Build would get the
// same input, so an open PR built from it is current without building. The freshness check
// guarantees head is the base tip and the checkout, which Build reads, is head. One git call
// resolves the paths of every group.
//
// A group gets "" (always built) when it has a lock file maintenance update, whose result
// follows the registries rather than the repository, or when an input path is outside the
// root or not in head (an untracked file); every group gets "" when git fails.
func inputFingerprints(ctx context.Context, o Options, root, head string, groups []engine.Group, want []bool, post map[string]engine.PostProcessor, g Git) []string {
	fps := make([]string, len(groups))
	paths := make([][]string, len(groups))
	index := map[string]int{}
	var all []string
	for i, grp := range groups {
		if !want[i] || slices.ContainsFunc(grp.Updates, dep.Update.Maintenance) {
			continue
		}
		p, err := engine.Inputs(root, o.Managers, grp.Updates, post)
		if err != nil {
			slog.DebugContext(ctx, "no input fingerprint", "branch", grp.Branch, "err", err)
			continue
		}
		paths[i] = p
		for _, q := range p {
			if _, ok := index[q]; !ok {
				index[q] = len(all)
				all = append(all, q)
			}
		}
	}
	if len(all) == 0 {
		return fps
	}
	ids, err := g.ObjectIDs(ctx, head, all)
	if err != nil {
		slog.WarnContext(ctx, "no input fingerprints, building every group", "err", err)
		return fps
	}
	id := buildID()
	for i, grp := range groups {
		if paths[i] == nil {
			continue
		}
		fps[i] = inputFingerprint(id, grp.Updates, paths[i], func(p string) string { return ids[index[p]] })
		if fps[i] == "" {
			slog.DebugContext(ctx, "no input fingerprint: an input is not in the base commit", "branch", grp.Branch)
		}
	}
	return fps
}

// inputFingerprint hashes the inputs of one group's build: build is the deps build, paths
// the sorted input paths and oid their object ids. It is "" when a path has no object.
func inputFingerprint(build string, ups []dep.Update, paths []string, oid func(string) string) string {
	encoded := make([]string, len(ups))
	for i, u := range ups {
		b, err := json.Marshal(buildInput{u.Dep, u.Type, u.NewVersion, u.NewDigest, u.NewName, u.NewChecksum, u.Lockfile})
		if err != nil {
			return ""
		}
		encoded[i] = string(b)
	}
	slices.Sort(encoded)
	h := sha256.New()
	h.Write([]byte("deps input v1\x00" + build + "\x00"))
	for _, e := range encoded {
		h.Write([]byte(e))
		h.Write([]byte{0})
	}
	for _, p := range paths {
		id := oid(p)
		if id == "" {
			return ""
		}
		h.Write([]byte(p + "\x00" + id + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// reusable reports whether the open PR pr can be judged without building its group: its
// body records the content fingerprint and input, the group's input fingerprint, and nothing
// already known requires a rebuild (a dashboard rebase request, a PR the forge reports as
// unmergeable unless rebaseWhen is never). A rebuild found necessary later (a conflict
// reported by GetPR, a branch behind base under behind-base-branch) builds in handle.
func reusable(o Options, pr platform.PR, input string, f force) bool {
	conflicted := pr.Mergeable != nil && !*pr.Mergeable && o.RebaseWhen != config.RebaseNever
	return input != "" && !f.rebase && !conflicted && bodyFingerprint(pr.Body) != "" && bodyInput(pr.Body) == input
}

func bodyInput(body string) string {
	if m := inputRe.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

// withInput returns body with its input fingerprint marker set to input: replaced, else
// added after the content fingerprint marker, else appended.
func withInput(body, input string) string {
	marker := "<!-- deps:input=" + input + " -->"
	if loc := inputRe.FindStringIndex(body); loc != nil {
		return body[:loc[0]] + marker + body[loc[1]:]
	}
	if loc := fingerprintRe.FindStringIndex(body); loc != nil {
		return body[:loc[1]] + "\n" + marker + body[loc[1]:]
	}
	return strings.TrimRight(body, "\n") + "\n\n" + marker + "\n"
}

// recordInput records input, the group's input fingerprint, in the body of the open PR pr
// whose content is current, when the body records another one or none (a PR from before
// input fingerprints, or base changed an input without changing the content). Only the marker
// changes; nothing is pushed. Runs on the same inputs then skip the group's build. Dry runs
// write nothing, and a failed write only warns: the next run builds again.
func recordInput(ctx context.Context, o Options, pc platform.Client, grp engine.Group, pr *platform.PR, input string) {
	if o.DryRun || input == "" || bodyInput(pr.Body) == input {
		return
	}
	if err := pc.UpdatePR(ctx, pr.Number, cmp.Or(pr.Title, grp.Title), withInput(pr.Body, input)); err != nil {
		slog.WarnContext(ctx, "recording the input fingerprint", "branch", grp.Branch, "number", pr.Number, "err", err)
		return
	}
	slog.DebugContext(ctx, "input fingerprint recorded", "branch", grp.Branch, "number", pr.Number, "input", input)
}
