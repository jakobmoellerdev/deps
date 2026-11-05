package bot

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
)

func TestRunSchedule(t *testing.T) {
	weekly, err := schedule.Parse([]string{"0 0 * * 0"})
	if err != nil {
		t.Fatal(err)
	}
	inWindow := schedule.Trigger{Event: schedule.EventSchedule, Cron: "0  0 * * 0"}
	outWindow := schedule.Trigger{Event: schedule.EventSchedule, Cron: "0 0 * * *"}
	tests := []struct {
		name               string
		trigger            schedule.Trigger
		security           bool
		updateNotScheduled bool
		noPR               bool // branchA exists without an open PR
		fresh              bool // the open PR of branchA has the current fingerprint
		wantStatus         map[string]string
		wantPushed         int
		wantClosed         []int
	}{
		{
			name:       "triggered by its cron",
			trigger:    inWindow,
			wantStatus: map[string]string{branchA: StatusUpdated, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantPushed: 2, wantClosed: []int{9},
		},
		{
			name:       "other cron keeps PR open",
			trigger:    outWindow,
			wantStatus: map[string]string{branchA: StatusSkipSchedule, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantPushed: 1, wantClosed: []int{9},
		},
		{
			name:               "updateNotScheduled updates the open PR out of window",
			trigger:            outWindow,
			updateNotScheduled: true,
			wantStatus:         map[string]string{branchA: StatusUpdated, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantPushed:         2, wantClosed: []int{9},
		},
		{
			name:               "updateNotScheduled keeps an unchanged PR",
			trigger:            outWindow,
			updateNotScheduled: true,
			fresh:              true,
			wantStatus:         map[string]string{branchA: StatusUpToDate, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantPushed:         1, wantClosed: []int{9},
		},
		{
			name:               "updateNotScheduled never creates out of window",
			trigger:            outWindow,
			updateNotScheduled: true,
			noPR:               true,
			wantStatus:         map[string]string{branchA: StatusSkipSchedule, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantPushed:         1, wantClosed: []int{9},
		},
		{
			name:       "workflow_dispatch acts immediately",
			trigger:    schedule.Trigger{Event: "workflow_dispatch"},
			wantStatus: map[string]string{branchA: StatusUpdated, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantPushed: 2, wantClosed: []int{9},
		},
		{
			name:               "push run refreshes the scheduled open PR",
			trigger:            schedule.Trigger{Event: "push"},
			updateNotScheduled: true,
			wantStatus:         map[string]string{branchA: StatusUpdated, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantPushed:         2, wantClosed: []int{9},
		},
		{
			name:               "push run never creates a scheduled PR",
			trigger:            schedule.Trigger{Event: "push"},
			updateNotScheduled: true,
			noPR:               true,
			wantStatus:         map[string]string{branchA: StatusSkipSchedule, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantPushed:         1, wantClosed: []int{9},
		},
		{
			name:       "push run still creates security PRs",
			trigger:    schedule.Trigger{Event: "push"},
			security:   true,
			noPR:       true,
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantPushed: 2, wantClosed: []int{9},
		},
		{
			name:       "local run acts immediately",
			wantStatus: map[string]string{branchA: StatusUpdated, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantPushed: 2, wantClosed: []int{9},
		},
		{
			name:       "schedule event without cron matches",
			trigger:    schedule.Trigger{Event: schedule.EventSchedule},
			wantStatus: map[string]string{branchA: StatusUpdated, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantPushed: 2, wantClosed: []int{9},
		},
		{
			name:       "security ignores schedule",
			trigger:    outWindow,
			security:   true,
			wantStatus: map[string]string{branchA: StatusUpdated, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantPushed: 2, wantClosed: []int{9},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, res := fixture(t)
			res.Updates[0].Schedule = weekly
			if tt.security {
				res.Updates[0].Advisories = []dep.Advisory{{ID: "GHSA-x", Summary: "bad", URL: "https://example.invalid/GHSA-x"}}
			}
			g := &fakeGit{
				branches: map[string]string{"refs/heads/" + branchA: "x", "refs/heads/deps/gomod/gone": "y"},
				authors:  map[string]string{branchA: email},
			}
			body := "stale"
			if tt.fresh {
				body = "<!-- deps:fingerprint=" + fingerprintOf(t, root, res, "a/x") + " -->"
			}
			pc := &fakePC{open: []platform.PR{{Number: 9, Head: "deps/gomod/gone"}}}
			if !tt.noPR {
				pc.open = append(pc.open, platform.PR{Number: 1, Head: branchA, Body: body, Mergeable: new(true)})
			}
			o := Options{AuthorEmail: email, Managers: managers, MaxNewPRs: 10, Trigger: tt.trigger, UpdateNotScheduled: tt.updateNotScheduled}
			acts, errs := Run(t.Context(), o, root, res, nil, pc, g)
			if len(errs) != 0 {
				t.Fatal(errs)
			}
			got := map[string]string{}
			for _, a := range acts {
				got[a.Branch] = a.Status
			}
			if len(got) != len(tt.wantStatus) {
				t.Errorf("actions = %v, want %v", got, tt.wantStatus)
			}
			for b, s := range tt.wantStatus {
				if got[b] != s {
					t.Errorf("%s: status %q, want %q", b, got[b], s)
				}
			}
			if len(g.pushed) != tt.wantPushed {
				t.Errorf("pushed = %v, want %d", g.pushed, tt.wantPushed)
			}
			if len(pc.closedNow) != len(tt.wantClosed) || pc.closedNow[0] != tt.wantClosed[0] {
				t.Errorf("closed = %v, want %v", pc.closedNow, tt.wantClosed)
			}
			if len(pc.got) != 0 {
				t.Errorf("GetPR calls = %v", pc.got)
			}
		})
	}
}

func TestWaitCrons(t *testing.T) {
	set := func(c ...string) *schedule.Set {
		s, err := schedule.Parse(c)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	ups := []dep.Update{
		{Schedule: set("0 0 * * 0", "0 6 * * 1")},
		{}, // always
		{Schedule: set("0 6 * * 1")},
		{Schedule: set("0 9 * * *"), Advisories: []dep.Advisory{{ID: "GO-1"}}}, // security: ignored
	}
	if got := waitCrons(ups); !slices.Equal(got, []string{"0 6 * * 1"}) {
		t.Errorf("waitCrons = %q", got)
	}
	if got := waitCrons(ups[:2]); !slices.Equal(got, []string{"0 0 * * 0", "0 6 * * 1"}) || ups[0].Schedule.Crons()[0] != "0 0 * * 0" {
		t.Errorf("waitCrons = %q", got)
	}
}

func TestPRBodySecurity(t *testing.T) {
	u := dep.Update{
		Dep: dep.Dep{File: "go.mod", Line: 3, Version: "v1.0.0"}, Type: dep.Patch, NewVersion: "v1.0.1",
		Advisories: []dep.Advisory{{ID: "GO-1", Summary: "rce", URL: "https://pkg.go.dev/vuln/GO-1"}, {ID: "GO-2", Summary: "dos"}},
	}
	dup := u
	body := prBody(t.Context(), nil, []dep.Update{u, dup}, "abc")
	for _, want := range []string{"| `go.mod:3` | `v1.0.0` | `v1.0.1` | patch |", "### Security", "- [GO-1](https://pkg.go.dev/vuln/GO-1): rce\n", "- GO-2: dos\n", "<!-- deps:fingerprint=abc -->"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	if strings.Count(body, "GO-2") != 1 {
		t.Errorf("advisory duplicated:\n%s", body)
	}
	if strings.Contains(prBody(t.Context(), nil, []dep.Update{{Dep: dep.Dep{Version: "v1"}, NewVersion: "v2"}}, "x"), "Security") {
		t.Error("security section without advisories")
	}
}

func TestPRBodyLockfileOnly(t *testing.T) {
	u := dep.Update{Dep: dep.Dep{File: "package.json", Line: 4, Version: "5.2.8", Range: "^5.2.8", Locked: "5.3.0"}, Type: dep.Patch, NewVersion: "5.3.1", Lockfile: true}
	if body := prBody(t.Context(), nil, []dep.Update{u}, "abc"); !strings.Contains(body, "| `package.json:4` | `5.3.0` | `5.3.1` | patch (lockfile) |") {
		t.Errorf("body:\n%s", body)
	}
}
func TestPRBodyModulePath(t *testing.T) {
	u := dep.Update{
		Dep:  dep.Dep{Manager: "gomod", File: "go.mod", Line: 3, Name: "github.com/google/go-github/v92", Version: "v92.0.0"},
		Type: dep.Major, NewVersion: "v93.0.0", NewName: "github.com/google/go-github/v93",
	}
	other := u
	other.Dep.File = "sub/go.mod"
	body := prBody(t.Context(), nil, []dep.Update{u, other}, "abc")
	for _, want := range []string{
		"| `go.mod:3` | `v92.0.0` | `github.com/google/go-github/v93@v93.0.0` | major |",
		"`github.com/google/go-github/v92` moves to the module path `github.com/google/go-github/v93`",
		"API may have changed",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	if strings.Count(body, "moves to the module path") != 1 {
		t.Errorf("note repeated:\n%s", body)
	}
}

// The updates of one dependency in different PR buckets get their own branches and PRs; each
// branch is built from its own update only.
func TestRunBucketBranches(t *testing.T) {
	root, res := fixture(t)
	minor := res.Updates[0]
	minor.Type, minor.NewVersion = dep.Minor, "v1.1"
	major := res.Updates[0]
	major.Bucket = "major"
	v3 := res.Updates[0]
	v3.NewVersion, v3.Bucket = "v3", "v3"
	res.Updates = []dep.Update{minor, major, v3}
	g := &fakeGit{}
	pc := &fakePC{open: []platform.PR{{Number: 4, Head: branchA, Body: "stale"}}}
	acts, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, root, res, nil, pc, g)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	got := map[string]string{}
	for _, a := range acts {
		got[a.Branch] = a.Status + " " + a.Title
	}
	want := map[string]string{
		branchA:            StatusUpdated + " chore(deps): update a/x to v1.1",
		branchA + "-major": StatusCreated + " chore(deps): update a/x to v2",
		branchA + "-v3":    StatusCreated + " chore(deps): update a/x to v3",
	}
	if !maps.Equal(got, want) {
		t.Errorf("actions = %v, want %v", got, want)
	}
	if !slices.Equal(pc.created, []string{branchA + "-major", branchA + "-v3"}) || !slices.Equal(pc.updated, []int{4}) || len(g.pushed) != 3 {
		t.Errorf("created %v updated %v pushed %v", pc.created, pc.updated, g.pushed)
	}
}
