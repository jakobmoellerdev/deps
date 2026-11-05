package mavends

import (
	"os"
	"slices"
	"testing"
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
	if !slices.Equal(got, want) {
		t.Fatalf("scanVersions(%q) = %q, encoding/xml %q", b, got, want)
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
	if v, ok := scanVersions([]byte(scanSeeds[0])); !ok || !slices.Equal(v, []string{"1.0", "2.0"}) {
		t.Errorf("scanVersions(seed 0) = %q, %v", v, ok)
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
