package versioning

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The regexp implementation parseDocker replaced; kept as the reference for parity tests.
var (
	refDockerRe     = regexp.MustCompile(`^(v?)(\d+(?:\.\d+)*)(.*)$`)
	refCommitHashRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

func refParseDocker(s string) (dockerTag, bool) {
	if refCommitHashRe.MatchString(s) && strings.ContainsAny(s, "abcdef") {
		return dockerTag{}, false
	}
	m := refDockerRe.FindStringSubmatch(s)
	if m == nil {
		return dockerTag{}, false
	}
	t := dockerTag{prefix: m[1], suffix: m[3]}
	for p := range strings.SplitSeq(m[2], ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return dockerTag{}, false
		}
		t.parts = append(t.parts, n)
	}
	return t, true
}

func checkDockerParity(t *testing.T, s string) {
	t.Helper()
	want, wok := refParseDocker(s)
	got, gok := parseDocker(s)
	if wok != gok || want.prefix != got.prefix || want.suffix != got.suffix || !slices.Equal(want.parts, got.parts) {
		t.Errorf("parseDocker(%q) = %+v, %v; regexp = %+v, %v", s, got, gok, want, wok)
	}
}

var dockerCorpus = []string{
	"", "v", "vv1", "V1", "1", "v1", "01", "1.2", "1.2.", "1..2", ".1", "1.2.3", "v1.2.3-alpine",
	"1.2.3\n", "1\nfoo", "\n1", "1.2-rc.1", "1.x", "1.2.3.4.5.6", "latest", "-1", "+1", "1e5",
	"99999999999999999999", "1.99999999999999999999", "9223372036854775807", "9223372036854775808",
	"3a22eba", "3a22eb", "1234567", "abcdef0", "ABCDEF0", "3a22ebag", "0123456789abcdef0123456789abcdef01234567",
	"0123456789abcdef0123456789abcdef012345678", "1a2b3c4d", "1.2.3a", "v1.0\xff", "1\xff", "١٢٣", "1.٢",
	"20240101", "2024.01.01-r1", "v1.2.3+build", "1.2.3_4", "1 2", "1.2 ", "1.\n", "v\n1",
}

func TestParseDockerParity(t *testing.T) {
	for _, s := range dockerCorpus {
		checkDockerParity(t, s)
	}
	for _, s := range benchDockerTags() {
		checkDockerParity(t, s)
	}
}

func TestParseDocker(t *testing.T) {
	for _, tc := range []struct {
		in             string
		ok             bool
		prefix, suffix string
		parts          []int
	}{
		{in: "v1.2.3-alpine", ok: true, prefix: "v", suffix: "-alpine", parts: []int{1, 2, 3}},
		{in: "1.2.", ok: true, suffix: ".", parts: []int{1, 2}},
		{in: "1..2", ok: true, suffix: "..2", parts: []int{1}},
		{in: "1234567", ok: true, parts: []int{1234567}},
		{in: "1a2b3c4", ok: false},
		{in: "1a2b3c", ok: true, suffix: "a2b3c", parts: []int{1}},
		{in: "1.2\n", ok: false},
		{in: "1.99999999999999999999", ok: false},
		{in: "vv1", ok: false},
	} {
		got, ok := parseDocker(tc.in)
		if ok != tc.ok || got.prefix != tc.prefix || got.suffix != tc.suffix || !slices.Equal(got.parts, tc.parts) {
			t.Errorf("parseDocker(%q) = %+v, %v", tc.in, got, ok)
		}
	}
}

func FuzzParseDocker(f *testing.F) {
	for _, s := range dockerCorpus {
		f.Add(s)
	}
	f.Fuzz(checkDockerParity)
}

func benchDockerTags() []string {
	suffixes := []string{"", "-alpine", "-slim", "-bookworm", "-alpine3.20", "-rc.1", ".windowsservercore"}
	tags := make([]string, 0, 2100)
	for major := range 10 {
		for minor := range 10 {
			for patch := range 10 {
				s := suffixes[(major+minor+patch)%len(suffixes)]
				tags = append(tags, fmt.Sprintf("%d.%d.%d%s", major, minor, patch, s))
			}
			tags = append(tags, fmt.Sprintf("v%d.%d", major, minor), fmt.Sprintf("%d.%d-%07x", major, minor, major*1000+minor))
		}
	}
	for i := range 200 {
		tags = append(tags, fmt.Sprintf("%07x", 0xa000000+i*7919), "latest", fmt.Sprintf("sha-%x", i))
	}
	return tags
}

func BenchmarkParseDocker(b *testing.B) {
	tags := benchDockerTags()
	b.ReportAllocs()
	for b.Loop() {
		for _, s := range tags {
			parseDocker(s)
		}
	}
}

func BenchmarkDockerBest(b *testing.B) {
	s, ok := Get("docker")
	if !ok {
		b.Fatal("no docker scheme")
	}
	tags := benchDockerTags()
	b.ReportAllocs()
	for b.Loop() {
		s.Best("1.2.0-alpine", tags, nil, nil)
	}
}
