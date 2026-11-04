package mavends

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// gradleAuth returns the credentials of the Gradle repository named id by Gradle's
// `credentials(PasswordCredentials)` convention: the project properties `<id>Username` and
// `<id>Password`, from ORG_GRADLE_PROJECT_<property> or $GRADLE_USER_HOME/gradle.properties
// (default $HOME/.gradle). nil when neither is set.
func (s *Source) gradleAuth(id string) (*auth, error) {
	s.gradleOnce.Do(func() {
		home := s.getenv("GRADLE_USER_HOME")
		if home == "" {
			if h := s.getenv("HOME"); h != "" {
				home = filepath.Join(h, ".gradle")
			}
		}
		if home == "" {
			return
		}
		p := filepath.Join(home, "gradle.properties")
		b, err := os.ReadFile(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			s.gradleErr = fmt.Errorf("gradle user properties: %w", err)
		default:
			s.gradleProps = parseProperties(b)
		}
	})
	if s.gradleErr != nil {
		return nil, s.gradleErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.gradleAuths[id]; ok {
		return a, nil
	}
	prop := func(name string) string {
		if v := s.getenv("ORG_GRADLE_PROJECT_" + name); v != "" {
			return v
		}
		return s.gradleProps[name]
	}
	var a *auth
	if user, pass := prop(id+"Username"), prop(id+"Password"); user != "" || pass != "" {
		a = &auth{user: user, pass: pass}
	}
	s.gradleAuths[id] = a
	return a, nil
}

// parseProperties reads a Java properties file: `key=value`, `key: value` or `key value`,
// `#`/`!` comments, backslash line continuations and escapes (`\t`, `\n`, `\uXXXX`, `\x` = x).
func parseProperties(b []byte) map[string]string {
	props := map[string]string{}
	var logical []byte
	for raw := range bytes.SplitSeq(b, []byte("\n")) {
		line := bytes.TrimLeft(bytes.TrimRight(raw, "\r"), " \t\f")
		if len(logical) == 0 && (len(line) == 0 || line[0] == '#' || line[0] == '!') {
			continue
		}
		if continued(line) {
			logical = append(logical, line[:len(line)-1]...)
			continue
		}
		logical = append(logical, line...)
		k, v := splitProperty(logical)
		props[k] = v
		logical = logical[:0]
	}
	if len(logical) > 0 {
		k, v := splitProperty(logical)
		props[k] = v
	}
	return props
}

// continued reports whether line ends in an odd number of backslashes.
func continued(line []byte) bool {
	n := 0
	for i := len(line) - 1; i >= 0 && line[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}

func splitProperty(line []byte) (key, value string) {
	i := 0
	for ; i < len(line); i++ {
		c := line[i]
		if c == '\\' {
			i++
			continue
		}
		if c == '=' || c == ':' || c == ' ' || c == '\t' || c == '\f' {
			break
		}
	}
	key = unescapeProperty(line[:min(i, len(line))])
	rest := bytes.TrimLeft(line[min(i, len(line)):], " \t\f")
	if len(rest) > 0 && (rest[0] == '=' || rest[0] == ':') {
		rest = bytes.TrimLeft(rest[1:], " \t\f")
	}
	return key, unescapeProperty(rest)
}

func unescapeProperty(b []byte) string {
	if bytes.IndexByte(b, '\\') < 0 {
		return string(b)
	}
	var sb strings.Builder
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' || i+1 == len(b) {
			sb.WriteByte(b[i])
			continue
		}
		i++
		switch b[i] {
		case 't':
			sb.WriteByte('\t')
		case 'n':
			sb.WriteByte('\n')
		case 'r':
			sb.WriteByte('\r')
		case 'f':
			sb.WriteByte('\f')
		case 'u':
			if i+4 < len(b) {
				if r, err := strconv.ParseUint(string(b[i+1:i+5]), 16, 32); err == nil {
					sb.WriteRune(rune(r))
					i += 4
					continue
				}
			}
			sb.WriteByte('u')
		default:
			sb.WriteByte(b[i])
		}
	}
	return sb.String()
}
