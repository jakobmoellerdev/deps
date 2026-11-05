package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
)

// maxAnnotations is how many error and how many warning annotations GitHub shows per step;
// later ones are dropped by GitHub, so deps does not print them (the result lines remain).
const maxAnnotations = 10

// annotator writes GitHub Actions workflow commands: error and warning annotations, located
// at the file and line when known, and collapsible log groups. It is off outside GitHub
// Actions. Gitea and Forgejo runners also set GITHUB_ACTIONS but do not render annotations,
// so it is off there too, and the log stays free of raw "::error::" lines.
type annotator struct {
	on        bool
	w         io.Writer
	dir       string // --dir; dependency files are relative to it
	workspace string // GITHUB_WORKSPACE; annotation paths are relative to it

	mu          sync.Mutex
	errs, warns int
}

// actionsLog is the annotator of the running command, set up by the root command.
var actionsLog = &annotator{}

func newAnnotator(getenv func(string) string, w io.Writer, dir string) *annotator {
	on := getenv("GITHUB_ACTIONS") == "true" && getenv("GITEA_ACTIONS") != "true" && getenv("FORGEJO_ACTIONS") != "true"
	return &annotator{on: on, w: w, dir: dir, workspace: getenv("GITHUB_WORKSPACE")}
}

// path makes p (relative to base, or absolute) relative to the workspace, which is where
// GitHub resolves annotation paths.
func (a *annotator) path(base, p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	ws := a.workspace
	if ws == "" {
		ws = "."
	}
	absWS, err1 := filepath.Abs(ws)
	absP, err2 := filepath.Abs(p)
	if err1 != nil || err2 != nil {
		return filepath.ToSlash(p)
	}
	rel, err := filepath.Rel(absWS, absP)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(absP)
	}
	return filepath.ToSlash(rel)
}

// command prints one annotation unless its kind's budget is used up.
func (a *annotator) command(kind, file string, line int, msg string) {
	if a == nil || !a.on {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	n := &a.errs
	if kind == "warning" {
		n = &a.warns
	}
	if *n >= maxAnnotations {
		return
	}
	*n++
	var props []string
	if file != "" {
		props = append(props, "file="+escapeProperty(file))
		if line > 0 {
			props = append(props, "line="+strconv.Itoa(line))
		}
	}
	props = append(props, "title=deps")
	fmt.Fprintf(a.w, "::%s %s::%s\n", kind, strings.Join(props, ","), escapeData(msg))
}

// error annotates err: a dependency error at its file and line, a config error at each
// problem's line in the config file, anything else without a location.
func (a *annotator) error(err error) {
	if a == nil || !a.on {
		return
	}
	var de *engine.DepError
	var ce *config.Error
	switch {
	case errors.As(err, &de):
		a.command("error", a.path(a.dir, de.File), de.Line, de.Name+": "+de.Err.Error())
	case errors.As(err, &ce) && ce.File != "" && len(ce.Problems) > 0:
		for _, p := range ce.Problems {
			a.command("error", a.path(".", ce.File), p.Line, p.Msg)
		}
	default:
		a.command("error", "", 0, err.Error())
	}
}

// group starts a collapsible log group; endGroup ends it.
func (a *annotator) group(title string) {
	if a != nil && a.on {
		fmt.Fprintf(a.w, "::group::%s\n", escapeData(title))
	}
}

func (a *annotator) endGroup() {
	if a != nil && a.on {
		fmt.Fprintln(a.w, "::endgroup::")
	}
}

func escapeData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

func escapeProperty(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}

// annotatingHandler passes records to the next handler and annotates warnings and errors
// with their message and attributes.
type annotatingHandler struct {
	slog.Handler
	a     *annotator
	attrs []slog.Attr
}

func (h *annotatingHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		var b strings.Builder
		b.WriteString(r.Message)
		add := func(at slog.Attr) bool {
			fmt.Fprintf(&b, " %s=%v", at.Key, at.Value)
			return true
		}
		for _, at := range h.attrs {
			add(at)
		}
		r.Attrs(add)
		kind := "warning"
		if r.Level >= slog.LevelError {
			kind = "error"
		}
		h.a.command(kind, "", 0, b.String())
	}
	return h.Handler.Handle(ctx, r)
}

func (h *annotatingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &annotatingHandler{Handler: h.Handler.WithAttrs(attrs), a: h.a, attrs: append(append([]slog.Attr(nil), h.attrs...), attrs...)}
}

func (h *annotatingHandler) WithGroup(name string) slog.Handler {
	return &annotatingHandler{Handler: h.Handler.WithGroup(name), a: h.a, attrs: h.attrs}
}
