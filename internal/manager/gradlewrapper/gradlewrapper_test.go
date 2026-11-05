package gradlewrapper

import (
	"os"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Fixtures (fetched 2026-11-04):
//   - testdata/spring-boot.properties: https://raw.githubusercontent.com/spring-projects/spring-boot/main/gradle/wrapper/gradle-wrapper.properties
//   - testdata/nowinandroid.properties: https://raw.githubusercontent.com/android/nowinandroid/main/gradle/wrapper/gradle-wrapper.properties
//
// The nowinandroid sum equals https://services.gradle.org/distributions/gradle-9.7.1-bin.zip.sha256.
const nowSum = "acd53f1edaf02f1a8ff99879f8a34b302661a057d9b063ae9e35b552f804d20a"

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMatch(t *testing.T) {
	m := New()
	for path, want := range map[string]bool{
		"gradle/wrapper/gradle-wrapper.properties":     true,
		"app/gradle/wrapper/gradle-wrapper.properties": true,
		"xgradle/wrapper/gradle-wrapper.properties":    false,
		"gradle/wrapper/gradle-wrapper.jar":            false,
		"gradle-wrapper.properties":                    false,
	} {
		if got := m.Match(path); got != want {
			t.Errorf("Match(%q) = %v", path, got)
		}
	}
}

func TestExtract(t *testing.T) {
	for _, tc := range []struct {
		name, src                string
		version, checksum, csURL string
		line                     int
		skip                     string
	}{
		{name: "spring-boot", src: fixture(t, "spring-boot.properties"), version: "9.8.1", line: 3},
		{name: "nowinandroid", src: fixture(t, "nowinandroid.properties"), version: "9.7.1", line: 4, checksum: nowSum,
			csURL: "https://services.gradle.org/distributions/gradle-{{version}}-bin.zip.sha256"},
		{name: "spaces, all, unescaped", src: "distributionSha256Sum = " + nowSum + "\ndistributionUrl = https://services.gradle.org/distributions/gradle-8.11-rc-1-all.zip  \n",
			version: "8.11-rc-1", line: 2, checksum: nowSum, csURL: "https://services.gradle.org/distributions/gradle-{{version}}-all.zip.sha256"},
		{name: "colon, crlf, mirror", src: "# mirror\r\ndistributionUrl: https\\://mirror.example.com/gradle/gradle-9.0-milestone-1-bin.zip\r\n", version: "9.0-milestone-1", line: 2},
		{name: "unsupported", src: "distributionUrl=https\\://example.com/dist/custom.zip\n", line: 1, skip: "unsupported distributionUrl"},
		{name: "bad sum", src: "distributionUrl=https\\://services.gradle.org/distributions/gradle-8.10.2-bin.zip\ndistributionSha256Sum=abc\n",
			version: "8.10.2", line: 1, skip: "invalid distributionSha256Sum"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds, err := New().Extract("gradle/wrapper/gradle-wrapper.properties", []byte(tc.src), manager.Env{})
			if err != nil || len(ds) != 1 {
				t.Fatalf("deps %v, err %v", ds, err)
			}
			d := ds[0]
			if d.Manager != Name || d.Datasource != "gradle-version" || d.Versioning != "maven" || d.DepType != DepTypeWrapper ||
				d.Name != "gradle" || d.Line != tc.line || d.Skip != tc.skip || d.Version != tc.version {
				t.Fatalf("dep = %+v", d)
			}
			if tc.skip != "" {
				return
			}
			if got := tc.src[d.RefSpan.Start:d.RefSpan.End]; got != tc.version {
				t.Errorf("RefSpan = %q", got)
			}
			if d.Checksum != tc.checksum || tc.src[d.ChecksumSpan.Start:d.ChecksumSpan.End] != tc.checksum {
				t.Errorf("checksum = %q span %v", d.Checksum, d.ChecksumSpan)
			}
			if (d.ChecksumSource != nil) != (tc.csURL != "") || d.ChecksumSource != nil && *d.ChecksumSource != (dep.ChecksumSource{URL: tc.csURL}) {
				t.Errorf("ChecksumSource = %+v", d.ChecksumSource)
			}
		})
	}
	if ds, err := New().Extract("f", []byte("distributionBase=GRADLE_USER_HOME\n"), manager.Env{}); err != nil || len(ds) != 0 {
		t.Errorf("no url: %v, %v", ds, err)
	}
}

func TestEdits(t *testing.T) {
	const newSum = "31c55713e40233a8303827ceb42ca48a47267a0ad4bab9177123121e71524c26" // gradle-8.10.2-bin.zip.sha256
	src := fixture(t, "nowinandroid.properties")
	ds, err := New().Extract("f", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	d := ds[0]
	for _, tc := range []struct {
		name string
		u    dep.Update
		old  []string
		new  []string
	}{
		{name: "version and checksum", u: dep.Update{Dep: d, NewVersion: "8.10.2", NewChecksum: newSum},
			old: []string{"gradle-9.7.1-bin", nowSum}, new: []string{"gradle-8.10.2-bin", newSum}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edits, err := New().Edits(tc.u)
			if err != nil {
				t.Fatal(err)
			}
			out, err := patch.Apply([]byte(src), edits)
			if err != nil {
				t.Fatal(err)
			}
			for i := range tc.old {
				if strings.Contains(string(out), tc.old[i]) || !strings.Contains(string(out), tc.new[i]) {
					t.Errorf("out = %s", out)
				}
			}
			if !strings.Contains(string(out), `distributionUrl=https\://services.gradle.org/distributions/gradle-8.10.2-bin.zip`) {
				t.Errorf("escape lost: %s", out)
			}
		})
	}
	// Without a pinned checksum only the version changes.
	src = fixture(t, "spring-boot.properties")
	ds, _ = New().Extract("f", []byte(src), manager.Env{})
	edits, err := New().Edits(dep.Update{Dep: ds[0], NewVersion: "9.8.2"})
	if err != nil || len(edits) != 1 {
		t.Fatalf("edits %v, err %v", edits, err)
	}
	if out, err := patch.Apply([]byte(src), edits); err != nil || !strings.Contains(string(out), "gradle-9.8.2-bin.zip") {
		t.Errorf("out %s, err %v", out, err)
	}
}
