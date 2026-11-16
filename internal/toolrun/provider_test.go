package toolrun

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"testing"
)

// fake provides one tool and reports the others as not found.
type fake struct {
	tool, path string
	err        error
	env        []string
}

func (f fake) Lookup(_ context.Context, tool string) (string, error) {
	switch {
	case f.err != nil && tool == f.tool:
		return "", f.err
	case tool != f.tool:
		return "", fmt.Errorf("%s: %w", tool, exec.ErrNotFound)
	}
	return f.path, nil
}

func (f fake) Env(context.Context) []string { return f.env }

func TestLookupPrefersTheProvider(t *testing.T) {
	ctx := WithProvider(t.Context(), fake{tool: "node", path: "/opt/node/bin/node"})
	if got, err := Lookup(ctx, "node"); err != nil || got != "/opt/node/bin/node" {
		t.Errorf("Lookup(node) = %q, %v", got, err)
	}
	// A tool the provider does not know falls back to the machine.
	sh, shErr := exec.LookPath("sh")
	got, err := Lookup(ctx, "sh")
	if (err == nil) != (shErr == nil) || got != sh {
		t.Errorf("Lookup(sh) = %q, %v; want %q, %v", got, err, sh, shErr)
	}
	// A tool neither has keeps exec.LookPath's error, which every caller's
	// "<tool> not found" message tests for.
	if _, err := Lookup(ctx, "deps-no-such-tool"); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("Lookup(missing) = %v, want exec.ErrNotFound", err)
	}
}

func TestLookupReportsInstallFailures(t *testing.T) {
	boom := errors.New("checksum mismatch")
	ctx := WithProvider(t.Context(), fake{tool: "node", err: boom})
	if _, err := Lookup(ctx, "node"); !errors.Is(err, boom) {
		t.Errorf("Lookup = %v, want the install error, not a fallback", err)
	}
}

func TestEnvAddsProviderEntriesBeforeExtra(t *testing.T) {
	ctx := WithProvider(t.Context(), fake{env: []string{"PATH=/opt/node/bin", "JAVA_HOME=/opt/jdk"}})
	env := Env(ctx, "NO_COLOR=1")
	tail := env[len(env)-3:]
	if !slices.Equal(tail, []string{"PATH=/opt/node/bin", "JAVA_HOME=/opt/jdk", "NO_COLOR=1"}) {
		t.Errorf("Env ends with %q", tail)
	}
	if plain := Env(t.Context()); slices.Contains(plain, "JAVA_HOME=/opt/jdk") {
		t.Error("Env without a provider carries provider entries")
	}
}
