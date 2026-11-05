package bot

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// countPost is a post-processor for the github-actions fixture that scopes every file to the
// root, counts its runs and changes nothing: the go mod tidy of the fixture.
type countPost struct{ runs *atomic.Int32 }

func (countPost) Scope(string, string) (string, bool) { return ".", true }
func (countPost) Siblings() []string                  { return nil }
func (p countPost) Process(context.Context, string, map[string][]byte, map[string][]byte) (map[string][]byte, error) {
	p.runs.Add(1)
	return nil, nil
}

// inputOf returns the input fingerprint the bot computes for group name with git g.
func inputOf(t *testing.T, root string, res *engine.Result, name string, post map[string]engine.PostProcessor, g Git) string {
	t.Helper()
	for _, grp := range engine.GroupUpdates(res.Updates, "") {
		if grp.Name == name {
			return inputFingerprints(t.Context(), Options{Managers: managers}, root, head, []engine.Group{grp}, []bool{true}, post, g)[0]
		}
	}
	t.Fatalf("no group %s", name)
	return ""
}

// TestRunInputFingerprint runs the bot on one group, a/x, whose open PR records the content
// and input fingerprints of the base (body "FP IN"); runs counts the post-processor, i.e.
// engine.Build.
func TestRunInputFingerprint(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name     string
		opts     Options
		git      fakeGit
		pr       platform.PR // Head branchA, Number 1; Body "FP IN" unless set
		pc       fakePC      // open is set to pr
		dashRe   bool        // the dashboard requests a rebase of branchA
		update   func(*dep.Update)
		noBuild  bool // the PR's input fingerprint is computed with the base unchanged
		want     string
		wantRuns int32
		// pushes, full PR updates (body regenerated), UpdatePR calls that only record the
		// input fingerprint, GetPR calls, EnableAutoMerge calls
		wantPushed, wantUpdated, wantRecorded, wantGetPR, wantAutomerge int
	}{
		{name: "equal input skips the build", pr: platform.PR{Mergeable: &yes}, want: StatusUpToDate},
		{name: "dry run skips the build too", opts: Options{DryRun: true}, pr: platform.PR{Mergeable: &yes}, want: StatusUpToDate},
		{name: "unknown mergeability looked up without building", pc: fakePC{mergeable: &yes}, want: StatusUpToDate, wantGetPR: 1},
		{
			name: "changed update rebuilds and updates", pr: platform.PR{Mergeable: &yes},
			update: func(u *dep.Update) { u.NewVersion = "v3" },
			want:   StatusUpdated, wantRuns: 1, wantPushed: 1, wantUpdated: 1,
		},
		{
			name: "changed scope tree rebuilds; same content records the new input",
			git:  fakeGit{objects: map[string]string{".": "moved"}}, pr: platform.PR{Mergeable: &yes},
			want: StatusUpToDate, wantRuns: 1, wantRecorded: 1,
		},
		{
			name: "changed file rebuilds", git: fakeGit{objects: map[string]string{"wf.yml": "moved"}}, pr: platform.PR{Mergeable: &yes},
			want: StatusUpToDate, wantRuns: 1, wantRecorded: 1,
		},
		{
			name: "input not in base always builds", git: fakeGit{objects: map[string]string{"wf.yml": ""}}, pr: platform.PR{Mergeable: &yes},
			want: StatusUpToDate, wantRuns: 1,
		},
		{
			name: "git failure builds", git: fakeGit{fail: map[string]error{"ObjectIDs": errBoom}}, pr: platform.PR{Mergeable: &yes},
			want: StatusUpToDate, wantRuns: 1,
		},
		{
			name: "missing marker rebuilds and records it", pr: platform.PR{Mergeable: &yes, Body: "FP"},
			want: StatusUpToDate, wantRuns: 1, wantRecorded: 1,
		},
		{
			name: "missing marker in a dry run records nothing", opts: Options{DryRun: true}, pr: platform.PR{Mergeable: &yes, Body: "FP"},
			want: StatusUpToDate, wantRuns: 1,
		},
		{name: "dashboard rebase request rebuilds", dashRe: true, pr: platform.PR{Mergeable: &yes}, want: StatusRebased, wantRuns: 1, wantPushed: 1},
		{name: "unmergeable PR rebuilds", pr: platform.PR{Mergeable: &no}, want: StatusUpdated, wantRuns: 1, wantPushed: 1, wantUpdated: 1},
		{
			name: "conflict reported by GetPR builds then", pc: fakePC{mergeable: &no},
			want: StatusUpdated, wantRuns: 1, wantPushed: 1, wantUpdated: 1, wantGetPR: 1,
		},
		{name: "never mode keeps a conflicted PR without building", opts: Options{RebaseWhen: config.RebaseNever}, pr: platform.PR{Mergeable: &no}, want: StatusUpToDate},
		{
			name: "behind base builds and rebases", opts: Options{RebaseWhen: config.RebaseBehindBase},
			git: fakeGit{parents: map[string][]string{branchA: {"old"}}}, pr: platform.PR{Mergeable: &yes},
			want: StatusRebased, wantRuns: 1, wantPushed: 1,
		},
		{
			name: "behind base in a dry run builds too", opts: Options{RebaseWhen: config.RebaseBehindBase, DryRun: true},
			git: fakeGit{parents: map[string][]string{branchA: {"old"}}}, pr: platform.PR{Mergeable: &yes},
			want: StatusWouldRebase, wantRuns: 1,
		},
		{name: "on base under behind-base-branch skips the build", opts: Options{RebaseWhen: config.RebaseBehindBase}, pr: platform.PR{Mergeable: &yes}, want: StatusUpToDate},
		{
			name: "automerge of a skipped PR", pr: platform.PR{Mergeable: &yes},
			update: func(u *dep.Update) { u.Automerge, u.AutomergeStrategy = true, "squash" },
			// The automerge policy is not a build input.
			noBuild: true, want: StatusUpToDate, wantAutomerge: 1,
		},
		{
			name: "labels and commit type are not build inputs", pr: platform.PR{Mergeable: &yes},
			update:  func(u *dep.Update) { u.Labels, u.Reviewers, u.CommitType = []string{"deps"}, []string{"me"}, "build" },
			noBuild: true, want: StatusUpToDate,
		},
		{
			name: "PR built by another deps build rebuilds", pr: platform.PR{Mergeable: &yes, Body: "FP OTHER"},
			want: StatusUpToDate, wantRuns: 1, wantRecorded: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, res := fixture(t)
			res.Updates = res.Updates[:1] // a/x only
			runs := &atomic.Int32{}
			post := map[string]engine.PostProcessor{"github-actions": countPost{runs}}
			fp := fingerprintOf(t, root, res, "a/x") // countPost changes nothing
			in := inputOf(t, root, res, "a/x", post, &fakeGit{})
			if in == "" {
				t.Fatal("no input fingerprint")
			}
			body := tt.pr.Body
			if body == "" {
				body = "FP IN"
			}
			body = strings.NewReplacer(
				"FP", "x\n<!-- deps:fingerprint="+fp+" -->",
				" IN", "\n<!-- deps:input="+in+" -->",
				" OTHER", "\n<!-- deps:input=0000000000000000 -->",
			).Replace(body) + "\n"
			if tt.update != nil {
				tt.update(&res.Updates[0])
				if tt.noBuild {
					if got := inputOf(t, root, res, "a/x", post, &fakeGit{}); got != in {
						t.Fatalf("input fingerprint changed to %s, want %s", got, in)
					}
				}
			}
			pr := tt.pr
			pr.Number, pr.Head, pr.Title, pr.Body = 1, branchA, titleA, body
			pc := tt.pc
			pc.open = []platform.PR{pr}
			o := tt.opts
			if tt.dashRe {
				o.Dashboard = &DashboardOptions{Title: dashTitle}
				pc.issues = []platform.Issue{{Number: 7, Title: dashTitle, Body: dashboardMarker + "\n- [x] <!-- deps:rebase=" + branchA + " --> " + titleA + "\n"}}
			}
			o.AuthorEmail, o.Managers = email, managers
			g := tt.git
			g.branches = map[string]string{"refs/heads/" + branchA: "x"}
			g.authors = map[string]string{branchA: email}

			acts, errs := Run(t.Context(), o, root, res, post, &pc, &g)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			var got []string
			for _, a := range acts {
				if !a.Dashboard {
					got = append(got, a.Branch+" "+a.Status)
				}
			}
			if want := []string{branchA + " " + tt.want}; !slices.Equal(got, want) {
				t.Errorf("actions = %q, want %q", got, want)
			}
			if n := runs.Load(); n != tt.wantRuns {
				t.Errorf("post-processor runs = %d, want %d", n, tt.wantRuns)
			}
			if g.oidCalls != 1 {
				t.Errorf("ObjectIDs calls = %d, want 1 per run", g.oidCalls)
			}
			if len(g.pushed) != tt.wantPushed || len(pc.updated) != tt.wantUpdated || len(pc.recorded) != tt.wantRecorded || len(pc.got) != tt.wantGetPR || len(pc.automerge) != tt.wantAutomerge {
				t.Errorf("pushed=%v updated=%v recorded=%v GetPR=%v automerge=%v", g.pushed, pc.updated, pc.recorded, pc.got, pc.automerge)
			}
		})
	}
}

// TestRunRecordsInputInNewPRs checks that created and updated PRs carry the input fingerprint
// the next run compares.
func TestRunRecordsInputInNewPRs(t *testing.T) {
	root, res := fixture(t)
	g := &fakeGit{}
	pc := &fakePC{}
	if _, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, root, res, nil, pc, g); len(errs) > 0 {
		t.Fatal(errs)
	}
	in := inputOf(t, root, res, "a/x", nil, &fakeGit{})
	if len(pc.bodies) != 2 || bodyInput(pc.bodies[0]) != in || bodyInput(pc.bodies[1]) == "" || bodyInput(pc.bodies[1]) == in {
		t.Errorf("bodies %q, want the input fingerprints of a/x (%s) and b/y", pc.bodies, in)
	}
	if bodyFingerprint(pc.bodies[0]) != fingerprintOf(t, root, res, "a/x") {
		t.Errorf("body %q lacks the content fingerprint", pc.bodies[0])
	}
}

func TestInputFingerprint(t *testing.T) {
	a := dep.Update{Dep: dep.Dep{Manager: "npm", File: "package.json", Name: "a", Version: "1.0.0"}, NewVersion: "1.1.0"}
	b := dep.Update{Dep: dep.Dep{Manager: "npm", File: "package.json", Name: "b", Version: "2.0.0"}, NewVersion: "2.1.0"}
	paths := []string{".", "package.json"}
	oids := func(p string) string { return "oid " + p }
	base := inputFingerprint("v1", []dep.Update{a, b}, paths, oids)
	if len(base) != 16 {
		t.Fatalf("fingerprint %q", base)
	}
	if got := inputFingerprint("v1", []dep.Update{b, a}, paths, oids); got != base {
		t.Error("update order changes the fingerprint")
	}
	lock := a
	lock.Lockfile = true
	digest := a
	digest.NewDigest = "sha256:abc"
	for name, got := range map[string]string{
		"build":    inputFingerprint("v2", []dep.Update{a, b}, paths, oids),
		"version":  inputFingerprint("v1", []dep.Update{a}, paths, oids),
		"lockfile": inputFingerprint("v1", []dep.Update{lock, b}, paths, oids),
		"digest":   inputFingerprint("v1", []dep.Update{digest, b}, paths, oids),
		"tree":     inputFingerprint("v1", []dep.Update{a, b}, paths, func(p string) string { return "x" + oids(p) }),
		"paths":    inputFingerprint("v1", []dep.Update{a, b}, paths[1:], oids),
	} {
		if got == base {
			t.Errorf("%s does not change the fingerprint", name)
		}
	}
	if got := inputFingerprint("v1", []dep.Update{a}, paths, func(string) string { return "" }); got != "" {
		t.Errorf("missing object: %q, want none", got)
	}
}

func TestInputFingerprintsSkipLockFileMaintenance(t *testing.T) {
	root, res := fixture(t)
	m := dep.Update{Dep: dep.Dep{Manager: "github-actions", File: "wf.yml", Name: "wf.yml"}, Type: dep.LockFileMaintenance}
	groups := engine.GroupUpdates(append(res.Updates, m), "")
	g := &fakeGit{}
	fps := inputFingerprints(t.Context(), Options{Managers: managers}, root, head, groups, slices.Repeat([]bool{true}, len(groups)), nil, g)
	for i, grp := range groups {
		if maint := slices.ContainsFunc(grp.Updates, dep.Update.Maintenance); maint != (fps[i] == "") {
			t.Errorf("%s: input fingerprint %q", grp.Branch, fps[i])
		}
	}
	if g.oidCalls != 1 {
		t.Errorf("ObjectIDs calls = %d, want 1", g.oidCalls)
	}
	none := inputFingerprints(t.Context(), Options{Managers: managers}, root, head, groups, make([]bool, len(groups)), nil, g)
	if slices.ContainsFunc(none, func(s string) bool { return s != "" }) || g.oidCalls != 1 {
		t.Errorf("unwanted groups fingerprinted: %q, %d git calls", none, g.oidCalls)
	}
}

func TestWithInput(t *testing.T) {
	const in = "0123456789abcdef"
	for body, want := range map[string]string{
		"t\n\n<!-- deps:fingerprint=aa -->\n":                            "t\n\n<!-- deps:fingerprint=aa -->\n<!-- deps:input=" + in + " -->\n",
		"t\n<!-- deps:fingerprint=aa -->\n<!-- deps:input=bb -->\nend\n": "t\n<!-- deps:fingerprint=aa -->\n<!-- deps:input=" + in + " -->\nend\n",
		"hand-written\n\n": "hand-written\n\n<!-- deps:input=" + in + " -->\n",
	} {
		got := withInput(body, in)
		if got != want {
			t.Errorf("withInput(%q) = %q, want %q", body, got, want)
		}
		if bodyInput(got) != in {
			t.Errorf("bodyInput(%q) = %q", got, bodyInput(got))
		}
	}
}
