package gitx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Signing key formats.
const (
	FormatSSH     = "ssh"
	FormatOpenPGP = "openpgp"
)

const (
	sshHeader = "-----BEGIN OPENSSH PRIVATE KEY-----"
	pgpHeader = "-----BEGIN PGP PRIVATE KEY BLOCK-----"
)

// ErrKeyFormat reports a signing key that is neither an OpenSSH nor an armored OpenPGP private
// key, nor a readable file holding one.
var ErrKeyFormat = errors.New("signing key: want an OpenSSH private key (" + sshHeader +
	") or an armored OpenPGP private key (" + pgpHeader + "), or the path of a file holding one")

// Signer makes Commit sign the commits it creates. It owns temporary key material (an SSH key
// file or a GNUPGHOME) that Close removes. A nil *Signer means unsigned commits.
type Signer struct {
	Format      string // FormatSSH or FormatOpenPGP
	Fingerprint string // SHA256:… (ssh) or the primary key fingerprint (openpgp)

	dir    string   // temporary directory removed by Close
	config []string // git -c arguments selecting the format, program and key
	env    []string // extra environment of the signing git process
}

// NewSigner prepares signing with key, which is the private key itself or the path of a file
// holding it. Only keys without a passphrase are supported. The key content is never logged or
// put in an error. The caller must Close the returned Signer, also on error paths.
func NewSigner(ctx context.Context, key string) (*Signer, error) {
	content, err := keyContent(key)
	if err != nil {
		return nil, err
	}
	switch {
	case strings.HasPrefix(content, sshHeader):
		return newSSHSigner(ctx, content)
	case strings.HasPrefix(content, pgpHeader):
		return newOpenPGPSigner(ctx, content)
	}
	return nil, ErrKeyFormat
}

// keyContent returns the normalized key text: inline when key holds an armor header, else read
// from the file key names. Line endings become LF and the text ends in one newline (CI secrets
// often lose the final newline, which ssh-keygen rejects).
func keyContent(key string) (string, error) {
	if !strings.Contains(key, "-----BEGIN ") {
		if strings.ContainsAny(key, "\n\r") || strings.TrimSpace(key) == "" {
			return "", ErrKeyFormat
		}
		b, err := os.ReadFile(key)
		if err != nil {
			// The path error carries the value; it may be a mangled secret, so only the cause is
			// reported.
			if pe, ok := errors.AsType[*fs.PathError](err); ok {
				err = pe.Err
			}
			return "", fmt.Errorf("%w (reading it as a file: %w)", ErrKeyFormat, err)
		}
		key = string(b)
	}
	key = strings.ReplaceAll(key, "\r\n", "\n")
	return strings.TrimSpace(key) + "\n", nil
}

func lookTool(name, format string) (string, error) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("signing with an %s key needs %s in PATH: %w", format, name, err)
	}
	return p, nil
}

// tool runs a signing helper with stdin and env; errors carry its stderr, never stdin.
func tool(ctx context.Context, env []string, stdin []byte, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", filepath.Base(name), args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func newSSHSigner(ctx context.Context, content string) (*Signer, error) {
	keygen, err := lookTool("ssh-keygen", "OpenSSH")
	if err != nil {
		return nil, err
	}
	s, err := newSigner(FormatSSH, "deps-sshkey-*")
	if err != nil {
		return nil, err
	}
	keyFile := filepath.Join(s.dir, "key")
	if err := os.WriteFile(keyFile, []byte(content), 0o600); err != nil {
		return nil, s.closeWith(err)
	}
	// -P "" fails for passphrase-protected keys instead of prompting.
	pub, err := tool(ctx, nil, nil, keygen, "-y", "-P", "", "-f", keyFile)
	if err != nil {
		return nil, s.closeWith(fmt.Errorf("reading the OpenSSH key without a passphrase (protected keys are not supported): %w", err))
	}
	pubFile := keyFile + ".pub"
	if err := os.WriteFile(pubFile, []byte(pub), 0o600); err != nil {
		return nil, s.closeWith(err)
	}
	out, err := tool(ctx, nil, nil, keygen, "-l", "-f", pubFile)
	if err != nil {
		return nil, s.closeWith(err)
	}
	if f := strings.Fields(out); len(f) >= 2 {
		s.Fingerprint = f[1]
	}
	s.config = []string{"-c", "gpg.format=ssh", "-c", "gpg.ssh.program=" + keygen, "-c", "user.signingkey=" + keyFile}
	return s, nil
}

func newOpenPGPSigner(ctx context.Context, content string) (*Signer, error) {
	gpg, err := lookTool("gpg", "OpenPGP")
	if err != nil {
		return nil, err
	}
	s, err := newSigner(FormatOpenPGP, "deps-gnupg-*")
	if err != nil {
		return nil, err
	}
	s.env = []string{"GNUPGHOME=" + s.dir}
	if _, err := tool(ctx, s.env, []byte(content), gpg, "--batch", "--no-tty", "--import"); err != nil {
		return nil, s.closeWith(err)
	}
	out, err := tool(ctx, s.env, nil, gpg, "--batch", "--no-tty", "--with-colons", "--list-secret-keys")
	if err != nil {
		return nil, s.closeWith(err)
	}
	fprs := primaryFingerprints(out)
	if len(fprs) != 1 {
		return nil, s.closeWith(fmt.Errorf("signing key: want exactly one OpenPGP secret key, got %d", len(fprs)))
	}
	s.Fingerprint = fprs[0]
	// A test signature fails for passphrase-protected or sign-incapable keys instead of
	// prompting (pinentry-mode error), so commit-tree cannot hang or fail later. The signature
	// goes to stdout (discarded): with --output /dev/null, gpg unlinks its output file when
	// signing fails, and as root that deletes /dev/null (seen in CI job containers).
	if _, err := tool(ctx, s.env, []byte("deps"), gpg, "--batch", "--no-tty", "--pinentry-mode", "error",
		"--local-user", s.Fingerprint, "--detach-sign", "--output", "-"); err != nil {
		return nil, s.closeWith(fmt.Errorf("signing with the OpenPGP key without a passphrase (protected keys are not supported): %w", err))
	}
	s.config = []string{"-c", "gpg.format=openpgp", "-c", "gpg.openpgp.program=" + gpg, "-c", "user.signingkey=" + s.Fingerprint}
	return s, nil
}

// primaryFingerprints returns the fingerprint of every primary secret key in gpg --with-colons
// output: the fpr record following each sec record.
func primaryFingerprints(colons string) []string {
	var fprs []string
	sec := false
	sc := bufio.NewScanner(strings.NewReader(colons))
	for sc.Scan() {
		f := strings.Split(sc.Text(), ":")
		switch {
		case f[0] == "sec":
			sec = true
		case f[0] == "fpr" && sec && len(f) > 9:
			fprs = append(fprs, f[9])
			sec = false
		case f[0] == "ssb":
			sec = false
		}
	}
	return fprs
}

func newSigner(format, pattern string) (*Signer, error) {
	dir, err := os.MkdirTemp("", pattern) // 0700
	if err != nil {
		return nil, err
	}
	return &Signer{Format: format, dir: dir}, nil
}

func (s *Signer) closeWith(err error) error {
	return errors.Join(err, s.Close())
}

// Close stops the gpg daemons of the temporary GNUPGHOME and removes the key material. It is
// safe to call on a nil Signer and more than once.
func (s *Signer) Close() error {
	if s == nil || s.dir == "" {
		return nil
	}
	if s.Format == FormatOpenPGP {
		// Independent of the (possibly canceled) run context: the agent must not outlive deps.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if gpgconf, err := exec.LookPath("gpgconf"); err == nil {
			_, _ = tool(ctx, s.env, nil, gpgconf, "--kill", "all")
			_, _ = tool(ctx, s.env, nil, gpgconf, "--remove-socketdir")
		}
		cancel()
	}
	err := os.RemoveAll(s.dir)
	s.dir = ""
	return err
}
