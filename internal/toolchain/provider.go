package toolchain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
)

// For returns the tool provider of one unit of work (one `deps serve` cycle): it installs
// what want declares, else what the Manager pins, and remembers the environment of every
// tool it installed, so a later tool run sees the ones before it (gradle needs JAVA_HOME,
// npx needs node on PATH).
func (m *Manager) For(want map[Tool]string) *Provider {
	return &Provider{m: m, want: want}
}

// Provider resolves the tools of one unit of work; it implements toolrun.Provider.
type Provider struct {
	m    *Manager
	want map[Tool]string

	mu    sync.Mutex
	order []Tool
	envs  map[Tool][]string
	paths []string
}

// Lookup installs the tool providing the command name and returns its path. A command no
// catalog entry provides, and one whose version neither the repository nor a pin names,
// yield an error wrapping exec.ErrNotFound, so the caller falls back to the machine and
// reports the tool as missing exactly as the CLI does.
func (p *Provider) Lookup(ctx context.Context, name string) (string, error) {
	tool, ok := p.m.toolOf(name)
	if !ok {
		return "", fmt.Errorf("%s: %w", name, exec.ErrNotFound)
	}
	in, err := p.m.Ensure(ctx, tool, p.want[tool])
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			slog.DebugContext(ctx, "toolchain not available", "tool", tool, "command", name, "err", err)
		}
		return "", err
	}
	bin, ok := in.Bins[name]
	if !ok {
		return "", fmt.Errorf("%s: %w", name, exec.ErrNotFound)
	}
	if _, err := os.Stat(bin); err != nil {
		return "", fmt.Errorf("%s of %s %s: %w", name, tool, in.Version, err)
	}
	p.record(in)
	return bin, nil
}

// record keeps the installation's environment for Env.
func (p *Provider) record(in Installed) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.envs == nil {
		p.envs = map[Tool][]string{}
	}
	if _, seen := p.envs[in.Tool]; seen {
		return
	}
	p.envs[in.Tool] = in.Env
	p.order = append(p.order, in.Tool)
	for _, kv := range in.Env {
		if dirs, ok := strings.CutPrefix(kv, "PATH="); ok {
			first, _, _ := strings.Cut(dirs, string(os.PathListSeparator))
			if first != "" && !slices.Contains(p.paths, first) {
				p.paths = append(p.paths, first)
			}
		}
	}
}

// Env are the entries every tool installed so far needs. The PATH of all of them is merged
// into one entry, in install order, in front of the inherited PATH; the other variables
// follow in the same order, so a later tool's JAVA_HOME or GOROOT wins.
func (p *Provider) Env(context.Context) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.order) == 0 {
		return nil
	}
	out := []string{pathEnv(p.paths...)}
	for _, t := range p.order {
		for _, kv := range p.envs[t] {
			if !strings.HasPrefix(kv, "PATH=") {
				out = append(out, kv)
			}
		}
	}
	return out
}

// Installed lists the tools this provider installed, for the cycle's log record.
func (p *Provider) Installed() []Tool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.order)
}
