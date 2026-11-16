package toolrun

import (
	"context"
	"errors"
	"os/exec"
)

// Provider resolves the ecosystem tools (node, go, cargo, java, terraform, …) of one unit of
// work. Without one every lookup is exec.LookPath, which is what the CLI does: it runs the
// tools of the machine it was started on. `deps serve` installs a provider per cycle that
// downloads what the repository declares into its cache volume, so the image stays slim.
//
// Credential helpers (gh, git credential, ssh-keygen, gpg) are not ecosystem tools and are
// never looked up through a Provider: they must stay the image's own binaries.
type Provider interface {
	// Lookup returns the absolute path of tool, installing it on demand. It returns an error
	// wrapping exec.ErrNotFound when the tool is not one it can provide, in which case the
	// caller falls back to exec.LookPath.
	Lookup(ctx context.Context, tool string) (string, error)
	// Env are the KEY=value entries the tools provided so far need: the PATH prefix of their
	// binaries and their own variables (JAVA_HOME, GOROOT, CARGO_HOME, DOTNET_ROOT, …). They
	// are one set rather than one per tool because tools need each other's: npx needs node
	// on PATH, gradle needs JAVA_HOME.
	Env(ctx context.Context) []string
}

type providerKey struct{}

// WithProvider returns ctx carrying p: Lookup and Env resolve through it for every call made
// with ctx or a context derived from it.
func WithProvider(ctx context.Context, p Provider) context.Context {
	return context.WithValue(ctx, providerKey{}, p)
}

// provider returns the Provider of ctx, nil without one.
func provider(ctx context.Context) Provider {
	p, _ := ctx.Value(providerKey{}).(Provider)
	return p
}

// Lookup resolves the absolute path of an ecosystem tool: through the Provider of ctx when
// it has one, else with exec.LookPath, which is also where a provider that does not know the
// tool falls back to. A tool neither can find yields exec.LookPath's error, which wraps
// exec.ErrNotFound, so every caller's "<tool> not found" message keeps its trigger condition.
func Lookup(ctx context.Context, tool string) (string, error) {
	if p := provider(ctx); p != nil {
		switch path, err := p.Lookup(ctx, tool); {
		case err == nil:
			return path, nil
		case !errors.Is(err, exec.ErrNotFound):
			// A failed install (checksum mismatch, download error) is not "no such tool":
			// falling back to the machine would hide it.
			return "", err
		}
	}
	return exec.LookPath(tool)
}
