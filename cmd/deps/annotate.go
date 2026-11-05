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
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
)

// maxAnnotations is how many error, how many warning and how many notice annotations GitHub
// shows per step; later ones are dropped by GitHub, so deps does not print them (the result
// lines remain).
const maxAnnotations = 10

// annotator writes GitHub/Gitea Actions workflow commands: error, warning and notice
// annotations, located at the file and line when known, collapsible log groups and secret
// masks. It is on with --log-format actions, which auto selects on an actionsRunner, and
// writes nothing otherwise.
type annotator struct {
	on        bool
	runner    bool // actionsRunner: auto selects the actions format
	w         io.Writer
	dir       string // --dir; dependency files are relative to it
	workspace string // GITHUB_WORKSPACE; annotation paths are relative to it

	mu                   sync.Mutex
	errs, warns, notices int
}

// actionsLog is the annotator of the running command, set up by the root command and
// switched on by setupLogger.
var actionsLog = &annotator{}

// newAnnotator returns an annotator, off until setupLogger picks the actions format.
func newAnnotator(getenv func(string) string, w io.Writer, dir string) *annotator {
	return &annotator{runner: actionsRunner(getenv), w: w, dir: dir, workspace: getenv("GITHUB_WORKSPACE")}
}

// actionsRunner reports whether deps runs on an Actions runner whose log view renders
// workflow commands: GitHub Actions, and Gitea Actions (GITEA_ACTIONS; Gitea 1.26 shows
// groups and severity-labeled annotation lines, and its runners also set GITHUB_ACTIONS).
// Forgejo (FORGEJO_ACTIONS) prints workflow commands literally, so it is none.
func actionsRunner(getenv func(string) string) bool {
	return getenv("FORGEJO_ACTIONS") != "true" && (getenv("GITHUB_ACTIONS") == "true" || getenv("GITEA_ACTIONS") == "true")
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
	switch kind {
	case "warning":
		n = &a.warns
	case "notice":
		n = &a.notices
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

// error annotates err: a dependency error at its file and line, a file error at its file, a
// config error at each problem's line in the config file, anything else without a location.
func (a *annotator) error(err error) {
	if a == nil || !a.on {
		return
	}
	var de *engine.DepError
	var ce *config.Error
	var fe *engine.FileError
	switch {
	case errors.As(err, &de):
		a.command("error", a.path(a.dir, de.File), de.Line, de.Name+": "+de.Err.Error())
	case errors.As(err, &fe):
		a.command("error", a.path(a.dir, fe.File), 0, fe.Err.Error())
	case errors.As(err, &ce) && ce.File != "" && len(ce.Problems) > 0:
		for _, p := range ce.Problems {
			a.command("error", a.path(".", ce.File), p.Line, p.Msg)
		}
	default:
		a.command("error", "", 0, err.Error())
	}
}

// security annotates a security update at its dependency's file and line with a notice.
func (a *annotator) security(u dep.Update) {
	if a == nil || !a.on || len(u.Advisories) == 0 {
		return
	}
	from, to := u.FromTo()
	ids := make([]string, len(u.Advisories))
	for i, adv := range u.Advisories {
		ids[i] = adv.ID
		if adv.Summary != "" {
			ids[i] += " (" + adv.Summary + ")"
		}
	}
	a.command("notice", a.path(a.dir, u.Dep.File), u.Dep.Line, fmt.Sprintf("security update %s %s -> %s: %s", u.Dep.Name, from, to, strings.Join(ids, ", ")))
}

// mask hides secret in the rest of the job log (::add-mask::). Call it before secret is used.
func (a *annotator) mask(secret string) {
	if a != nil && a.on && secret != "" {
		fmt.Fprintf(a.w, "::add-mask::%s\n", escapeData(secret))
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
