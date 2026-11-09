package jenkins

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func render(ds []dep.Dep) string {
	var b strings.Builder
	for _, d := range ds {
		fmt.Fprintf(&b, "%d %s %s", d.Line, d.Name, d.Version)
		if d.Skip != "" {
			fmt.Fprintf(&b, " skip=%q", d.Skip)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func extract(t *testing.T, file string, src []byte) []dep.Dep {
	t.Helper()
	ds, err := New().Extract(file, src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.Skip == "" {
			if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != d.Version {
				t.Errorf("%s: RefSpan %q, want %q", d.Name, got, d.Version)
			}
		}
		if d.Manager != Name || d.Datasource != Datasource || d.Versioning != Versioning || d.DepType != DepTypePlugin || d.File != file {
			t.Errorf("%s: %+v", d.Name, d)
		}
	}
	return ds
}

func TestMatch(t *testing.T) {
	m := New()
	for p, want := range map[string]bool{
		"plugins.txt": true, "jenkins/plugins.txt": true, "a/b/plugins.yaml": true, "plugins.yml": true,
		"my-plugins.txt": false, "plugins.json": false, "plugins.txt/x": false, "plugins/plugins.ini": false,
	} {
		if m.Match(p) != want {
			t.Errorf("Match(%q) = %v", p, !want)
		}
	}
	if m.Name() != Name {
		t.Errorf("Name = %q", m.Name())
	}
}

// TestExtractFixtures reads librecores/librecores-ci-jenkins-server's plugins.txt, the head of
// base2Services/jenkins-distribution's plugins.yaml, Ableton/ansible-role-jenkins-jcasc's
// molecule plugins.yaml and Renovate's jenkins manager fixtures.
func TestExtractFixtures(t *testing.T) {
	for file, want := range map[string]string{
		"testdata/librecores/plugins.txt": `1 ansicolor 0.5.2
2 matrix-auth 2.3
3 cloudbees-folder 6.6
4 workflow-aggregator 2.6
5 pipeline-utility-steps 2.1.0
6 git 3.9.1
7 timestamper 1.8.10
8 yet-another-docker-plugin 0.1.0-rc48
9 ownership 0.12.1
10 job-restrictions 0.8
11 role-strategy 2.9.0
12 mailer 1.21
13 authorize-project 1.3.0
14 security-inspector 0.5
15 monitoring 1.74.0
16 locale 1.3
17 blueocean 1.9.0
18 filesystem_scm 2.1
19 junit 1.26.1
20 checkstyle 3.50
21 findbugs 4.72
22 description-setter 1.10
23 email-ext 2.63
24 embeddable-build-status 1.9
25 log-parser 2.0
26 periodicbackup 1.5
27 saferestart 0.3
28 sidebar-link 1.9.1
29 simple-theme-plugin 0.5.1
30 ssh-slaves 1.28.1
31 subversion 2.12.1
32 tap 2.2.1
33 ws-cleanup 0.36
34 credentials 2.1.18
35 jdk-tool 1.1
36 plot 2.1.1
`,
		"testdata/base2/plugins.yaml": `4 analysis-model-api 14.18.0-1028.v9c938eeda_ef2
7 ant 520.vd082ecfb_16a_9
10 antisamy-markup-formatter 173.v680e3a_b_69ff3
13 apache-httpcomponents-client-4-api 4.5.14-269.vfa_2321039a_83
16 artifact-manager-s3 1002.v005e958510dd
19 asm-api 9.11.1-226.vff833a_e4c58e
22 audit-trail 472.vb_51e5934ec50
25 authentication-tokens 1.144.v5ff4a_5ec5c33
28 aws-credentials 265.v8422a_f384cd9
`,
		"testdata/ableton/plugins.yaml": `3 configuration-as-code  skip="no version"
6 sidebar-link 2.5.0
`,
		"testdata/renovate/plugins.txt": `1 email-ext 1.2.3
3 apache-httpcomponents-client-4-api 4.4.10-2.0
4 authentication-tokens 1.2
5 blueocean 1.21.0
11 git 4.2.0 skip="ignored"
12 git-client 3.3.1 skip="ignored"
`,
		"testdata/renovate/plugins.yaml": `4 git latest skip="unsupported version"
7 job-import-plugin 2.10
10 invalid-version-plugin 2.10 skip="invalid version"
13 ignore-plugin 2.10 skip="ignored"
16 docker  skip="no version"
19 cloudbees-bitbucket-branch-source experimental skip="unsupported version"
20 script-security  skip="download url"
26 workflow-step-api 2.19-rc289.d09828a05a74 skip="unsupported version"
`,
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := render(extract(t, file, src)); got != want {
			t.Errorf("%s: deps =\n%s\nwant\n%s", file, got, want)
		}
	}
}

func TestExtractText(t *testing.T) {
	src := []byte("# comment\r\n" +
		"  git:5.10.1\r\n" +
		"latest-plugin:latest\n" +
		"script-security:1.56:http://ftp-chi.osuosl.org/pub/jenkins/plugins/script-security/1.56/script-security.hpi\n" +
		"direct:https://example.com/direct.hpi\n" +
		"workflow-step-api:incrementals;org.jenkins-ci.plugins.workflow;2.19-rc289.d09828a05a74\n" +
		"bare-name\n" +
		"kept:1.0 # renovate: other\n" +
		"pyup:2.0 #pyup: ignore # because\n" +
		"last:3.0")
	want := `2 git 5.10.1
3 latest-plugin latest skip="unsupported version"
4 script-security 1.56 skip="download url"
5 direct  skip="download url"
6 workflow-step-api incrementals;org.jenkins-ci.plugins.workflow;2.19-rc289.d09828a05a74 skip="unsupported version"
8 kept 1.0
9 pyup 2.0 skip="ignored"
10 last 3.0
`
	if got := render(extract(t, "ci/plugins.txt", src)); got != want {
		t.Errorf("deps =\n%s\nwant\n%s", got, want)
	}
}

func TestExtractYAML(t *testing.T) {
	src := []byte(`plugins:
  - artifactId: plain
    source:
      version: 2.5.0
  - artifactId: double
    source: {version: "1.2"}
  - artifactId: int
    source:
      version: 2
  - artifactId: null-version
    source:
      version:
  - artifactId: escaped
    source:
      version: "1.\u0030"
  - artifactId: block
    source:
      version: >-
        1.0
  - artifactId: ignored-upper
    source:
      version: '1.0'
    renovate:
      ignore: True
  - artifactId: ""
  - name: no-artifact-id
  - artifactId: [list]
`)
	want := `4 plain 2.5.0
6 double 1.2
9 int 2 skip="invalid version"
10 null-version  skip="no version"
15 escaped 1.0 skip="cannot locate value"
18 block 1.0 skip="cannot locate value"
22 ignored-upper 1.0 skip="ignored"
`
	ds := extract(t, "plugins.yaml", src)
	if got := render(ds); got != want {
		t.Errorf("deps =\n%s\nwant\n%s", got, want)
	}
	if ds[0].InsertAt != ds[0].RefSpan.Start || ds[1].InsertAt != 0 {
		t.Errorf("plain InsertAt = %d, quoted %d", ds[0].InsertAt, ds[1].InsertAt)
	}
	for _, src := range []string{"", "plugins: []\n", "# only a comment with artifactId\n", "artifactId: x\n", "plugins: {artifactId: x}\n"} {
		if ds := extract(t, "plugins.yml", []byte(src)); len(ds) != 0 {
			t.Errorf("%q: deps %v", src, ds)
		}
	}
	if _, err := New().Extract("plugins.yaml", []byte("plugins: [artifactId\n"), manager.Env{}); err == nil {
		t.Error("invalid YAML without error")
	}
}

func TestEdits(t *testing.T) {
	src := []byte("plugins:\n  - artifactId: a\n    source:\n      version: 2.5.0\n  - artifactId: b\n    source:\n      version: '2.5'\n")
	ds := extract(t, "plugins.yaml", src)
	txt := []byte("git:5.10.1 # pinned\n")
	ds = append(ds, extract(t, "plugins.txt", txt)...)
	for _, tt := range []struct {
		d    dep.Dep
		src  []byte
		to   string
		want string
	}{
		{ds[0], src, "2.6.0", "      version: 2.6.0\n"},
		{ds[0], src, "2.10", "      version: '2.10'\n"},
		{ds[0], src, "608.v67378e9d3db_1", "      version: 608.v67378e9d3db_1\n"},
		{ds[1], src, "2.10", "      version: '2.10'\n"},
		{ds[2], txt, "5.11.0", "git:5.11.0 # pinned\n"},
	} {
		edits, err := New().Edits(dep.Update{Dep: tt.d, NewVersion: tt.to})
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply(tt.src, edits)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), tt.want) {
			t.Errorf("%s → %s:\n%s\nwant line %q", tt.d.Name, tt.to, out, tt.want)
		}
	}
	if _, err := New().Edits(dep.Update{Dep: ds[0], NewVersion: "2.6.0", NewDigest: "sha256:x"}); err == nil {
		t.Error("digest pin without error")
	}
}

func TestPlainString(t *testing.T) {
	for v, want := range map[string]bool{
		"2.5.0": true, "608.v67378e9d3db_1": true, "1.0-rc1": true,
		"2.10": false, "2": false, "1e3": false, "true": false, "null": false, "": false, "a: b": false,
	} {
		if got := plainString(v); got != want {
			t.Errorf("plainString(%q) = %v", v, got)
		}
	}
}
