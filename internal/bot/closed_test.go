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
			wantMarker: "<!-- deps:versions=a%2Fx=v1.2 -->",
		},
		{
			name: "minor: closed PR for the same version", bucket: "", version: "v1.1", branch: branchA,
			closed: closedPR(branchA, "chore(deps): update a/x to v1.1", ""), want: StatusSkipClosed,
		},
		{
			name: "minor: marker of the same version under another title", bucket: "", version: "v1.1", branch: branchA,
			closed: closedPR(branchA, "Update a/x", "a%2Fx=v1.1"), want: StatusSkipClosed,
		},
		{
			name: "minor: marker of another version under the same title", bucket: "", version: "v1.2", branch: branchA,
			closed: closedPR(branchA, "chore(deps): update a/x to v1.2", "a%2Fx=v1.1"), want: StatusCreated,
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
	for body, want := range map[string]map[string][]target{
		"no marker": nil,
		"<!-- deps:versions=a%2Fx=v2 @s%2Fp=2.0.0+b a%2Fx=v2.1 bad -->": {"a/x": {{"v2", ""}, {"v2.1", ""}}, "@s/p": {{"2.0.0 b", ""}}},
		"<!-- deps:versions=img=1.2@sha256%3Aabc tag=@sha256%3Adef -->": {"img": {{"1.2", "sha256:abc"}}, "tag": {{"", "sha256:def"}}},
		"<!-- deps:versions=%zz=1 a=%zz b=1@%zz -->":                    {},
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

func TestVersionsMarker(t *testing.T) {
	u := func(name, version, digest, bucket string) dep.Update {
		return dep.Update{Dep: dep.Dep{Name: name}, NewVersion: version, NewDigest: digest, Bucket: bucket}
	}
	for _, tt := range []struct {
		ups  []dep.Update
		want string
	}{
		{[]dep.Update{u("a/x", "v1.1", "", "")}, "<!-- deps:versions=a%2Fx=v1.1 -->"},
		{[]dep.Update{u("img", "1.2", "sha256:abc", "major"), u("img", "1.2", "sha256:abc", "major")}, "<!-- deps:versions=img=1.2@sha256%3Aabc -->"},
		{[]dep.Update{{Dep: dep.Dep{Name: "Cargo.lock"}, Type: dep.LockFileMaintenance}}, ""},
	} {
		if got := versionsMarker(tt.ups); got != tt.want {
			t.Errorf("versionsMarker(%+v) = %q, want %q", tt.ups, got, tt.want)
		}
	}
}

// Closed PRs match by their versions marker, so custom titles (prTitle), a title template
// changed since the PR was closed, or a title without the version still suppress exactly the
// closed targets.
func TestClosedForCustomTitles(t *testing.T) {
	const branch = "deps/docker/img"
	up := func(version, digest string) dep.Update {
		return dep.Update{Dep: dep.Dep{Manager: "docker", Name: "img", Versioning: "docker"}, Type: dep.Minor, NewVersion: version, NewDigest: digest}
	}
	grp := func(title string, ups ...dep.Update) engine.Group {
		return engine.Group{Branch: branch, Title: title, Updates: ups}
	}
	closed := func(title, body string) []platform.PR {
		return []platform.PR{{Number: 4, Head: branch, Title: title, Body: body}}
	}
	sec := up("1.2", "")
	sec.Advisories = []dep.Advisory{{ID: "GHSA-1"}}
	tests := []struct {
		name   string
		closed []platform.PR
		grp    engine.Group
		want   string
	}{
		{"same version, other title", closed("Update img", "<!-- deps:versions=img=1.2 -->"), grp("bump img", up("1.2", "")), StatusSkipClosed},
		{"title without version, next version", closed("Update img", "<!-- deps:versions=img=1.2 -->"), grp("Update img", up("1.3", "")), ""},
		{"same version, other digest", closed("Update img", "<!-- deps:versions=img=1.2@sha256%3Aa -->"), grp("Update img", up("1.2", "sha256:b")), ""},
		{"same version and digest", closed("x", "<!-- deps:versions=img=1.2@sha256%3Aa -->"), grp("y", up("1.2", "sha256:a")), StatusSkipClosed},
		{"no marker: same title", closed("Update img", ""), grp("Update img", up("1.3", "")), StatusSkipClosed},
		{"no marker: other title", closed("Update img to 1.2", ""), grp("bump img", up("1.2", "")), ""},
		{"security: closed PR without advisories", closed("Update img", "<!-- deps:versions=img=1.2 -->"), grp("Update img [security]", sec), ""},
		{"security: closed security PR", closed("Update img [security]", "<!-- deps:versions=img=1.2 -->"), grp("bump img [security]", sec), StatusSkipClosed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, status := closedFor(tt.closed, tt.grp)
			if status != tt.want || (c != nil) != (tt.want != "") {
				t.Fatalf("closedFor = %v, %q, want %q", c, status, tt.want)
			}
		})
	}
}

// TestDeclineReplacements: a replacement whose PR was closed brings back the dep's usual
// updates next to it; an open PR, a ticked recreate checkbox or a closed PR with another
// title (a new replacement version) keep only the replacement.
func TestDeclineReplacements(t *testing.T) {
	d := dep.Dep{Manager: "npm", File: "package.json", Line: 1, Name: "jade", Version: "1.11.0"}
	usual := dep.Update{Dep: d, Type: dep.Minor, NewVersion: "1.12.0"}
	rep := dep.Update{Dep: d, Type: dep.Replacement, NewName: "pug", NewVersion: "2.0.0", Bucket: engine.ReplacementBucket, Declined: []dep.Update{usual}}
	other := dep.Update{Dep: dep.Dep{Manager: "npm", File: "package.json", Line: 1, Name: "left-pad", Version: "1.3.0"}, Type: dep.Patch, NewVersion: "1.3.1"}
	const branch, title = "deps/npm/jade-replacement", "chore(deps): replace jade with pug 2.0.0"
	pr := func(t string) []platform.PR { return []platform.PR{{Number: 3, Head: branch, Title: t}} }
	for _, tt := range []struct {
		name         string
		open, closed []platform.PR
		dash         *dashboard
		want         []string
	}{
		{"closed", nil, pr(title), nil, []string{"jade replacement 2.0.0", "jade minor 1.12.0", "left-pad patch 1.3.1"}},
		{"no PR", nil, nil, nil, []string{"jade replacement 2.0.0", "left-pad patch 1.3.1"}},
		{"open PR", pr(title), pr(title), nil, []string{"jade replacement 2.0.0", "left-pad patch 1.3.1"}},
		{"recreate ticked", nil, pr(title), &dashboard{recreate: map[string]bool{branch: true}}, []string{"jade replacement 2.0.0", "left-pad patch 1.3.1"}},
		{"other version closed", nil, pr("chore(deps): replace jade with pug 1.0.0"), nil, []string{"jade replacement 2.0.0", "left-pad patch 1.3.1"}},
	} {
		var got []string
		for _, u := range declineReplacements([]dep.Update{rep, other}, tt.open, tt.closed, "", tt.dash) {
			got = append(got, u.Dep.Name+" "+string(u.Type)+" "+u.NewVersion)
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}
