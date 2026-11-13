package osv

import (
	"encoding/json/v2"
	"reflect"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestDistroTarget(t *testing.T) {
	tests := []struct {
		name, ds, source string
		wantEco, stream  string // wantEco "" = not queried
	}{
		{"debian", "deb", "https://deb.debian.org/debian?suite=bookworm&components=main&binaryArch=amd64", "Debian:12", ""},
		{"debian security", "deb", "https://security.debian.org/debian-security?suite=bookworm-security&components=main&binaryArch=amd64", "Debian:12", ""},
		{"debian release alias", "deb", "https://deb.debian.org/debian?release=trixie&components=main&binaryArch=amd64", "Debian:13", ""},
		{"debian stable", "deb", "https://deb.debian.org/debian?suite=stable&components=main&binaryArch=amd64", "", ""},
		{"default registry", "deb", "", "", ""},
		{"second url decides", "deb", "https://example.com/repo?suite=stable&components=main&binaryArch=amd64 https://deb.debian.org/debian?suite=bullseye-updates&components=main&binaryArch=amd64", "Debian:11", ""},
		{"ubuntu lts", "deb", "http://archive.ubuntu.com/ubuntu?suite=jammy-updates&components=main&binaryArch=amd64", "Ubuntu:22.04:LTS", ""},
		{"ubuntu interim", "deb", "http://archive.ubuntu.com/ubuntu?suite=oracular&components=main&binaryArch=amd64", "Ubuntu:24.10", ""},
		{"ubuntu pro", "deb", "https://esm.ubuntu.com/infra/ubuntu?suite=focal-infra-security&components=main&binaryArch=amd64", "Ubuntu:Pro:20.04:LTS", ""},
		{"bad url", "deb", "http://[::1", "", ""},
		{"alpine branch", "apk", "https://dl-cdn.alpinelinux.org/alpine?branch=v3.20&components=main,community&arch=x86_64", "Alpine:v3.20", ""},
		{"alpine edge", "apk", "https://dl-cdn.alpinelinux.org/alpine?branch=edge&components=main&arch=x86_64", "", ""},
		{"alpine path", "apk", "https://dl-cdn.alpinelinux.org/alpine/v3.22/main/x86_64/APKINDEX.tar.gz", "Alpine:v3.22", ""},
		{"alpine no branch", "apk", "https://example.com/apk/main/x86_64", "", ""},
		{"wolfi", "apk", "https://packages.wolfi.dev/os?arch=x86_64", "Wolfi", ""},
		{"chainguard", "apk", "https://packages.cgr.dev/os?arch=x86_64", "Chainguard", ""},
		{"rocky", "rpm", "https://dl.rockylinux.org/pub/rocky/9/BaseOS/x86_64/os/", "Rocky Linux:9", ""},
		{"rocky minor", "rpm", "https://dl.rockylinux.org/vault/rocky/8.9/AppStream/x86_64/os/", "Rocky Linux:8", ""},
		{"alma mirror", "rpm", "https://mirror.example.org/almalinux/9.4/BaseOS/x86_64/os/repodata/", "AlmaLinux:9", ""},
		{"rocky without version", "rpm", "https://dl.rockylinux.org/pub/rocky/", "", ""},
		{"leap", "rpm", "https://download.opensuse.org/update/leap/15.6/oss/", "openSUSE:Leap 15.6", ""},
		{"rhel", "rpm", "https://cdn.redhat.com/content/dist/rhel9/9/x86_64/baseos/os", "Red Hat", "Red Hat:enterprise_linux:9:"},
		{"ubi", "rpm", "https://cdn-ubi.redhat.com/content/public/ubi/dist/ubi8/8/x86_64/appstream/os", "Red Hat", "Red Hat:enterprise_linux:8:"},
		{"fedora", "rpm", "https://dl.fedoraproject.org/pub/fedora/linux/releases/41/Everything/x86_64/os/", "", ""},
		{"leap 16", "rpm", "https://download.opensuse.org/update/leap/16.0/oss/", "openSUSE:Leap 16.0", ""},
		{"tumbleweed", "rpm", "https://download.opensuse.org/tumbleweed/repo/oss/", "openSUSE:Tumbleweed", ""},
		{"azure linux", "rpm", "https://packages.microsoft.com/azurelinux/3.0/prod/base/x86_64/", "Azure Linux:3", ""},
		{"cbl-mariner", "rpm", "https://packages.microsoft.com/cbl-mariner/2.0/prod/base/x86_64/", "Azure Linux:2", ""},
		{"openeuler", "rpm", "https://repo.openeuler.org/openEuler-24.03-LTS-SP1/OS/x86_64/", "openEuler:24.03-LTS-SP1", ""},
		{"mageia", "rpm", "https://mirror.init7.net/mageia/distrib/9/x86_64/media/core/updates/", "Mageia:9", ""},
		{"sles server", "rpm", "https://updates.suse.com/SUSE/Updates/SLE-Server/12-SP5/x86_64/update/", "SUSE", "SUSE:Linux Enterprise Server 12 SP5"},
		{"sles module", "rpm", "https://updates.suse.com/SUSE/Updates/SLE-Module-Basesystem/15-SP6/x86_64/update/", "SUSE", "SUSE:Linux Enterprise Module for Basesystem 15 SP6"},
		{"sle micro", "rpm", "https://updates.suse.com/SUSE/Updates/SLE-Micro/5.5/x86_64/update/", "SUSE", "SUSE:Linux Enterprise Micro 5.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tg := targetOf(dep.Dep{Manager: "custom.regex", Datasource: tt.ds, Name: "curl", Version: "1.0-1", Source: tt.source})
			if tt.wantEco == "" {
				if tg != nil {
					t.Fatalf("target = %+v, want none", tg.qs)
				}
				return
			}
			if tg == nil {
				t.Fatal("no target")
			}
			want := query{Package: pkg{Name: "curl", Ecosystem: tt.wantEco}, Version: "1.0-1"}
			if len(tg.qs) != 1 || tg.qs[0] != want || !tg.distro || tg.stream != tt.stream {
				t.Errorf("target = %+v distro=%v stream=%q, want %+v stream=%q", tg.qs, tg.distro, tg.stream, want, tt.stream)
			}
		})
	}
	if tg := targetOf(dep.Dep{Datasource: "deb", Name: "curl", Source: "https://deb.debian.org/debian?suite=bookworm"}); tg != nil {
		t.Errorf("unversioned dep queried: %+v", tg.qs)
	}
}

// The advisories are trimmed copies of the real OSV records (fetched 2026-11-09).
const (
	debianCurl = `{"id":"DEBIAN-CVE-2024-11053","details":"When asked to both use a .netrc file for credentials and to follow HTTP redirects",
		"references":[{"type":"ADVISORY","url":"https://security-tracker.debian.org/tracker/CVE-2024-11053"}],
		"affected":[{"package":{"name":"curl","ecosystem":"Debian:12"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"7.88.1-10+deb12u10"}]}]},
		            {"package":{"name":"curl","ecosystem":"Debian:13"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"8.11.1-1"}]}]}]}`
	alpineCurl = `{"id":"ALPINE-CVE-2024-11053","details":"When asked to both use a .netrc file for credentials and to follow HTTP\nredirects",
		"references":[{"type":"ADVISORY","url":"https://security.alpinelinux.org/vuln/CVE-2024-11053"}],
		"affected":[{"package":{"name":"curl","ecosystem":"Alpine:v3.19"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"7.76.0"},{"fixed":"8.11.1-r0"}]}]},
		            {"package":{"name":"curl","ecosystem":"Alpine:v3.20"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"7.76.0"},{"fixed":"8.11.1-r0"}]}]}]}`
	rockyCurl = `{"id":"RLSA-2026:1350","summary":"Moderate: curl security update",
		"references":[{"type":"ADVISORY","url":"https://errata.rockylinux.org/RLSA-2026:1350"}],
		"affected":[{"package":{"name":"curl","ecosystem":"Rocky Linux:9"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"0:7.76.1-35.el9_7.3"}]}]}]}`
	rhelCurl = `{"id":"RHSA-2023:6745","summary":"Red Hat Security Advisory: curl security update",
		"references":[{"type":"ADVISORY","url":"https://access.redhat.com/errata/RHSA-2023:6745"}],
		"affected":[{"package":{"name":"curl-debuginfo","ecosystem":"Red Hat:enterprise_linux:9::appstream"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"0:7.76.1-26.el9_3.2"}]}]},
		            {"package":{"name":"curl","ecosystem":"Red Hat:enterprise_linux:9::baseos"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"0:7.76.1-26.el9_3.2"}]}]}]}`
	// An advisory of another release's stream: OSV matches a "Red Hat" query against it.
	rhel8Curl = `{"id":"RHSA-2024:0001","summary":"curl on RHEL 8",
		"affected":[{"package":{"name":"curl","ecosystem":"Red Hat:enterprise_linux:8::baseos"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"0:7.61.1-34.el8_9.2"}]}]},
		            {"package":{"name":"curl","ecosystem":"Red Hat:enterprise_linux:9::baseos"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"7.76.1-30.el9"},{"last_affected":"7.76.1-31.el9"}]},{"type":"GIT","events":[{"fixed":"abc"}]}]}]}`
)

func TestAdvisoriesDistro(t *testing.T) {
	f := newFake(t)
	f.results["Debian:12|curl|7.88.1-10+deb12u5|"] = result{ids: []string{"DEBIAN-CVE-2024-11053"}}
	f.results["Alpine:v3.20|curl|8.9.0-r0|"] = result{ids: []string{"ALPINE-CVE-2024-11053"}}
	f.results["Rocky Linux:9|curl|7.76.1-26.el9|"] = result{ids: []string{"RLSA-2026:1350"}}
	f.results["Red Hat|curl|7.76.1-26.el9|"] = result{ids: []string{"RHSA-2023:6745", "RHSA-2024:0001"}}
	f.results["Red Hat|curl|7.76.1-26.el9_3.2|"] = result{ids: []string{"RHSA-2023:6745", "RHSA-2024:0001"}}
	f.vulns["DEBIAN-CVE-2024-11053"] = debianCurl
	f.vulns["ALPINE-CVE-2024-11053"] = alpineCurl
	f.vulns["RLSA-2026:1350"] = rockyCurl
	f.vulns["RHSA-2023:6745"] = rhelCurl
	f.vulns["RHSA-2024:0001"] = rhel8Curl
	s := f.start(nil)
	const rhel9 = "https://cdn.redhat.com/content/dist/rhel9/9/x86_64/baseos/os"
	deps := []dep.Dep{
		{Manager: "custom.regex", Datasource: "deb", Name: "curl", Version: "7.88.1-10+deb12u5", Source: "https://deb.debian.org/debian?suite=bookworm&components=main&binaryArch=amd64"},
		{Manager: "custom.regex", Datasource: "apk", Name: "curl", Version: "8.9.0-r0", Source: "https://dl-cdn.alpinelinux.org/alpine?branch=v3.20&components=main&arch=x86_64"},
		{Manager: "custom.regex", Datasource: "rpm", Name: "curl", Version: "7.76.1-26.el9", Source: "https://dl.rockylinux.org/pub/rocky/9/BaseOS/x86_64/os/"},
		{Manager: "custom.regex", Datasource: "rpm", Name: "curl", Version: "7.76.1-26.el9", Source: rhel9},
		{Manager: "custom.regex", Datasource: "rpm", Name: "curl", Version: "7.76.1-26.el9_3.2", Source: rhel9},
		{Manager: "custom.regex", Datasource: "deb", Name: "curl", Version: "7.88.1-10", Source: "https://deb.debian.org/debian?suite=sid&components=main&binaryArch=amd64"},
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]datasource.Vuln{
		{{Advisory: dep.Advisory{ID: "DEBIAN-CVE-2024-11053", Summary: "When asked to both use a .netrc file for credentials and to follow HTTP redirects", URL: "https://security-tracker.debian.org/tracker/CVE-2024-11053"}, Fixed: []string{"7.88.1-10+deb12u10"}}},
		{{Advisory: dep.Advisory{ID: "ALPINE-CVE-2024-11053", Summary: "When asked to both use a .netrc file for credentials and to follow HTTP", URL: "https://security.alpinelinux.org/vuln/CVE-2024-11053"}, Fixed: []string{"8.11.1-r0"}}},
		{{Advisory: dep.Advisory{ID: "RLSA-2026:1350", Summary: "Moderate: curl security update", URL: "https://errata.rockylinux.org/RLSA-2026:1350"}, Fixed: []string{"7.76.1-35.el9_7.3"}}},
		// Only the RHEL 9 stream's ranges count: RHSA-2024:0001 affects other el9 builds.
		{{Advisory: dep.Advisory{ID: "RHSA-2023:6745", Summary: "Red Hat Security Advisory: curl security update", URL: "https://access.redhat.com/errata/RHSA-2023:6745"}, Fixed: []string{"7.76.1-26.el9_3.2"}}},
		nil,
		nil,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if len(f.batches) != 1 || len(f.batches[0]) != 5 {
		t.Errorf("batches = %+v", f.batches)
	}
}

func TestRPMAffected(t *testing.T) {
	entries := func(events ...string) []affected {
		v := &vuln{}
		doc := `{"id":"X","affected":[{"package":{"name":"p","ecosystem":"Red Hat:enterprise_linux:9::baseos"},"ranges":[` + eco(events...) + `]}]}`
		if err := json.Unmarshal([]byte(doc), v); err != nil {
			t.Fatal(err)
		}
		return v.streamEntries("p", "Red Hat:enterprise_linux:9:")
	}
	tests := []struct {
		version string
		events  []string
		want    bool
	}{
		{"1.0-1", []string{`{"introduced":"0"}`, `{"fixed":"0:1.0-2"}`}, true},
		{"1.0-2", []string{`{"introduced":"0"}`, `{"fixed":"0:1.0-2"}`}, false},
		{"1.0-1", []string{`{"introduced":"1.0-2"}`}, false},
		{"1.0-3", []string{`{"introduced":"1.0-2"}`, `{"last_affected":"1.0-3"}`}, true},
		{"1.0-4", []string{`{"introduced":"1.0-2"}`, `{"last_affected":"1.0-3"}`}, false},
		{"1.0-1", []string{`{"introduced":"0"}`, `{"fixed":"not a version"}`}, true},
		{"latest", []string{`{"introduced":"0"}`}, false},
		// OSV records the events of a range in no particular order: they are sorted before
		// they are applied ("[0, 1.0-2)" and "[1.0-5, ∞)" here).
		{"1.0-3", []string{`{"introduced":"1.0-5"}`, `{"fixed":"1.0-2"}`, `{"introduced":"0"}`}, false},
		{"1.0-6", []string{`{"introduced":"1.0-5"}`, `{"fixed":"1.0-2"}`, `{"introduced":"0"}`}, true},
		{"1.0-1", []string{`{"introduced":"1.0-5"}`, `{"fixed":"1.0-2"}`, `{"introduced":"0"}`}, true},
	}
	for _, tt := range tests {
		tg := newTarget(query{Package: pkg{Name: "p", Ecosystem: ecoRedHat}, Version: tt.version})
		tg.scheme = "rpm"
		if got := tg.schemeAffected(entries(tt.events...)); got != tt.want {
			t.Errorf("schemeAffected(%s, %v) = %v, want %v", tt.version, tt.events, got, tt.want)
		}
	}
}
