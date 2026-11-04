package mavends

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// securityPassphrase decrypts the master password of settings-security.xml (Maven's
// DefaultSecDispatcher.SYSTEM_PROPERTY_SEC_LOCATION).
const securityPassphrase = "settings.security"

// encryptedRe matches plexus-cipher's decorated form: the text between the first unescaped
// `{` and the next `}` not preceded by a backslash (DefaultPlexusCipher.ENCRYPTED_STRING_PATTERN).
var encryptedRe = regexp.MustCompile(`(?s)(?:^|[^\\])\{(.*?[^\\])\}`)

// encrypted returns the base64 payload of a Maven-encrypted `{...}` value.
func encrypted(s string) (string, bool) {
	m := encryptedRe.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// plexusDecrypt reverses plexus-cipher's PBECipher.encrypt64: base64 of salt (8 bytes), the
// padding length n (1 byte), the AES-128-CBC/PKCS5 ciphertext and n random bytes; key and IV
// are the two halves of SHA-256(password || salt).
func plexusDecrypt(payload, password string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(payload), ""))
	if err != nil {
		return "", fmt.Errorf("malformed encrypted value: %w", err)
	}
	const saltSize = 8
	if len(raw) < saltSize+1 {
		return "", errors.New("malformed encrypted value: too short")
	}
	n := len(raw) - saltSize - 1 - int(raw[saltSize])
	if n <= 0 || n%aes.BlockSize != 0 {
		return "", errors.New("malformed encrypted value: bad length")
	}
	h := sha256.New()
	h.Write([]byte(password))
	h.Write(raw[:saltSize])
	keyIV := h.Sum(nil)
	block, err := aes.NewCipher(keyIV[:16])
	if err != nil {
		return "", err
	}
	plain := make([]byte, n)
	cipher.NewCBCDecrypter(block, keyIV[16:]).CryptBlocks(plain, raw[saltSize+1:saltSize+1+n])
	p := int(plain[n-1])
	if p == 0 || p > aes.BlockSize {
		return "", errors.New("decryption failed (wrong master password?)")
	}
	for _, b := range plain[n-p:] {
		if int(b) != p {
			return "", errors.New("decryption failed (wrong master password?)")
		}
	}
	return string(plain[:n-p]), nil
}

// decryptValue returns s, decrypted with the master password when it is encrypted.
func (s *Source) decryptValue(v string) (string, error) {
	payload, ok := encrypted(v)
	if !ok {
		return v, nil
	}
	master, err := s.masterPassword()
	if err != nil {
		return "", err
	}
	return plexusDecrypt(payload, master)
}

// masterPassword reads and decrypts the `<master>` of settings-security.xml
// ($DEPS_MAVEN_SETTINGS_SECURITY, else next to the user settings), following `<relocation>`.
func (s *Source) masterPassword() (string, error) {
	s.masterOnce.Do(func() {
		p := s.getenv("DEPS_MAVEN_SETTINGS_SECURITY")
		if p == "" {
			p = filepath.Join(filepath.Dir(s.userSettingsPath()), "settings-security.xml")
		}
		for range 4 {
			var sec struct {
				Master     string `xml:"master"`
				Relocation string `xml:"relocation"`
			}
			b, err := os.ReadFile(p)
			if err == nil {
				err = xml.Unmarshal(b, &sec)
			}
			if err != nil {
				s.masterErr = fmt.Errorf("maven master password: %w", err)
				return
			}
			if sec.Relocation = strings.TrimSpace(sec.Relocation); sec.Relocation != "" {
				p = sec.Relocation
				continue
			}
			payload, ok := encrypted(sec.Master)
			if !ok {
				s.masterErr = fmt.Errorf("maven master password: %s has no encrypted <master>", p)
				return
			}
			s.master, err = plexusDecrypt(payload, securityPassphrase)
			if err != nil {
				s.masterErr = fmt.Errorf("maven master password %s: %w", p, err)
			}
			return
		}
		s.masterErr = fmt.Errorf("maven master password: too many relocations at %s", p)
	})
	return s.master, s.masterErr
}
