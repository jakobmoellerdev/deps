package mavends

import (
	"os"
	"slices"
	"testing"
)

// The documents the lastUpdated assertions of TestScanVersionsSeeds name.
const (
	lastUpdatedSeed  = `<metadata><versioning><lastUpdated> 20260102030405 </lastUpdated></versioning></metadata>`
	strayLastUpdated = `<metadata><lastUpdated>1</lastUpdated><versions><lastUpdated>2</lastUpdated></versions></metadata>`
)

var scanSeeds = []string{
	`<metadata><versioning><versions><version>1.0</version><version> 2.0 </version><version></version></versions></versioning></metadata>`,
	`<?xml version="1.0" encoding="UTF-8"?><metadata modelVersion='1.1.0'><version>9</version><versioning><versions><version>1</version></versions></versioning></metadata>trailing`,
	`<metadata><versioning><versions><version>1<!-- c --></version></versions></versioning></metadata>`,
	`<metadata><versioning><versions><version><![CDATA[1]]></version></versions></versioning></metadata>`,
	`<metadata><versioning><versions><version>1&amp;2</version></versions></versioning></metadata>`,
	`<metadata><versioning><versions><version>1</version></versions></versioning>`,
	`<metadata><versioning><versions><version>1</versions></version></versioning></metadata>`,
	`<metadata><versioning><versions><version><x/>1</version></versions></versioning></metadata>`,
	`<metadata><versioning><versions><version/><version >3</version ></versions></versioning></metadata>`,
	`<m:metadata xmlns:m="x"><versioning><versions><version>1</version></versions></versioning></m:metadata>`,
	`<metadata/>`,
	`<?xml version="1.1"?><metadata/>`,
	`<a><b><c><d><e><f><g><h><i><j>x</j></i></h></g></f></e></d></c></b></a>`,
	"<metadata><versioning><versions><version>1\r\n</version></versions></versioning></metadata>",
	"<metadata><versioning><versions><version>\u00e91</version></versions></versioning></metadata>",
	`<metadata a="1" a="2"><x a=1/></metadata>`,
	`text<metadata/>`,
	lastUpdatedSeed,
	`<metadata><versioning><lastUpdated>1</lastUpdated><lastUpdated/></versioning></metadata>`,
	`<metadata><versioning><lastUpdated>1</lastUpdated><lastUpdated>2</lastUpdated></versioning></metadata>`,
	strayLastUpdated,
	`<metadata><versioning><lastUpdated><x/>1</lastUpdated></versioning></metadata>`,
	``,
}

// checkScanParity fails if scanVersions accepts b but disagrees with encoding/xml.
func checkScanParity(t *testing.T, b []byte) {
	t.Helper()
	got, ok := scanVersions(b)
	if !ok {
		return
	}
	want, err := decodeMetadata(b)
	if err != nil {
		t.Fatalf("scanVersions accepted %q, encoding/xml: %v", b, err)
	}
	if !slices.Equal(got.versions, want.versions) {
		t.Fatalf("scanVersions(%q) = %q, encoding/xml %q", b, got.versions, want.versions)
	}
	if got.lastUpdated != want.lastUpdated {
		t.Fatalf("scanVersions(%q) lastUpdated = %q, encoding/xml %q", b, got.lastUpdated, want.lastUpdated)
	}
}

func TestScanVersionsFixtures(t *testing.T) {
	for _, f := range []string{"testdata/guava-maven-metadata.xml", "testdata/kotlin-jvm-plugin-maven-metadata.xml"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := scanVersions(b); !ok {
			t.Errorf("%s: scanVersions fell back to encoding/xml", f)
		}
		checkScanParity(t, b)
	}
}

func TestScanVersionsSeeds(t *testing.T) {
	for _, s := range scanSeeds {
		checkScanParity(t, []byte(s))
	}
	if doc, ok := scanVersions([]byte(scanSeeds[0])); !ok || !slices.Equal(doc.versions, []string{"1.0", "2.0"}) {
		t.Errorf("scanVersions(seed 0) = %q, %v", doc.versions, ok)
	}
	// The scanner reads `versioning>lastUpdated` only where encoding/xml does.
	if doc, ok := scanVersions([]byte(lastUpdatedSeed)); !ok || doc.lastUpdated != "20260102030405" {
		t.Errorf("scanVersions(lastUpdated seed) = %q, %v", doc.lastUpdated, ok)
	}
	if doc, ok := scanVersions([]byte(strayLastUpdated)); !ok || doc.lastUpdated != "" {
		t.Errorf("scanVersions(lastUpdated outside versioning) = %q, %v", doc.lastUpdated, ok)
	}
	for _, s := range scanSeeds[2:8] {
		if _, ok := scanVersions([]byte(s)); ok {
			t.Errorf("scanVersions(%q) accepted markup it should leave to encoding/xml", s)
		}
	}
}

func FuzzScanVersions(f *testing.F) {
	for _, s := range scanSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(checkScanParity)
}
