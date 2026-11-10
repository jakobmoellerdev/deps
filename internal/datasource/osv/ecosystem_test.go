package osv

import (
	"reflect"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Trimmed copies of the real OSV records (fetched 2026-11-10). OSV returns every advisory of
// the package for these ecosystems, so the ranges decide.
const (
	juliaHTTP = `{"id":"JLSEC-2025-1","summary":"CR/LF injection in URIs.jl (also affects HTTP.jl)",
		"affected":[{"package":{"name":"HTTP","ecosystem":"Julia"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"1.10.17"}]}]},
		            {"package":{"name":"URIs","ecosystem":"Julia"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"1.6.0"}]}]}]}`
	// Scored with CVSS v4 only, so deps.dev publishes no base score; the record's own
	// severity is the rating deps shows.
	swiftNIO = `{"id":"GHSA-r3rc-9hpw-54v9","summary":"swift-nio vulnerable to HTTP request smuggling","aliases":["CVE-2025-123"],
		"severity":[{"type":"CVSS_V4","score":"CVSS:4.0/AV:N/AC:H/AT:P/PR:N/UI:N/VC:L/VI:H/VA:L/SC:N/SI:N/SA:N"}],
		"database_specific":{"severity":"HIGH"},
		"affected":[{"package":{"name":"github.com/apple/swift-nio","ecosystem":"SwiftURL"},"ranges":[{"type":"SEMVER","events":[{"introduced":"1.0.0"},{"fixed":"2.100.0"}]}]}]}`
)

func TestAdvisoriesJulia(t *testing.T) {
	f := newFake(t)
	f.results["Julia|HTTP|1.9.0|"] = result{ids: []string{"JLSEC-2025-1"}}
	f.results["Julia|HTTP|1.10.17|"] = result{ids: []string{"JLSEC-2025-1"}}
	f.vulns["JLSEC-2025-1"] = juliaHTTP
	s := f.start(nil)
	deps := []dep.Dep{
		// The manifest's version is queried, else the compat entry's lower bound.
		{Manager: "julia", Datasource: "julia-registry", Name: "HTTP", Version: "1.9", Locked: "1.9.0"},
		// Already fixed: OSV returns the advisory anyway, the range rules it out.
		{Manager: "julia", Datasource: "julia-registry", Name: "HTTP", Version: "1.10.17"},
		{Manager: "julia", Datasource: "julia-registry", Name: "HTTP"},
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]datasource.Vuln{
		{{Advisory: dep.Advisory{ID: "JLSEC-2025-1", Summary: "CR/LF injection in URIs.jl (also affects HTTP.jl)", URL: "https://osv.dev/vulnerability/JLSEC-2025-1"}, Fixed: []string{"1.10.17"}}},
		nil,
		nil,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestAdvisoriesSwift(t *testing.T) {
	f := newFake(t)
	f.results["SwiftURL|github.com/apple/swift-nio|2.40.0|"] = result{ids: []string{"GHSA-r3rc-9hpw-54v9"}}
	f.results["SwiftURL|github.com/apple/swift-nio|2.100.0|"] = result{ids: []string{"GHSA-r3rc-9hpw-54v9"}}
	f.vulns["GHSA-r3rc-9hpw-54v9"] = swiftNIO
	s := f.start(nil)
	const src = "https://github.com/apple/swift-nio"
	deps := []dep.Dep{
		// Package.resolved pins the version; the manifest's requirement is the fallback.
		{Manager: "swift", Datasource: "git-tags", Name: "apple/swift-nio", Source: src, Version: "2.0.0", Locked: "2.40.0"},
		// The mint and xcodegen managers extract the same packages.
		{Manager: "xcodegen", Datasource: "git-tags", Name: "apple/swift-nio", Source: "https://GitHub.com/apple/swift-nio.git", Version: "v2.40.0"},
		{Manager: "mint", Datasource: "git-tags", Name: "apple/swift-nio", Source: src, Version: "2.100.0"},
		// A branch or an owner-less URL names no package.
		{Manager: "swift", Datasource: "git-tags", Name: "apple/swift-nio", Source: src},
		{Manager: "swift", Datasource: "git-tags", Name: "nio", Source: "https://github.com/nio", Version: "2.40.0"},
		// Other managers' git-tags deps are not Swift packages.
		{Manager: "git-submodules", Datasource: "git-tags", Name: "apple/swift-nio", Source: src, Version: "2.40.0"},
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	adv := dep.Advisory{
		ID: "GHSA-r3rc-9hpw-54v9", Summary: "swift-nio vulnerable to HTTP request smuggling",
		Aliases: []string{"CVE-2025-123"}, URL: "https://osv.dev/vulnerability/GHSA-r3rc-9hpw-54v9",
		CVSS: 8.3, Rating: "high", // scored from the record's own CVSS v4 vector
	}
	want := [][]datasource.Vuln{
		{{Advisory: adv, Fixed: []string{"2.100.0"}}},
		{{Advisory: adv, Fixed: []string{"2.100.0"}}},
		nil, nil, nil, nil,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if risk := adv.Risk(); risk != "CVSS 8.3 high" {
		t.Errorf("risk = %q", risk)
	}
	if len(f.batches) != 1 || len(f.batches[0]) != 2 { // the two versions, deduped
		t.Errorf("batches = %+v", f.batches)
	}
}
