// Package hexpmbob lists the Elixir and Erlang/OTP builds of bob, Hex's build service
// (builds.hex.pm), which setup-beam, asdf, mise and the official Docker images install from.
package hexpmbob

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Name is the datasource name (Renovate's).
const Name = "hexpm-bob"

// DefaultURL is bob's build host; DEPS_HEXPM_BOB_URL overrides it (a mirror).
const DefaultURL = "https://builds.hex.pm"

var (
	// otpRe matches the OTP build names: `otp/<os>-<version>` (`otp/ubuntu-22.04`).
	otpRe = regexp.MustCompile(`^otp/\w+-\d+\.\d+$`)
	// elixirRef is a release build of Elixir (`v1.18.4`, `v1.18.0-rc.0`, `v1.18.4-otp-27`):
	// branch builds (`main`, `v1.18`, `main-otp-27`) are not.
	elixirRef = regexp.MustCompile(`^v(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)$`)
	// otpRef is a release build of Erlang/OTP (`OTP-27.3.4.1`, `OTP-27.0-rc1`): branch builds
	// (`maint`, `maint-27`, `master`) are not.
	otpRef = regexp.MustCompile(`^OTP-(\d+(?:\.\d+)*(?:-rc\d+)?)$`)
)

// Source is the hexpm-bob datasource.
type Source struct {
	client *http.Client
	sem    chan struct{}
	base   string // build host base URL without a trailing slash

	lists datasource.Memo[string, *list] // by package name
}

// list is the releases of a build list with the build time of each.
type list struct {
	rels  []datasource.Release
	times map[string]time.Time
}

// New returns the hexpm-bob datasource. sem bounds concurrent requests (nil = unbounded);
// getenv supplies DEPS_HEXPM_BOB_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	return &Source{client: client, sem: sem, base: strings.TrimSuffix(cmp.Or(getenv("DEPS_HEXPM_BOB_URL"), DefaultURL), "/")}
}

// Releases lists the release builds of d.Name, `elixir` or `otp/<os>-<version>`, in the
// list's order: Elixir versions without their `v` (`1.18.4`, also per-OTP builds
// `1.18.4-otp-27`), OTP versions without `OTP-` (`27.3.4.1`). The result is shared and must
// not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	l, err := s.lookup(ctx, d.Name)
	if err != nil {
		return nil, err
	}
	return l.rels, nil
}

// ReleaseTime implements datasource.Timer with the build time the list records for version;
// zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	l, err := s.lookup(ctx, d.Name)
	if err != nil {
		return time.Time{}, err
	}
	return l.times[version], nil
}

// SourceRepo implements datasource.SourceRepo: elixir-lang/elixir for Elixir, erlang/otp for
// OTP builds.
func (*Source) SourceRepo(_ context.Context, d dep.Dep, _ string) (string, error) {
	switch {
	case d.Name == "elixir":
		return "https://github.com/elixir-lang/elixir", nil
	case otpRe.MatchString(d.Name):
		return "https://github.com/erlang/otp", nil
	}
	return "", nil
}

// Digest is unsupported: builds are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// lookup fetches `GET <base>/builds/<name>/builds.txt` once per name: one build per line,
// `<ref> <commit> <build time> [<sha256>]`.
func (s *Source) lookup(ctx context.Context, name string) (*list, error) {
	ref := elixirRef
	switch {
	case name == "elixir":
	case otpRe.MatchString(name):
		ref = otpRef
	default:
		return nil, fmt.Errorf("invalid hexpm-bob name %q (want elixir or otp/<os>-<version>, e.g. otp/ubuntu-22.04)", name)
	}
	return s.lists.Do(name, func() (*list, error) {
		start := time.Now()
		u := s.base + "/builds/" + name + "/builds.txt"
		l := &list{times: map[string]time.Time{}}
		err := datasource.GetStream(ctx, s.client, s.sem, u, func(r io.Reader) error {
			sc := bufio.NewScanner(r)
			for sc.Scan() {
				f := strings.Fields(sc.Text())
				if len(f) < 3 {
					continue
				}
				m := ref.FindStringSubmatch(f[0])
				if m == nil {
					continue
				}
				if _, dup := l.times[m[1]]; dup {
					continue
				}
				l.rels = append(l.rels, datasource.Release{Version: m[1]})
				t, _ := time.Parse(time.RFC3339, f[2])
				l.times[m[1]] = t.UTC()
			}
			if err := sc.Err(); err != nil {
				return err
			}
			if len(l.rels) == 0 {
				return errors.New("no releases")
			}
			return nil
		})
		slog.DebugContext(ctx, "hexpm-bob builds", "name", name, "releases", len(l.rels), "duration", time.Since(start), "err", err)
		if err != nil {
			return nil, err
		}
		return l, nil
	})
}
