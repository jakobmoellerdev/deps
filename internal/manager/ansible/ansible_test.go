package ansible

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

func extract(t *testing.T, m manager.Manager, file string, src []byte) []dep.Dep {
	t.Helper()
	deps, err := m.Extract(file, src, manager.Env{YAML: &yamlspan.Parsed{}})
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range deps {
		if d.Manager != m.Name() || d.File != file {
			t.Errorf("%d: manager %s file %s", i, d.Manager, d.File)
		}
		if d.Skip == "" {
			want := d.Version
			if d.Range != "" {
				want = d.Range
			}
			if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != want {
				t.Errorf("%d: src[RefSpan] = %q, want %q", i, got, want)
			}
		}
	}
	return deps
}

// render prints one line per dep: line, dep type, datasource, name, version, then the range,
// source and skip when set.
func render(deps []dep.Dep) string {
	var b strings.Builder
	for _, d := range deps {
		fmt.Fprintf(&b, "%d %s %s %s %s", d.Line, d.DepType, d.Datasource, d.Name, d.Version)
		if d.Range != "" && d.Range != d.Version {
			fmt.Fprintf(&b, " range=%q", d.Range)
		}
		if d.Source != "" {
			fmt.Fprintf(&b, " source=%s", d.Source)
		}
		if d.Skip != "" {
			fmt.Fprintf(&b, " skip=%q", d.Skip)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMatch(t *testing.T) {
	g, a := NewGalaxy(), New()
	for path, want := range map[string][2]bool{
		"requirements.yml":                   {true, false},
		"requirements.yaml":                  {true, false},
		"collections/requirements.yml":       {true, false},
		"roles/requirements.ansible.yaml":    {true, false},
		"galaxy.yml":                         {true, false},
		"x/galaxy.ansible.yml":               {true, false},
		"requirements.txt":                   {false, false},
		"dev-requirements.yml":               {false, false},
		"roles/web/tasks/main.yml":           {false, true},
		"tasks/install.yaml":                 {false, true},
		"tasks/sub/install.yaml":             {false, false},
		"roles/web/handlers/main.yml":        {false, false},
		"mytasks/main.yml":                   {false, false},
		"roles/web/tasks/main.yml.bak":       {false, false},
		"playbooks/requirements.ansible.yml": {true, false},
	} {
		if got := g.Match(path); got != want[0] {
			t.Errorf("ansible-galaxy Match(%q) = %v", path, got)
		}
		if got := a.Match(path); got != want[1] {
			t.Errorf("ansible Match(%q) = %v", path, got)
		}
	}
}

func TestExtractGitCollections(t *testing.T) {
	// openstack-k8s-operators/ci-framework requirements.yml
	src := fixture(t, "ci-framework-requirements.yml")
	got := render(extract(t, NewGalaxy(), "requirements.yml", src))
	want := `20 galaxy-collection git-tags ansible-collections/ansible.posix 2.0.0 source=https://github.com/ansible-collections/ansible.posix
23 galaxy-collection git-tags ansible-collections/ansible.utils v6.0.0 source=https://github.com/ansible-collections/ansible.utils
26 galaxy-collection git-tags ansible-collections/community.general 10.6.0 source=https://github.com/ansible-collections/community.general
29 galaxy-collection git-tags ansible-collections/community.crypto 2.26.1 source=https://github.com/ansible-collections/community.crypto
32 galaxy-collection git-tags containers/ansible-podman-collections 1.16.3 source=https://github.com/containers/ansible-podman-collections
35 galaxy-collection git-tags ansible-collections/community.libvirt 1.3.1 source=https://github.com/ansible-collections/community.libvirt
38 galaxy-collection git-tags ansible-collections/kubernetes.core 5.2.0 source=https://github.com/ansible-collections/kubernetes.core
41 galaxy-collection git-tags ansible-collections/ansible.netcommon v8.0.0 source=https://github.com/ansible-collections/ansible.netcommon
44 galaxy-collection git-tags openstack/ansible-config_template 2.1.1 source=https://github.com/openstack/ansible-config_template
47 galaxy-collection git-tags ansible-collections/junipernetworks.junos v10.0.0 source=https://github.com/ansible-collections/junipernetworks.junos
50 galaxy-collection git-tags ansible-collections/cisco.ios v10.0.0 source=https://github.com/ansible-collections/cisco.ios
51 galaxy-collection git-tags ansible-collections/mellanox.onyx  source=https://github.com/ansible-collections/mellanox.onyx skip="no version"
55 galaxy-collection git-tags openshift/community.okd 4.0.1 source=https://github.com/openshift/community.okd
58 galaxy-collection git-tags ovirt/ovirt-ansible-collection 3.2.0-1 source=https://github.com/ovirt/ovirt-ansible-collection skip="non-version ref"
`
	if got != want {
		t.Errorf("deps =\n%s\nwant\n%s", got, want)
	}
}

func TestExtractRolesAndRanges(t *testing.T) {
	for _, tc := range []struct{ fixture, want string }{
		// kencx/homelab: a Galaxy role, a git collection tracking HEAD, a collection without version
		{"homelab-requirements.yml", `3 role galaxy geerlingguy.docker 7.0.1
7 galaxy-collection git-tags jhuesser/ansible-collection HEAD source=https://github.com/jhuesser/ansible-collection skip="non-version ref"
8 galaxy-collection galaxy-collection community.docker  skip="no version"
`},
		// ceph/teuthology: bare names and upper bounds
		{"teuthology-requirements.yml", `3 galaxy-collection galaxy-collection amazon.aws  skip="no version"
5 galaxy-collection galaxy-collection ansible.netcommon 6.0.0 range="<6.0.0"
6 galaxy-collection galaxy-collection ansible.posix  skip="no version"
8 galaxy-collection galaxy-collection ansible.utils 3.0.0 range="<3.0.0"
9 galaxy-collection galaxy-collection community.docker  skip="no version"
10 galaxy-collection galaxy-collection community.general  skip="no version"
11 galaxy-collection galaxy-collection community.postgresql  skip="no version"
`},
		// kubernetes-sigs/kubespray galaxy.yml
		{"kubespray-galaxy.yml", `16 galaxy-collection galaxy-collection ansible.utils 2.5.0 range=">=2.5.0"
17 galaxy-collection galaxy-collection community.crypto 2.22.3 range=">=2.22.3"
18 galaxy-collection galaxy-collection community.general 7.0.0 range=">=7.0.0"
19 galaxy-collection galaxy-collection ansible.netcommon 5.3.0 range=">=5.3.0"
20 galaxy-collection galaxy-collection ansible.posix 1.5.4 range=">=1.5.4"
21 galaxy-collection galaxy-collection community.docker 3.11.0 range=">=3.11.0"
22 galaxy-collection galaxy-collection kubernetes.core 2.4.2 range=">=2.4.2"
`},
	} {
		name := "requirements.yml"
		if strings.Contains(tc.fixture, "galaxy") {
			name = "galaxy.yml"
		}
		got := render(extract(t, NewGalaxy(), name, fixture(t, tc.fixture)))
		if got != tc.want {
			t.Errorf("%s: deps =\n%s\nwant\n%s", tc.fixture, got, tc.want)
		}
	}
}

func TestExtractForms(t *testing.T) {
	src := `# old format: a list of roles
- src: geerlingguy.java
  version: "2.6.0"
- name: nginx
  src: https://github.com/bennojoy/nginx
  version: v1.0.0
- git+https://github.com/geerlingguy/ansible-role-apache.git,3.2.0
- src: git@gitlab.example.com:ops/role-x.git
  scm: git
  version: 1.4.0
- src: https://example.com/files/role.tar.gz
  name: tarball
- src: hg+https://hg.example.com/role
  scm: hg
  version: 1.0.0
- name: geerlingguy.docker
  version: master
- name: localrole
  version: 1.0.0
- src: geerlingguy.nodejs
- include: other.yml
- src: geerlingguy.pip
  version: |
    1.0.0
`
	got := render(extract(t, NewGalaxy(), "roles/requirements.yml", []byte(src)))
	want := `3 role galaxy geerlingguy.java 2.6.0
6 role git-tags bennojoy/nginx v1.0.0 source=https://github.com/bennojoy/nginx
7 role git-tags geerlingguy/ansible-role-apache 3.2.0 source=https://github.com/geerlingguy/ansible-role-apache
10 role git-tags ops/role-x 1.4.0 source=https://gitlab.example.com/ops/role-x
11 role galaxy https://example.com/files/role.tar.gz  skip="unsupported source"
13 role galaxy hg+https://hg.example.com/role  skip="unsupported source"
17 role galaxy geerlingguy.docker master skip="non-version ref"
18 role galaxy localrole  skip="unsupported source"
20 role galaxy geerlingguy.nodejs  skip="no version"
23 role galaxy geerlingguy.pip 1.0.0 skip="cannot locate value"
`
	if got != want {
		t.Errorf("deps =\n%s\nwant\n%s", got, want)
	}

	src = `---
roles:
  - name: geerlingguy.java
    version: '2.6.0' # pinned
collections:
  - name: community.general
    version: "==10.1.0"
  - name: ansible.posix
    version: 1.6.2
    source: https://hub.example.com/api/galaxy/content/community/
  - name: community.docker
    version: ">=3.0.0,<4.0.0"
  - name: community.crypto
    version: "*"
  - name: ./local/collection
    type: dir
  - name: https://example.com/c-1.0.0.tar.gz
    type: url
  - name: git+https://github.com/ansible-collections/amazon.aws.git,8.1.0
  - name: git@github.com:o/r.git
    type: git
    version: main
  - version: 1.0.0
  - name: notacollection
    version: 1.0.0
  - name: /abs/path
`
	got = render(extract(t, NewGalaxy(), "requirements.yml", []byte(src)))
	want = `4 role galaxy geerlingguy.java 2.6.0
7 galaxy-collection galaxy-collection community.general 10.1.0 range="==10.1.0"
9 galaxy-collection galaxy-collection ansible.posix 1.6.2 source=https://hub.example.com/api/galaxy/content/community/
12 galaxy-collection galaxy-collection community.docker 3.0.0 range=">=3.0.0,<4.0.0"
14 galaxy-collection galaxy-collection community.crypto * skip="unconstrained"
15 galaxy-collection galaxy-collection ./local/collection  skip="local source"
17 galaxy-collection galaxy-collection https://example.com/c-1.0.0.tar.gz  skip="unsupported source"
19 galaxy-collection git-tags ansible-collections/amazon.aws 8.1.0 source=https://github.com/ansible-collections/amazon.aws
22 galaxy-collection git-tags o/r main source=https://github.com/o/r skip="non-version ref"
23 galaxy-collection galaxy-collection   skip="no name"
24 galaxy-collection galaxy-collection notacollection  skip="no name"
26 galaxy-collection galaxy-collection /abs/path  skip="local source"
`
	if got != want {
		t.Errorf("deps =\n%s\nwant\n%s", got, want)
	}
}

func TestExtractEmptyAndInvalid(t *testing.T) {
	g := NewGalaxy()
	for _, src := range []string{"", "---\n", "foo: bar\n", "dependencies: {}\n", "roles: 3\n"} {
		if deps := extract(t, g, "requirements.yml", []byte(src)); len(deps) != 0 {
			t.Errorf("%q: %+v", src, deps)
		}
	}
	if deps := extract(t, g, "galaxy.yml", []byte("namespace: a\nname: b\ndependencies:\n  a.b: {x: 1}\n")); render(deps) != "4 galaxy-collection galaxy-collection a.b  skip=\"no version\"\n" {
		t.Errorf("galaxy.yml mapping value: %s", render(deps))
	}
	if _, err := g.Extract("requirements.yml", []byte("roles: [\n"), manager.Env{}); err == nil {
		t.Error("want a YAML error")
	}
	if _, err := New().Extract("tasks/main.yml", []byte("- image: [\n"), manager.Env{}); err == nil {
		t.Error("want a YAML error")
	}
}

func TestExtractGitMetadataAndTaskFiles(t *testing.T) {
	// openstack-k8s-operators/ci-framework galaxy.yml: SCM URL keys with git versions
	got := render(extract(t, NewGalaxy(), "galaxy.yml", fixture(t, "ci-framework-galaxy.yml")))
	for _, line := range []string{
		"74 galaxy-collection git-tags containers/ansible-podman-collections 1.16.2 source=https://github.com/containers/ansible-podman-collections\n",
		"77 galaxy-collection git-tags ansible-collections/ansible.utils v5.1.2 source=https://github.com/ansible-collections/ansible.utils\n",
		"85 galaxy-collection git-tags ansible-collections/mellanox.onyx master source=https://github.com/ansible-collections/mellanox.onyx skip=\"non-version ref\"\n",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("missing %q in\n%s", line, got)
		}
	}
	if n := strings.Count(got, "\n"); n != 14 {
		t.Errorf("%d deps:\n%s", n, got)
	}
	got = render(extract(t, NewGalaxy(), "galaxy.yml", []byte("dependencies:\n  ./local: '1.0.0'\n")))
	if got != "2 galaxy-collection galaxy-collection ./local  skip=\"no name\"\n" {
		t.Errorf("local key: %s", got)
	}
	// a task file named requirements.yml (ci-framework roles/cifmw_external_dns/tasks)
	if deps := extract(t, NewGalaxy(), "roles/x/tasks/requirements.yml", fixture(t, "ci-framework-tasks-requirements.yml")); len(deps) != 0 {
		t.Errorf("task file: %s", render(deps))
	}
}

func TestExtractEdgeForms(t *testing.T) {
	src := `roles:
  - geerlingguy.java,2.6.0
  - "geerlingguy.git,\x31.0.0"
  - - nested
  - ""
  - git+https://example.com,1.0.0
  - src: ssh://git@git.example.com/ops/role.git
    version: 2.0.0
  - src: http://example.com/role.zip
    name: zipped
  - src: git@github.com:o/r.git
collections:
  - name: community.general
    source: git@github.com:o/r.git
    version: 1.0.0
  - name: community.docker
    version: ""
  - name: community.crypto
    version: "\x31.0.0"
  - community.okd
`
	got := render(extract(t, NewGalaxy(), "requirements.yml", []byte(src)))
	want := `2 role galaxy geerlingguy.java 2.6.0
3 role galaxy geerlingguy.git 1.0.0 skip="cannot locate value"
5 role galaxy   skip="no name"
6 role git-tags git+https://example.com  skip="unsupported source"
8 role git-tags ops/role 2.0.0 source=https://git.example.com/ops/role
9 role galaxy http://example.com/role.zip  skip="unsupported source"
11 role git-tags o/r  source=https://github.com/o/r skip="no version"
13 galaxy-collection galaxy-collection community.general  source=git@github.com:o/r.git skip="unsupported source"
17 galaxy-collection galaxy-collection community.docker  skip="no version"
19 galaxy-collection galaxy-collection community.crypto  skip="cannot locate value"
20 galaxy-collection galaxy-collection community.okd  skip="no version"
`
	if got != want {
		t.Errorf("deps =\n%s\nwant\n%s", got, want)
	}
}

func TestEdits(t *testing.T) {
	src := []byte(`roles:
  - src: geerlingguy.java
    version: "2.6.0"
  - git+https://github.com/geerlingguy/ansible-role-apache.git,3.2.0
collections:
  - name: community.general
    version: "==10.1.0"
  - name: community.docker
    version: ">=3.0.0,<4.0.0"
  - name: ansible.posix
    version: 1.6.2
`)
	g := NewGalaxy()
	deps := extract(t, g, "requirements.yml", src)
	var edits []patch.Edit
	for i, v := range []string{"2.6.1", "3.3.0", "11.0.0", "5.1.0", "2.0.0"} {
		e, err := g.Edits(dep.Update{Dep: deps[i], NewVersion: v, RangeStrategy: "replace"})
		if err != nil {
			t.Fatalf("%s: %v", deps[i].Name, err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	want := `roles:
  - src: geerlingguy.java
    version: "2.6.1"
  - git+https://github.com/geerlingguy/ansible-role-apache.git,3.3.0
collections:
  - name: community.general
    version: "==11.0.0"
  - name: community.docker
    version: ">=5.1.0,<5.2.0"
  - name: ansible.posix
    version: 2.0.0
`
	if string(out) != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
	if _, err := g.Edits(dep.Update{Dep: deps[0], NewVersion: "2.6.1", NewDigest: "abc"}); err == nil {
		t.Error("digest pin: want an error")
	}
	rr := g.(manager.RangeUpdater)
	if !rr.InRange(deps[3], "3.5.0") || rr.InRange(deps[3], "4.0.0") {
		t.Error("InRange")
	}
	if r, ok := rr.NewRange(deps[3], "4.1.0", "widen"); !ok || r != ">=3.0.0,<4.2.0" {
		t.Errorf("NewRange widen = %q %v", r, ok)
	}
}

func TestExtractTasks(t *testing.T) {
	m := New()
	got := render(extract(t, m, "roles/traefik/tasks/main.yml", fixture(t, "traefik-tasks-main.yml")))
	if want := "28 image docker traefik 2.2 source=docker.io/library/traefik\n"; got != want {
		t.Errorf("traefik: %s", got)
	}
	got = render(extract(t, m, "roles/mailpit/tasks/main.yml", fixture(t, "mailpit-tasks-main.yml")))
	if want := "26 image docker {{ mailpit_image }}  skip=\"template\"\n"; got != want {
		t.Errorf("mailpit: %s", got)
	}
	src := []byte(`- name: nested
  block:
    - containers.podman.podman_container:
        name: db
        image: "docker.io/library/postgres:17.2@sha256:` + strings.Repeat("a", 64) + `"
    - kubernetes.core.k8s:
        definition:
          spec:
            containers:
              - image: ghcr.io/o/app:${TAG}
    - ec2_instance:
        image:
          id: ami-123
`)
	got = render(extract(t, m, "tasks/main.yml", src))
	want := "5 image docker postgres 17.2 source=docker.io/library/postgres\n10 image docker ghcr.io/o/app:${TAG}  skip=\"variable reference\"\n"
	if got != want {
		t.Errorf("nested:\n%s\nwant\n%s", got, want)
	}
	if deps := extract(t, m, "tasks/main.yml", []byte("- debug: {msg: hi}\n")); len(deps) != 0 {
		t.Errorf("no image: %+v", deps)
	}
	deps := extract(t, m, "tasks/main.yml", fixture(t, "traefik-tasks-main.yml"))
	edits, err := m.Edits(dep.Update{Dep: deps[0], NewVersion: "3.2"})
	if err != nil || len(edits) != 1 || edits[0].New != "3.2" {
		t.Errorf("Edits = %+v, %v", edits, err)
	}
	if r, err := m.(manager.Renamer).Rename(deps[0], "docker.io/library/traefik"); err != nil || r.Source != "docker.io/library/traefik" {
		t.Errorf("Rename = %+v, %v", r, err)
	}
}
