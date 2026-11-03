package bot

import (
	"strings"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
)

func TestRunSchedule(t *testing.T) {
	night, err := schedule.Parse([]string{"* 0-5 * * *"}, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	inWindow := time.Date(2026, 11, 3, 3, 0, 0, 0, time.UTC)
	outWindow := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		now        time.Time
		security   bool
		wantStatus map[string]string
		wantPushed int
		wantClosed []int
	}{
		{
			name:       "in window",
			now:        inWindow,
			wantStatus: map[string]string{branchA: StatusUpdated, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantPushed: 2, wantClosed: []int{9},
		},
		{
			name:       "out of window keeps PR open",
			now:        outWindow,
			wantStatus: map[string]string{branchA: StatusSkipSchedule, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantPushed: 1, wantClosed: []int{9},
		},
		{
			name:       "security ignores schedule",
			now:        outWindow,
			security:   true,
			wantStatus: map[string]string{branchA: StatusUpdated, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantPushed: 2, wantClosed: []int{9},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, res := fixture(t)
			res.Updates[0].Schedule = night
			if tt.security {
				res.Updates[0].Advisories = []dep.Advisory{{ID: "GHSA-x", Summary: "bad", URL: "https://example.invalid/GHSA-x"}}
			}
			g := &fakeGit{
				branches: map[string]string{"refs/heads/" + branchA: "x", "refs/heads/deps/gomod/gone": "y"},
				authors:  map[string]string{branchA: email},
			}
			pc := &fakePC{open: []platform.PR{
				{Number: 1, Head: branchA, Body: "stale"},
				{Number: 9, Head: "deps/gomod/gone"},
			}}
			o := Options{AuthorEmail: email, Managers: managers, MaxNewPRs: 10, Now: tt.now}
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

func TestScheduledZeroNowUsesClock(t *testing.T) {
	root, res := fixture(t)
	never, err := schedule.Parse([]string{"* * 31 2 *"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := range res.Updates {
		res.Updates[i].Schedule = never
	}
	g := &fakeGit{}
	acts, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers, MaxNewPRs: 10}, root, res, nil, &fakePC{}, g)
	if len(errs) != 0 || len(g.pushed) != 0 || len(acts) != 2 || acts[0].Status != StatusSkipSchedule {
		t.Fatalf("acts = %+v errs = %v pushed = %v", acts, errs, g.pushed)
	}
}

func TestPRBodySecurity(t *testing.T) {
	u := dep.Update{
		Dep: dep.Dep{File: "go.mod", Line: 3, Version: "v1.0.0"}, Type: dep.Patch, NewVersion: "v1.0.1",
		Advisories: []dep.Advisory{{ID: "GO-1", Summary: "rce", URL: "https://pkg.go.dev/vuln/GO-1"}, {ID: "GO-2", Summary: "dos"}},
	}
	dup := u
	body := prBody([]dep.Update{u, dup}, "abc")
	for _, want := range []string{"| `go.mod:3` | `v1.0.0` | `v1.0.1` | patch |", "### Security", "- [GO-1](https://pkg.go.dev/vuln/GO-1): rce\n", "- GO-2: dos\n", "<!-- deps:fingerprint=abc -->"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	if strings.Count(body, "GO-2") != 1 {
		t.Errorf("advisory duplicated:\n%s", body)
	}
	if strings.Contains(prBody([]dep.Update{{Dep: dep.Dep{Version: "v1"}, NewVersion: "v2"}}, "x"), "Security") {
		t.Error("security section without advisories")
	}
}
