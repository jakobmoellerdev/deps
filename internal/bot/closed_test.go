package bot

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// closedFixture is the bot fixture with a/x's update to version in bucket (b/y's when b is
// set too, in the policy group "G"); its deps use the tag scheme.
func closedFixture(t *testing.T, bucket, version, b string) (string, *engine.Result) {
	t.Helper()
	root, res := fixture(t)
	for i := range res.Updates {
		u := &res.Updates[i]
		u.Dep.Versioning, u.Bucket, u.NewVersion = "tag", bucket, version
		if bucket == "" {
			u.Type = dep.Minor
		}
		if b != "" {
			u.Group = "G"
		}
	}
	if b == "" {
		res.Updates = res.Updates[:1]
	} else {
		res.Updates[1].NewVersion = b
	}
	return root, res
}

// closedPR is an unmerged closed PR on branch with title; versions ("name=version ...") is
// its versions marker, none when empty.
func closedPR(branch, title, versions string) platform.PR {
	body := "x\n<!-- deps:fingerprint=0000000000000000 -->\n"
	if versions != "" {
		body += "<!-- deps:versions=" + versions + " -->\n"
	}
	return platform.PR{Number: 4, Head: branch, Title: title, Body: body, URL: "https://forge/pulls/4"}
}

func TestRunClosedPRs(t *testing.T) {
	const (
		major   = branchA + "-major"
		group   = "deps/group/g-major"
		v2Title = "chore(deps): update a/x to v2"
	)
	advisory := func(u *dep.Update) { u.Advisories = []dep.Advisory{{ID: "GHSA-1"}} }
	tests := []struct {
		name            string
		bucket, version string
		b               string // b/y's version: a group of a/x and b/y
		security        bool
		closed          platform.PR
		merged          bool
		branch          string // of the update; default major
		want            string
		wantMarker      string // in the created PR's body
	}{
		{
			name: "closed major PR ignores a newer version of that major", bucket: "major", version: "v2.1",
			closed: closedPR(major, v2Title, "a%2Fx=v2"), want: StatusSkipClosedMajor,
		},
		{
			name: "closed major PR does not ignore the next major", bucket: "major", version: "v3",
			closed: closedPR(major, v2Title, "a%2Fx=v2"), want: StatusCreated, wantMarker: "<!-- deps:versions=a%2Fx=v3 -->",
		},
		{
			name: "closed major PR for the same version", bucket: "major", version: "v2",
			closed: closedPR(major, v2Title, "a%2Fx=v2"), want: StatusSkipClosed,
		},
		{
			name: "closed major PR without marker: version from the title", bucket: "major", version: "v2.1",
			closed: closedPR(major, v2Title, ""), want: StatusSkipClosedMajor,
		},
		{
			name: "closed major PR without marker, next major", bucket: "major", version: "v3",
			closed: closedPR(major, v2Title, ""), want: StatusCreated,
		},
		{
			name: "closed major PR of another dependency", bucket: "major", version: "v2.1",
			closed: closedPR(major, "chore(deps): update a/z to v2", ""), want: StatusCreated,
		},
		{
			name: "merged major PR does not block", bucket: "major", version: "v2.1",
			closed: closedPR(major, v2Title, "a%2Fx=v2"), merged: true, want: StatusCreated,
		},
		{
			name: "per-major bucket", bucket: "v2", version: "v2.1", branch: branchA + "-v2",
			closed: closedPR(branchA+"-v2", v2Title, "a%2Fx=v2"), want: StatusSkipClosedMajor, wantMarker: "",
		},
		{
			name: "security update ignores a closed PR without advisories", bucket: "major", version: "v2.1", security: true,
			closed: closedPR(major, v2Title, "a%2Fx=v2"), want: StatusCreated,
		},
		{
			name: "security update ignored by a closed security PR", bucket: "major", version: "v2.1", security: true,
			closed: closedPR(major, v2Title+" [security]", "a%2Fx=v2"), want: StatusSkipClosedMajor,
		},
		{
			name: "group: every dependency in the closed majors", bucket: "major", version: "v2.1", b: "v3.1", branch: group,
			closed: closedPR(group, "chore(deps): update G (2 dependencies)", "a%2Fx=v2 b%2Fy=v3"), want: StatusSkipClosedMajor,
		},
		{
			name: "group: one dependency in the next major", bucket: "major", version: "v2.1", b: "v4", branch: group,
			closed: closedPR(group, "chore(deps): update G (2 dependencies)", "a%2Fx=v2 b%2Fy=v3"), want: StatusCreated,
			wantMarker: "<!-- deps:versions=a%2Fx=v2.1 b%2Fy=v4 -->",
		},
		{
			name: "minor: closed PR ignores only its version", bucket: "", version: "v1.2", branch: branchA,
			closed: closedPR(branchA, "chore(deps): update a/x to v1.1", ""), want: StatusCreated,
		},
		{
			name: "minor: closed PR for the same version", bucket: "", version: "v1.1", branch: branchA,
			closed: closedPR(branchA, "chore(deps): update a/x to v1.1", ""), want: StatusSkipClosed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, res := closedFixture(t, tt.bucket, tt.version, tt.b)
			if tt.security {
				advisory(&res.Updates[0])
			}
			branch := tt.branch
			if branch == "" {
				branch = major
			}
			tt.closed.Merged = tt.merged
			pc := &fakePC{closed: []platform.PR{tt.closed}}
			acts, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, root, res, nil, pc, &fakeGit{})
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			if got := statusOf(acts, branch); got != tt.want {
				t.Fatalf("status of %s = %q, want %q (%+v)", branch, got, tt.want, acts)
			}
			if tt.want != StatusCreated {
				if a := acts[slices.IndexFunc(acts, func(a Action) bool { return a.Branch == branch })]; a.Number != 4 || a.URL != "https://forge/pulls/4" || len(pc.created) != 0 {
					t.Errorf("skipped action %+v created %v", a, pc.created)
				}
				return
			}
			if !slices.Equal(pc.created, []string{branch}) {
				t.Fatalf("created %v", pc.created)
			}
			if !strings.Contains(pc.bodies[0], tt.wantMarker) {
				t.Errorf("body lacks %q:\n%s", tt.wantMarker, pc.bodies[0])
			}
			if tt.bucket == "" && strings.Contains(pc.bodies[0], "deps:versions=") {
				t.Errorf("non-major body has a versions marker:\n%s", pc.bodies[0])
			}
		})
	}
}

// A closed major PR is listed under "Ignored or blocked"; its recreate checkbox makes the next
// run create the PR.
func TestDashboardRecreateClosedMajor(t *testing.T) {
	const major = branchA + "-major"
	root, res := closedFixture(t, "major", "v2.1", "")
	closed := []platform.PR{closedPR(major, "chore(deps): update a/x to v2", "a%2Fx=v2")}
	pc := &fakePC{closed: closed}
	acts, errs, _ := dashRun(t, Options{}, res, root, pc, &fakeGit{})
	if len(errs) > 0 || statusOf(acts, major) != StatusSkipClosedMajor {
		t.Fatalf("%v %+v", errs, acts)
	}
	want := "## Ignored or blocked\n\n- [ ] <!-- deps:recreate=" + major + " --> chore(deps): update a/x to v2.1 ([#4](https://forge/pulls/4) closed without merging, so this major is ignored; tick to recreate it)\n"
	if !strings.Contains(pc.createdIssues[0].Body, want) {
		t.Fatalf("body lacks %q:\n%s", want, pc.createdIssues[0].Body)
	}

	issue := platform.Issue{Number: 7, Title: dashTitle, Body: strings.Replace(pc.createdIssues[0].Body, "- [ ] <!-- deps:recreate=", "- [x] <!-- deps:recreate=", 1)}
	pc = &fakePC{closed: closed, issues: []platform.Issue{issue}}
	acts, errs, _ = dashRun(t, Options{}, res, root, pc, &fakeGit{})
	if len(errs) > 0 || statusOf(acts, major) != StatusCreated || !slices.Equal(pc.created, []string{major}) {
		t.Fatalf("recreate: %v %+v created %v", errs, acts, pc.created)
	}
}

func TestBodyVersions(t *testing.T) {
	for body, want := range map[string]map[string][]string{
		"no marker": nil,
		"<!-- deps:versions=a%2Fx=v2 @s%2Fp=2.0.0+b a%2Fx=v2.1 bad -->": {"a/x": {"v2", "v2.1"}, "@s/p": {"2.0.0 b"}},
		"<!-- deps:versions=%zz=1 a=%zz -->":                            {},
	} {
		got := bodyVersions(body)
		if (got == nil) != (want == nil) || len(got) != len(want) {
			t.Errorf("bodyVersions(%q) = %v, want %v", body, got, want)
			continue
		}
		for k, v := range want {
			if !slices.Equal(got[k], v) {
				t.Errorf("bodyVersions(%q)[%q] = %v, want %v", body, k, got[k], v)
			}
		}
	}
}
