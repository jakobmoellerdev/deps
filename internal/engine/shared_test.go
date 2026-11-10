package engine

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// sharedDep is a maven dep whose version is the jackson.version property of pom.xml.
func sharedDep(name string) dep.Dep {
	return dep.Dep{Manager: "maven", Versioning: "maven", File: "pom.xml", Line: 3, Name: name, Version: "2.15.0",
		RefSpan: dep.Span{Start: 40, End: 46}, VersionRef: "jackson.version"}
}

// sharedUp is an update of sharedDep(name).
func sharedUp(name, version string, typ dep.UpdateType, bucket, group string) dep.Update {
	return dep.Update{Dep: sharedDep(name), NewVersion: version, Type: typ, Bucket: bucket, Group: group}
}

func render(ups []dep.Update) []string {
	out := make([]string, len(ups))
	for i, u := range ups {
		out[i] = fmt.Sprintf("%s %s %s bucket=%q group=%q adv=%d", u.Dep.Name, u.NewVersion, u.Type, u.Bucket, u.Group, len(u.Advisories))
	}
	return out
}

// noPolicy is the empty policy of every update for unifyShared.
func noPolicy(int, dep.UpdateType) *config.Policy { return &config.Policy{} }

func TestUnifyShared(t *testing.T) {
	adv := []dep.Advisory{{ID: "GHSA-1"}}
	core, databind, annotations := "jackson-core", "jackson-databind", "jackson-annotations"
	tests := []struct {
		name string
		ups  []dep.Update
		want []string
	}{
		{
			name: "same version",
			ups:  []dep.Update{sharedUp(core, "2.17.0", dep.Minor, "", ""), sharedUp(databind, "2.17.0", dep.Minor, "", ""), sharedUp(annotations, "2.17.0", dep.Minor, "", "")},
			want: []string{
				`jackson-core 2.17.0 minor bucket="" group="jackson.version" adv=0`,
				`jackson-databind 2.17.0 minor bucket="" group="jackson.version" adv=0`,
				`jackson-annotations 2.17.0 minor bucket="" group="jackson.version" adv=0`,
			},
		},
		{
			name: "lowest version wins, type recomputed",
			ups:  []dep.Update{sharedUp(core, "2.17.0", dep.Minor, "", ""), sharedUp(databind, "2.15.3", dep.Patch, "", ""), sharedUp(annotations, "2.17.1", dep.Minor, "", "")},
			want: []string{
				`jackson-core 2.15.3 patch bucket="" group="jackson.version" adv=0`,
				`jackson-databind 2.15.3 patch bucket="" group="jackson.version" adv=0`,
				`jackson-annotations 2.15.3 patch bucket="" group="jackson.version" adv=0`,
			},
		},
		{
			name: "per bucket, majors only some reach are kept",
			ups: []dep.Update{
				sharedUp(core, "2.17.0", dep.Minor, "", ""), sharedUp(core, "3.1.0", dep.Major, "major", ""),
				sharedUp(databind, "2.16.1", dep.Minor, "", ""), sharedUp(databind, "3.0.0", dep.Major, "major", ""),
				sharedUp(annotations, "2.17.0", dep.Minor, "", ""),
			},
			want: []string{
				`jackson-core 2.16.1 minor bucket="" group="jackson.version" adv=0`,
				`jackson-core 3.0.0 major bucket="major" group="jackson.version" adv=0`,
				`jackson-databind 2.16.1 minor bucket="" group="jackson.version" adv=0`,
				`jackson-databind 3.0.0 major bucket="major" group="jackson.version" adv=0`,
				`jackson-annotations 2.16.1 minor bucket="" group="jackson.version" adv=0`,
			},
		},
		{
			name: "policy group kept",
			ups:  []dep.Update{sharedUp(core, "2.17.0", dep.Minor, "", ""), sharedUp(databind, "2.17.0", dep.Minor, "", "jvm"), sharedUp(annotations, "2.17.0", dep.Minor, "", "jvm")},
			want: []string{
				`jackson-core 2.17.0 minor bucket="" group="jvm" adv=0`,
				`jackson-databind 2.17.0 minor bucket="" group="jvm" adv=0`,
				`jackson-annotations 2.17.0 minor bucket="" group="jvm" adv=0`,
			},
		},
		{
			name: "differing policy groups take the first",
			ups:  []dep.Update{sharedUp(core, "2.17.0", dep.Minor, "", "a"), sharedUp(databind, "2.17.0", dep.Minor, "", "b"), sharedUp(annotations, "2.17.0", dep.Minor, "", "")},
			want: []string{
				`jackson-core 2.17.0 minor bucket="" group="a" adv=0`,
				`jackson-databind 2.17.0 minor bucket="" group="a" adv=0`,
				`jackson-annotations 2.17.0 minor bucket="" group="a" adv=0`,
			},
		},
		{
			name: "lowered security update loses its advisories",
			ups: func() []dep.Update {
				sec := sharedUp(core, "2.17.1", dep.Minor, "", "")
				sec.Advisories = adv
				low := sharedUp(databind, "2.16.0", dep.Minor, "", "")
				low.Advisories = adv
				return []dep.Update{sec, low, sharedUp(annotations, "2.17.0", dep.Minor, "", "")}
			}(),
			want: []string{
				`jackson-core 2.16.0 minor bucket="" group="jackson.version" adv=0`,
				`jackson-databind 2.16.0 minor bucket="" group="jackson.version" adv=1`,
				`jackson-annotations 2.16.0 minor bucket="" group="jackson.version" adv=0`,
			},
		},
		{
			name: "one dep's own buckets are left alone",
			ups:  []dep.Update{sharedUp(core, "2.17.0", dep.Minor, "", ""), sharedUp(core, "3.0.0", dep.Major, "major", "")},
			want: []string{
				`jackson-core 2.17.0 minor bucket="" group="" adv=0`,
				`jackson-core 3.0.0 major bucket="major" group="" adv=0`,
			},
		},
		{
			name: "lockfile-only updates do not edit the definition",
			ups: func() []dep.Update {
				lock := sharedUp(databind, "2.15.2", dep.Patch, "", "")
				lock.Lockfile = true
				return []dep.Update{sharedUp(core, "2.17.0", dep.Minor, "", ""), lock}
			}(),
			want: []string{
				`jackson-core 2.17.0 minor bucket="" group="" adv=0`,
				`jackson-databind 2.15.2 patch bucket="" group="" adv=0`,
			},
		},
		{
			name: "other definitions are separate",
			ups: func() []dep.Update {
				other := sharedUp(databind, "2.16.0", dep.Minor, "", "")
				other.Dep.File = "sub/pom.xml"
				return []dep.Update{sharedUp(core, "2.17.0", dep.Minor, "", ""), other}
			}(),
			want: []string{
				`jackson-core 2.17.0 minor bucket="" group="" adv=0`,
				`jackson-databind 2.16.0 minor bucket="" group="" adv=0`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unifyShared(t.Context(), tt.ups, noPolicy)
			if got := render(tt.ups); !slices.Equal(got, tt.want) {
				t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

// Custom deps sharing one version with a checksum each (shareCustom): a lowered member takes
// the checksum of its own update to the lower version, not the other member's.
func TestUnifySharedOwnChecksums(t *testing.T) {
	up := func(sumAt int, version, sum, bucket string) dep.Update {
		return dep.Update{Dep: dep.Dep{Manager: "custom", DepType: "regex", Versioning: "semver", File: ".env", Line: 1, Name: "o/t",
			Version: "1.0.0", RefSpan: dep.Span{Start: 10, End: 15}, ChecksumSpan: dep.Span{Start: sumAt, End: sumAt + 64},
			VersionRef: "o/t", Together: true}, NewVersion: version, NewChecksum: sum, Type: dep.Minor, Bucket: bucket}
	}
	ups := []dep.Update{
		up(20, "1.2.0", "a12", ""), up(20, "1.1.0", "a11", "y"),
		up(100, "1.1.0", "b11", ""), up(100, "1.2.0", "b12", "x"),
	}
	unifyShared(t.Context(), ups, noPolicy)
	var got []string
	for _, u := range ups {
		got = append(got, fmt.Sprintf("%d %s %s %s %s", u.Dep.ChecksumSpan.Start, u.NewVersion, u.NewChecksum, u.Bucket, u.Group))
	}
	want := []string{"20 1.1.0 a11  o/t", "20 1.1.0 a11 y o/t", "100 1.1.0 b11  o/t", "100 1.2.0 b12 x o/t"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestGroupUpdatesShared(t *testing.T) {
	sec := sharedUp("jackson-databind", "2.17.0", dep.Minor, "", "")
	sec.Advisories = []dep.Advisory{{ID: "GHSA-1"}}
	tests := []struct {
		name string
		ups  []dep.Update
		want []string // branch: title (updates)
	}{
		{
			name: "one PR per bucket",
			ups: []dep.Update{
				sharedUp("jackson-core", "2.17.0", dep.Minor, "", ""), sharedUp("jackson-core", "3.0.0", dep.Major, "major", ""),
				sharedUp("jackson-databind", "2.17.0", dep.Minor, "", ""), sharedUp("jackson-databind", "3.0.0", dep.Major, "major", ""),
				sharedUp("jackson-annotations", "2.17.0", dep.Minor, "", ""),
			},
			want: []string{
				"deps/group/jackson.version: chore(deps): update jackson.version to 2.17.0 (3)",
				"deps/group/jackson.version-major: chore(deps): update jackson.version to 3.0.0 (2)",
			},
		},
		{
			name: "policy group",
			ups:  []dep.Update{sharedUp("jackson-core", "2.17.0", dep.Minor, "", "jvm"), sharedUp("jackson-databind", "2.17.0", dep.Minor, "", "")},
			want: []string{"deps/group/jvm: chore(deps): update jvm to 2.17.0 (2)"},
		},
		{
			name: "security member stays in the group",
			ups:  []dep.Update{sharedUp("jackson-core", "2.17.0", dep.Minor, "", ""), sec},
			want: []string{"deps/group/jackson.version: fix(deps): update jackson.version to 2.17.0 [security] (2)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unifyShared(t.Context(), tt.ups, noPolicy)
			var got []string
			for _, g := range GroupUpdates(tt.ups, "") {
				got = append(got, fmt.Sprintf("%s: %s (%d)", g.Branch, g.Title, len(g.Updates)))
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLocalShared(t *testing.T) {
	tests := []struct {
		name string
		ups  []dep.Update
		want []bool // separate
	}{
		{
			name: "non-major kept for every member",
			ups: []dep.Update{
				sharedUp("jackson-core", "2.16.1", dep.Minor, "", ""), sharedUp("jackson-core", "3.0.0", dep.Major, "major", ""),
				sharedUp("jackson-databind", "2.16.1", dep.Minor, "", ""), sharedUp("jackson-databind", "3.0.0", dep.Major, "major", ""),
				sharedUp("jackson-annotations", "2.16.1", dep.Minor, "", ""),
			},
			want: []bool{false, true, false, true, false},
		},
		{
			name: "lowest major kept",
			ups: []dep.Update{
				sharedUp("jackson-core", "3.1.0", dep.Major, "v3", ""), sharedUp("jackson-core", "4.0.0", dep.Major, "v4", ""),
				sharedUp("jackson-databind", "3.1.0", dep.Major, "v3", ""), sharedUp("jackson-annotations", "4.0.0", dep.Major, "v4", ""),
			},
			want: []bool{false, true, false, true},
		},
		{
			name: "newest non-major, whichever member comes first",
			ups: []dep.Update{
				sharedUp("jackson-core", "2.16.1", dep.Minor, "minor", ""), sharedUp("jackson-core", "2.15.3", dep.Patch, "patch", ""),
				sharedUp("jackson-databind", "2.15.3", dep.Patch, "patch", ""), sharedUp("jackson-databind", "2.16.1", dep.Minor, "minor", ""),
			},
			want: []bool{false, true, true, false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local, separate := Local(tt.ups)
			if !slices.Equal(separate, tt.want) {
				t.Errorf("separate = %v, want %v", separate, tt.want)
			}
			if n := strings.Count(fmt.Sprint(tt.want), "false"); len(local) != n {
				t.Errorf("local = %d updates, want %d", len(local), n)
			}
		})
	}
}

// propManager is a stand-in for the maven managers: [each] `<artifactId>` of pom.xml with a
// `${name}` version is a dep whose RefSpan is the `<name>` property value.
type propManager struct{}

var (
	propRe = regexp.MustCompile(`<([a-z.]+)>([0-9.]+)</`)
	artRe  = regexp.MustCompile(`<artifactId>([a-z-]+)</artifactId><version>\$\{([a-z.]+)\}</version>`)
)

func (propManager) Name() string        { return "maven" }
func (propManager) Match(p string) bool { return p == "pom.xml" }
func (propManager) Edits(u dep.Update) ([]patch.Edit, error) {
	return []patch.Edit{{Start: u.Dep.RefSpan.Start, End: u.Dep.RefSpan.End, Old: u.Dep.Version, New: u.NewVersion}}, nil
}

func (propManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	props := map[string]dep.Span{}
	for _, m := range propRe.FindAllSubmatchIndex(src, -1) {
		props[string(src[m[2]:m[3]])] = dep.Span{Start: m[4], End: m[5]}
	}
	var deps []dep.Dep
	for _, m := range artRe.FindAllSubmatchIndex(src, -1) {
		ref := string(src[m[4]:m[5]])
		sp := props[ref]
		deps = append(deps, dep.Dep{Manager: "maven", Datasource: "go", Versioning: "maven", File: file, Line: 1,
			Name: string(src[m[2]:m[3]]), Version: string(src[sp.Start:sp.End]), RefSpan: sp, VersionRef: ref})
	}
	return deps, nil
}

func TestResolveSharedVersionOnePR(t *testing.T) {
	const pom = "<project><properties><jackson.version>2.15.0</jackson.version></properties><dependencies>" +
		"<artifactId>jackson-core</artifactId><version>${jackson.version}</version>" +
		"<artifactId>jackson-databind</artifactId><version>${jackson.version}</version>" +
		"</dependencies></project>"
	root, cfg := setup(t, "", map[string]string{"pom.xml": pom})
	ms := []manager.Manager{propManager{}}
	o := Options{Managers: ms, Datasources: map[string]Datasource{"go": fakeGo{
		"jackson-core":     {"2.15.0", "2.16.0", "2.17.0"},
		"jackson-databind": {"2.15.0", "2.16.0"},
	}}}
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 || len(res.Updates) != 2 {
		t.Fatalf("errors %v, updates %s", res.Errors, render(res.Updates))
	}
	groups := GroupUpdates(res.Updates, "")
	if len(groups) != 1 || groups[0].Branch != "deps/group/jackson.version" || groups[0].Title != "chore(deps): update jackson.version to 2.16.0" || len(groups[0].Updates) != 2 {
		t.Fatalf("groups = %+v", groups)
	}
	local, separate := Local(res.Updates)
	if len(local) != 2 || slices.Contains(separate, true) {
		t.Errorf("local %s, separate %v", render(local), separate)
	}
	changes, errs := Build(t.Context(), root, ms, groups[0].Updates, nil)
	if len(errs) != 0 || len(changes) != 1 {
		t.Fatalf("changes %d, errors %v", len(changes), errs)
	}
	if want := strings.Replace(pom, "2.15.0", "2.16.0", 1); string(changes[0].New) != want {
		t.Errorf("pom.xml =\n%s\nwant\n%s", changes[0].New, want)
	}
}

// TestResolveSharedSecurityLowered: a security update lowered to the version a sibling of its
// shared definition reaches is no security update any more, so it takes the normal policy of
// its new type: schedule, dashboard approval and the type's commit settings.
func TestResolveSharedSecurityLowered(t *testing.T) {
	const pom = "<project><properties><jackson.version>2.15.0</jackson.version></properties><dependencies>" +
		"<artifactId>jackson-core</artifactId><version>${jackson.version}</version>" +
		"<artifactId>jackson-databind</artifactId><version>${jackson.version}</version>" +
		"</dependencies></project>"
	root, cfg := setup(t, "dashboard: {enabled: true}\ndashboardApproval: true\nschedule: ['0 1 * * *']\n"+
		"rules: [{updateTypes: [patch], commitType: build}]\n", map[string]string{"pom.xml": pom})
	o := Options{
		Managers: []manager.Manager{propManager{}},
		Datasources: map[string]Datasource{"go": fakeGo{
			"jackson-core":     {"2.15.0", "2.15.3", "2.16.0", "2.17.1"},
			"jackson-databind": {"2.15.0", "2.15.3"},
		}},
		Security: &fakeSec{vulns: map[string][]Vuln{"jackson-core": {vuln("GHSA-1", "2.17.1")}}},
	}
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil || len(res.Errors) != 0 || len(res.Updates) != 2 {
		t.Fatalf("err %v, errors %v, updates %s", err, res.Errors, render(res.Updates))
	}
	for _, u := range res.Updates {
		if u.NewVersion != "2.15.3" || u.Type != dep.Patch || len(u.Advisories) != 0 || u.SecurityGroup != "" {
			t.Errorf("%s: %s %s advisories %d securityGroup %q, want 2.15.3 patch without advisories", u.Dep.Name, u.NewVersion, u.Type, len(u.Advisories), u.SecurityGroup)
		}
		if u.Schedule == nil || !u.DashboardApproval || u.CommitType != "build" {
			t.Errorf("%s: schedule %v, dashboardApproval %v, commitType %q, want the normal patch policy", u.Dep.Name, u.Schedule, u.DashboardApproval, u.CommitType)
		}
	}
	groups := GroupUpdates(res.Updates, "")
	if len(groups) != 1 || groups[0].Title != "build(deps): update jackson.version to 2.15.3" || !groups[0].DashboardApproval {
		t.Errorf("groups = %+v", groups)
	}
}
