package gitx

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
)

func need(t *testing.T, tools ...string) {
	t.Helper()
	for _, name := range tools {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " not in PATH")
		}
	}
}

// tempDir points os.TempDir at a fresh directory, so leftovers of the signer are observable.
func tempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	return dir
}

func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("leftover temporary file %s", e.Name())
	}
}

func run(t *testing.T, env []string, stdin, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

// sshKey generates an ed25519 key in a temporary directory and returns its path.
func sshKey(t *testing.T, passphrase string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "id")
	run(t, nil, "", "ssh-keygen", "-q", "-t", "ed25519", "-N", passphrase, "-C", "bot", "-f", p)
	return p
}

// pgpKey generates an ed25519 OpenPGP key for Bot <bot@example.com> in a throwaway GNUPGHOME and
// returns the armored secret key.
func pgpKey(t *testing.T, passphrase string) string {
	t.Helper()
	home, err := os.MkdirTemp("", "gpgtest-") // short: agent sockets may live in GNUPGHOME
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"GNUPGHOME=" + home}
	t.Cleanup(func() {
		_ = exec.Command("gpgconf", "--homedir", home, "--kill", "all").Run()
		_ = exec.Command("gpgconf", "--homedir", home, "--remove-socketdir").Run()
		os.RemoveAll(home)
	})
	run(t, env, "", "gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", passphrase,
		"--quick-gen-key", "Bot <bot@example.com>", "ed25519", "sign", "never")
	args := []string{"--batch", "--armor", "--export-secret-keys"}
	if passphrase != "" {
		args = []string{"--batch", "--pinentry-mode", "loopback", "--passphrase", passphrase, "--armor", "--export-secret-keys"}
	}
	return run(t, env, "", "gpg", args...)
}

func signedCommit(t *testing.T, s *Signer) (clone, commit string) {
	t.Helper()
	_, clone = setup(t)
	parent := git(t, clone, "rev-parse", "HEAD")
	r := Repo{Dir: clone, Signer: s}
	return clone, mkCommit(t, r, parent, []engine.FileChange{{Path: "README", Mode: 0o644, New: []byte("x\n")}}, "signed\n")
}

func TestSSHSigner(t *testing.T) {
	need(t, "git", "ssh-keygen")
	key := sshKey(t, "")
	tmp := tempDir(t)

	// The value is a path here.
	s, err := NewSigner(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if s.Format != FormatSSH || !strings.HasPrefix(s.Fingerprint, "SHA256:") {
		t.Fatalf("signer = %s %q", s.Format, s.Fingerprint)
	}
	if fi, err := os.Stat(filepath.Join(s.dir, "key")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}
	clone, c := signedCommit(t, s)

	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(t.TempDir(), "allowed")
	write(t, allowed, "bot@example.com namespaces=\"git\" "+string(pub))
	out := git(t, clone, "-c", "gpg.ssh.allowedSignersFile="+allowed, "verify-commit", "--raw", c)
	if !strings.Contains(out, "Good \"git\" signature for bot@example.com") {
		t.Errorf("verify-commit: %s", out)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	assertEmpty(t, tmp)
}

func TestOpenPGPSigner(t *testing.T) {
	need(t, "git", "gpg", "gpgconf")
	armored := pgpKey(t, "")
	tmp := tempDir(t)

	// The value is the key content, as a CI secret holds it: CRLF, no final newline.
	s, err := NewSigner(t.Context(), strings.ReplaceAll(strings.TrimSpace(armored), "\n", "\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Format != FormatOpenPGP || len(s.Fingerprint) != 40 {
		t.Fatalf("signer = %s %q", s.Format, s.Fingerprint)
	}
	clone, c := signedCommit(t, s)

	// Verified against the signer's temporary keyring.
	cmd := exec.Command("git", "verify-commit", "--raw", c)
	cmd.Dir = clone
	cmd.Env = append(os.Environ(), s.env...)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "GOODSIG") || !strings.Contains(string(out), s.Fingerprint) {
		t.Errorf("verify-commit: %v\n%s", err, out)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	assertEmpty(t, tmp)
}

func TestSignerErrors(t *testing.T) {
	ctx := t.Context()
	tmp := tempDir(t)
	for name, key := range map[string]string{
		"garbage":          "s3cr3t-not-a-key",
		"multi-line":       "line1\nline2",
		"public key":       "-----BEGIN PGP PUBLIC KEY BLOCK-----\nx\n",
		"rsa pem":          "-----BEGIN RSA PRIVATE KEY-----\nx\n",
		"missing file":     filepath.Join(tmp, "nope"),
		"file without key": func() string { p := filepath.Join(t.TempDir(), "k"); write(t, p, "hello\n"); return p }(),
	} {
		_, err := NewSigner(ctx, key)
		if !errors.Is(err, ErrKeyFormat) {
			t.Errorf("%s: err = %v, want ErrKeyFormat", name, err)
		} else if strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), "nope") {
			t.Errorf("%s: error reveals the value: %v", name, err)
		}
	}
	if _, err := NewSigner(ctx, filepath.Join(tmp, "nope")); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Errorf("missing file: err = %v, want the cause", err)
	}

	t.Run("protected ssh", func(t *testing.T) {
		need(t, "ssh-keygen")
		_, err := NewSigner(ctx, sshKey(t, "secret"))
		if err == nil || errors.Is(err, ErrKeyFormat) || !strings.Contains(err.Error(), "protected keys are not supported") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("protected openpgp", func(t *testing.T) {
		need(t, "gpg", "gpgconf")
		_, err := NewSigner(ctx, pgpKey(t, "secret"))
		if err == nil || errors.Is(err, ErrKeyFormat) || !strings.Contains(err.Error(), "protected keys are not supported") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("missing tools", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		for _, c := range []struct{ key, tool string }{
			{sshHeader + "\nx\n-----END OPENSSH PRIVATE KEY-----\n", "ssh-keygen"},
			{pgpHeader + "\nx\n", "gpg"},
		} {
			if _, err := NewSigner(ctx, c.key); err == nil || !strings.Contains(err.Error(), "needs "+c.tool+" in PATH") {
				t.Errorf("%s: err = %v", c.tool, err)
			}
		}
	})
	t.Run("invalid ssh key", func(t *testing.T) {
		need(t, "ssh-keygen")
		if _, err := NewSigner(ctx, sshHeader+"\nbm90IGEga2V5\n-----END OPENSSH PRIVATE KEY-----"); err == nil {
			t.Error("invalid OpenSSH key accepted")
		}
	})
	t.Run("invalid openpgp key", func(t *testing.T) {
		need(t, "gpg", "gpgconf")
		if _, err := NewSigner(ctx, pgpHeader+"\n\nbm90IGEga2V5\n-----END PGP PRIVATE KEY BLOCK-----"); err == nil {
			t.Error("invalid OpenPGP key accepted")
		}
	})
	assertEmpty(t, tmp)
}

func TestPrimaryFingerprints(t *testing.T) {
	out := "sec:u:255:22:AAAA:1::::::scESC:\nfpr:::::::::AAAAFPR:\ngrp:::::::::G:\nuid:u::::::::Bot:\n" +
		"ssb:u:255:18:BBBB:1::::::e:\nfpr:::::::::BBBBFPR:\nsec:u:255:22:CCCC:1:::::::\nfpr:::::::::CCCCFPR:\n"
	if got := primaryFingerprints(out); strings.Join(got, ",") != "AAAAFPR,CCCCFPR" {
		t.Errorf("primaryFingerprints = %v", got)
	}
}

func TestSubcommand(t *testing.T) {
	if got := subcommand([]string{"-c", "a=b", "-c", "c=d", "commit-tree", "x"}); got != "commit-tree" {
		t.Errorf("subcommand = %q", got)
	}
	if got := subcommand([]string{"push"}); got != "push" {
		t.Errorf("subcommand = %q", got)
	}
}
