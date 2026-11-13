package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// fakeRepo is a git-tags datasource of one repository: tags (name -> commit), the commit of
// HEAD and commit times (missing = unknown, commitTimeErr = failed lookup), counting
// CommitTime calls.
type fakeRepo struct {
	tags          map[string]string
	head          string
	commitTimes   map[string]time.Time
	commitTimeErr error
	timeCalls     atomic.Int32
}

func (f *fakeRepo) Releases(context.Context, dep.Dep, string) ([]Release, error) {
	var out []Release
	for _, n := range slices.Sorted(maps.Keys(f.tags)) {
		out = append(out, Release{Version: n, Digest: f.tags[n]})
	}
	return out, nil
}

func (f *fakeRepo) Digest(_ context.Context, _ dep.Dep, v string) (string, error) {
	if c, ok := f.tags[v]; ok {
		return c, nil
	}
	return "", fmt.Errorf("tag %s %w", v, datasource.ErrVersionNotFound)
}

func (f *fakeRepo) ReleaseTime(context.Context, dep.Dep, string) (time.Time, error) {
	return time.Time{}, nil
}

func (f *fakeRepo) Head(context.Context, dep.Dep, string) (datasource.Revision, error) {
	return datasource.Revision{Digest: f.head}, nil
}

func (f *fakeRepo) CommitTime(_ context.Context, _ dep.Dep, commit string) (time.Time, error) {
	f.timeCalls.Add(1)
	if f.commitTimeErr != nil {
		return time.Time{}, f.commitTimeErr
	}
	return f.commitTimes[commit], nil
}

// A commit time the forge cannot answer is no unknown time: the commit pin fails instead of
// refreshing its digest to a head that may be minutes old under a minimumReleaseAge.
func TestCommitPinCommitTimeError(t *testing.T) {
	const wf = ".github/workflows/ci.yml"
	c := func(s string) string { return sha("useblacksmith/begin-testbox@" + s) }
	src := "jobs:\n  testbox:\n    steps:\n      - name: Begin Testbox\n        uses: useblacksmith/begin-testbox@" + c("main~3") + "\n"
	ds := &fakeRepo{tags: map[string]string{"v1.0.0": c("v1.0.0")}, head: c("main"), commitTimeErr: errors.New("upstream unavailable")}
	cfg := "minimumReleaseAge: 30 days\nrules:\n  - names: [useblacksmith/begin-testbox]\n    ignore: false\n"
	root, conf := setup(t, cfg, map[string]string{wf: src})
	res, err := Resolve(t.Context(), root, conf, env, Options{Managers: managers, Datasources: map[string]Datasource{"git-tags": ds}, Now: ageNow})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Updates) != 0 || len(res.Pending) != 0 {
		t.Errorf("updates = %+v, pending = %+v; want none", res.Updates, res.Pending)
	}
	var de *DepError
	switch {
	case len(res.Errors) != 1:
		t.Fatalf("errors = %v, want the pin whose commit time failed", res.Errors)
	case !errors.As(res.Errors[0], &de) || de.Name != "useblacksmith/begin-testbox":
		t.Errorf("error = %v, want a DepError of the action", res.Errors[0])
	case !strings.Contains(de.Error(), "commit time of "+dep.ShortDigest(c("main"))):
		t.Errorf("error = %v, want the failed lookup named", de)
	}
}

// TestCommitPins resolves bare SHA pins in openclaw's shape: a SHA a tag points at is that
// tag's version; any other SHA is ignored by default (Renovate's unversioned-reference) and,
// once enabled, tracks the default branch head.
func TestCommitPins(t *testing.T) {
	const wf = ".github/workflows/ci.yml"
	c := func(s string) string { return sha("useblacksmith/begin-testbox@" + s) }
	tagged := map[string]string{"v0": c("v0.4.1"), "v0.4.1": c("v0.4.1"), "v0.5.0": c("v0.5.0"), "v1.0.0": c("v1.0.0")}
	untagged := c("main~3")
	enable := "rules:\n  - names: [useblacksmith/begin-testbox]\n    ignore: false\n"
	tests := []struct {
		name, cfg, pin, head string
		commitTime           time.Time
		want                 []wantUpdate
		line, skip, pending  string
		timeCalls            int32
	}{
		{
			name: "at a tag: newest version written as comment", pin: c("v0.4.1"),
			want: []wantUpdate{{typ: dep.Minor, version: "v0.5.0", digest: c("v0.5.0")}, {typ: dep.Major, version: "v1.0.0", digest: c("v1.0.0"), b: "major"}},
			line: "        uses: useblacksmith/begin-testbox@" + c("v0.5.0") + " # v0.5.0",
		},
		{name: "at the newest tag", pin: c("v1.0.0")},
		{
			name: "at a tag: rules match the version", pin: c("v0.4.1"), cfg: "rules:\n  - names: [useblacksmith/begin-testbox]\n    allow: \"< 0.5\"\n",
		},
		{
			name: "at a tag: an ignore rule wins", pin: c("v0.4.1"), cfg: "rules:\n  - names: [useblacksmith/*]\n    ignore: true\n",
			skip: "ignored by rule #0",
		},
		{name: "no tag: ignored by default", pin: untagged, head: c("main"), skip: config.UnversionedReason},
		{
			name: "no tag, enabled: digest to the head", pin: untagged, head: c("main"), cfg: enable,
			want: []wantUpdate{{typ: dep.Digest, digest: c("main")}},
			line: "        uses: useblacksmith/begin-testbox@" + c("main"),
		},
		{name: "no tag, enabled: at the head", pin: untagged, head: untagged, cfg: enable},
		{name: "no tag, enabled: empty repository", pin: untagged, cfg: enable},
		{name: "no tag, enabled: digest updates off", pin: untagged, head: c("main"), cfg: enable + "    allowedUpdateTypes: [major, minor, patch]\n"},
		{
			name: "no tag, enabled: too new", pin: untagged, head: c("main"), commitTime: daysAgo(14), cfg: "minimumReleaseAge: 30 days\n" + enable,
			pending: tooNew(14, "30 days"), timeCalls: 1,
		},
		{
			name: "no tag, enabled: old enough", pin: untagged, head: c("main"), commitTime: daysAgo(40), cfg: "minimumReleaseAge: 30 days\n" + enable,
			want: []wantUpdate{{typ: dep.Digest, digest: c("main")}}, timeCalls: 1,
		},
		{
			name: "no tag, enabled: unknown time, timestamp-required", pin: untagged, head: c("main"),
			cfg:     "minimumReleaseAge: 30 days\nminimumReleaseAgeBehaviour: timestamp-required\n" + enable,
			pending: "release time unknown (minimum 30 days, timestamp-required)", timeCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "jobs:\n  testbox:\n    steps:\n      - name: Begin Testbox\n        uses: useblacksmith/begin-testbox@" + tt.pin + "\n"
			ds := &fakeRepo{tags: tagged, head: tt.head, commitTimes: map[string]time.Time{c("main"): tt.commitTime}}
			root, cfg := setup(t, tt.cfg, map[string]string{wf: src})
			res, err := Resolve(t.Context(), root, cfg, env, Options{Managers: managers, Datasources: map[string]Datasource{"git-tags": ds}, Now: ageNow})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Errors) > 0 {
				t.Fatal(res.Errors)
			}
			if got := gotUpdates(res); !slices.Equal(got, tt.want) {
				t.Errorf("updates = %+v, want %+v", got, tt.want)
			}
			var skips []string
			for _, d := range res.Skipped {
				skips = append(skips, d.Skip)
			}
			if (tt.skip == "") != (len(skips) == 0) || tt.skip != "" && skips[0] != tt.skip {
				t.Errorf("skipped = %q, want %q", skips, tt.skip)
			}
			if tt.pending != "" && (len(res.Pending) != 1 || res.Pending[0].Version != dep.ShortDigest(c("main")) || res.Pending[0].Reason != tt.pending) {
				t.Errorf("pending = %+v, want %q", res.Pending, tt.pending)
			}
			if n := ds.timeCalls.Load(); n != tt.timeCalls {
				t.Errorf("CommitTime calls = %d, want %d", n, tt.timeCalls)
			}
			if tt.line == "" {
				return
			}
			changes, errs := Build(t.Context(), root, managers, res.Updates[:1], nil)
			if len(errs) > 0 || len(changes) != 1 {
				t.Fatalf("changes = %+v, errors %v", changes, errs)
			}
			if got := line(5, string(changes[0].New)); got != tt.line {
				t.Errorf("line:\n got %s\nwant %s", got, tt.line)
			}
			u := res.Updates[0]
			from, to := u.FromTo()
			title := GroupUpdates(res.Updates[:1], "")[0].Title
			switch u.Type {
			case dep.Digest:
				if from != "@"+dep.ShortDigest(tt.pin) || to != "@"+dep.ShortDigest(tt.head) || title != "chore(deps): update useblacksmith/begin-testbox digest to "+dep.ShortDigest(tt.head) {
					t.Errorf("FromTo = %s -> %s, title %q", from, to, title)
				}
			default:
				if !strings.HasPrefix(from, "v0.4.1@") || title != "chore(deps): update useblacksmith/begin-testbox to v0.5.0" {
					t.Errorf("FromTo = %s -> %s, title %q", from, to, title)
				}
			}
		})
	}
}
