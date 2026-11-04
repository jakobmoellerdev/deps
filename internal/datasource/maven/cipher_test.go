package mavends

import (
	"context"
	"encoding/base64"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Test vectors of plexus-cipher 2.0 (the SHA-256 PBECipher Maven 3.9 ships) and
// plexus-sec-dispatcher (fetched 2026-11-04):
//   - PBECipherTest: "veryOpenText" with password "testtest",
//     https://github.com/codehaus-plexus/plexus-cipher/blob/plexus-cipher-2.0/src/test/java/org/sonatype/plexus/components/cipher/PBECipherTest.java
//   - DefaultPlexusCipherTest: "my testing phrase" with password "testtest",
//     https://github.com/codehaus-plexus/plexus-cipher/blob/plexus-cipher-2.0/src/test/java/org/sonatype/plexus/components/cipher/DefaultPlexusCipherTest.java
//   - SecUtilTest: master `{1wQaa6S/…}` (decrypted with "settings.security") decrypting the
//     server password `{BteqUEnq…}` to "testtest",
//     https://github.com/codehaus-plexus/plexus-sec-dispatcher/blob/sec-dispatcher-1.4/src/test/java/org/sonatype/plexus/components/sec/dispatcher/SecUtilTest.java
const (
	secUtilMaster   = "{1wQaa6S/o8MH7FnaTNL53XmhT5O0SEGXQi3gC49o6OY=}"
	secUtilPassword = "{BteqUEnqHecHM7MZfnj9FwLcYbdInWxou1C929Txa0A=}"
)

func TestPlexusDecrypt(t *testing.T) {
	tests := []struct{ payload, password, want string }{
		{"ibeHrdCOonkH7d7YnH7sarQLbwOk1ljkkM/z8hUhl4c=", "testtest", "veryOpenText"},
		{"LFulS0pAlmMHpDtm+81oPcqctcwpco5p4Fo7640/gqDRifCahXBefG4FxgKcu17v", "testtest", "my testing phrase"},
	}
	for _, tt := range tests {
		got, err := plexusDecrypt(tt.payload, tt.password)
		if err != nil || got != tt.want {
			t.Errorf("plexusDecrypt(%s) = %q, %v; want %q", tt.payload, got, err, tt.want)
		}
	}
	for _, bad := range []struct{ payload, password string }{
		{"ibeHrdCOonkH7d7YnH7sarQLbwOk1ljkkM/z8hUhl4c=", "wrong"},
		{"not base64!", "testtest"},
		{base64.StdEncoding.EncodeToString([]byte("short")), "testtest"},
		{base64.StdEncoding.EncodeToString(append(make([]byte, 8), 0, 1, 2, 3)), "testtest"},
	} {
		if got, err := plexusDecrypt(bad.payload, bad.password); err == nil {
			t.Errorf("plexusDecrypt(%q, %q) = %q, want error", bad.payload, bad.password, got)
		}
	}
}

func TestEncrypted(t *testing.T) {
	// The decoration cases of DefaultPlexusCipherTest.testUnDecorate.
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"This is a test", "", false},
		{"{This is a test}", "This is a test", true},
		{"Comment {This is a test} other comment with a: }", "This is a test", true},
		{`\{This is a test\}`, "", false},
		{`Comment {foo\{This is a test\}} other comment with a: }`, `foo\{This is a test\}`, true},
		{"{}", "", false},
	}
	for _, tt := range tests {
		if got, ok := encrypted(tt.in); got != tt.want || ok != tt.ok {
			t.Errorf("encrypted(%q) = %q, %v", tt.in, got, ok)
		}
	}
}

func TestEncryptedServerPassword(t *testing.T) {
	settings := `<settings><servers><server><id>corp</id><username>ci</username><password>` + secUtilPassword + `</password></server></servers></settings>`
	security := `<settingsSecurity><master>` + secUtilMaster + `</master></settingsSecurity>`
	const p = "corp.example/m2/g/a/maven-metadata.xml"
	d := dep.Dep{Manager: "maven", File: "pom.xml", Name: "g:a", Source: "corp=https://corp.example/m2/"}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("ci:testtest"))

	t.Run("next to user settings", func(t *testing.T) {
		home := t.TempDir()
		writeFile(t, filepath.Join(home, ".m2", "settings.xml"), settings)
		writeFile(t, filepath.Join(home, ".m2", "settings-security.xml"), security)
		h, c := newHosts(t)
		h.files[p] = libMeta
		s := New(c, nil, env(map[string]string{"HOME": home}))
		if _, err := s.Releases(context.Background(), d, t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if hs := h.headers(p); len(hs) != 1 || hs[0].Get("Authorization") != want {
			t.Errorf("headers = %v", hs)
		}
	})
	t.Run("relocated", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "settings.xml"), settings)
		writeFile(t, filepath.Join(dir, "sec.xml"), `<settingsSecurity><relocation>`+filepath.Join(dir, "real.xml")+`</relocation></settingsSecurity>`)
		writeFile(t, filepath.Join(dir, "real.xml"), security)
		h, c := newHosts(t)
		h.files[p] = libMeta
		s := New(c, nil, env(map[string]string{"DEPS_MAVEN_SETTINGS": filepath.Join(dir, "settings.xml"), "DEPS_MAVEN_SETTINGS_SECURITY": filepath.Join(dir, "sec.xml")}))
		if _, err := s.Releases(context.Background(), d, t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if hs := h.headers(p); len(hs) != 1 || hs[0].Get("Authorization") != want {
			t.Errorf("headers = %v", hs)
		}
	})
	t.Run("no master password", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "settings.xml"), settings)
		s := New(http.DefaultClient, nil, env(map[string]string{"DEPS_MAVEN_SETTINGS": filepath.Join(dir, "settings.xml")}))
		_, err := s.Releases(context.Background(), d, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "server corp password") || strings.Contains(err.Error(), "BteqUEnq") {
			t.Errorf("err = %v", err)
		}
		// Servers that are not used do not matter.
		if _, err := s.repositories(dep.Dep{Manager: "maven", File: "pom.xml", Source: "other=https://other.example/"}, t.TempDir()); err != nil {
			t.Errorf("unused server: %v", err)
		}
	})
}

func TestGradleCredentials(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "gradle.properties"), "# corp repository\ncorpUsername = alice\ncorpPassword=pa\\:ss\\\n  word\nonlyUserUsername: bob\n")
	h, c := newHosts(t)
	const p = "/m2/g/a/maven-metadata.xml"
	for _, host := range []string{"corp.example", "envrepo.example", "user.example", "anon.example"} {
		h.files[host+p] = libMeta
	}
	s := New(c, nil, env(map[string]string{
		"GRADLE_USER_HOME":                   home,
		"ORG_GRADLE_PROJECT_envRepoUsername": "carol",
		"ORG_GRADLE_PROJECT_envRepoPassword": "env-secret",
		"ORG_GRADLE_PROJECT_corpPassword":    "from-env",
	}))
	d := dep.Dep{
		Manager: "gradle", File: "build.gradle.kts", Name: "g:a",
		Source: "corp=https://corp.example/m2/ envRepo=https://envrepo.example/m2/ onlyUser=https://user.example/m2/ https://anon.example/m2/",
	}
	if _, err := s.Releases(context.Background(), d, home); err != nil {
		t.Fatal(err)
	}
	basic := func(u, p string) string { return "Basic " + base64.StdEncoding.EncodeToString([]byte(u+":"+p)) }
	for host, want := range map[string]string{
		"corp.example":    basic("alice", "from-env"), // env overrides gradle.properties per property
		"envrepo.example": basic("carol", "env-secret"),
		"user.example":    basic("bob", ""),
		"anon.example":    "",
	} {
		if hs := h.headers(host + p); len(hs) != 1 || hs[0].Get("Authorization") != want {
			t.Errorf("%s: headers = %v, want %q", host, hs, want)
		}
	}
	// Maven deps do not use Gradle credentials.
	if repos, _ := s.repositories(dep.Dep{Manager: "maven", File: "pom.xml", Source: d.Source}, ""); repos[0].auth != nil {
		t.Error("maven dep got Gradle credentials")
	}
}

func TestParseProperties(t *testing.T) {
	got := parseProperties([]byte("a=1\r\n! comment\n  b : two words \nc three\nd\\=x=4\ne=multi\\\n   line\\\\\nf=\\u00e9\\t\n\n"))
	want := map[string]string{"a": "1", "b": "two words ", "c": "three", "d=x": "4", "e": "multiline\\", "f": "é\t"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %v", got)
	}
}
