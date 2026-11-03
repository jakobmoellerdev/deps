package main

import (
	"fmt"
	"os"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/bot"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
)

// appendSummary appends markdown to the CI step summary file, if the runner provides one.
func appendSummary(md string) {
	p := os.Getenv("GITHUB_STEP_SUMMARY")
	if p == "" {
		p = os.Getenv("GITEA_STEP_SUMMARY")
	}
	if p == "" {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "deps: step summary: %v\n", err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(md); err != nil {
		fmt.Fprintf(os.Stderr, "deps: step summary: %v\n", err)
	}
}

func writeUpdateSummary(res *engine.Result) {
	var b strings.Builder
	b.WriteString("## deps\n\n| Dependency | File | From | To | Type |\n|---|---|---|---|---|\n")
	for _, u := range res.Updates {
		from, to := u.FromTo()
		fmt.Fprintf(&b, "| `%s` | `%s:%d` | `%s` | `%s` | %s |\n", u.Dep.Name, u.Dep.File, u.Dep.Line, from, to, u.Type)
	}
	appendSummary(b.String())
}

func writeRunSummary(actions []bot.Action) {
	var b strings.Builder
	b.WriteString("## deps\n\n| Dependency | Status | PR |\n|---|---|---|\n")
	for _, a := range actions {
		pr := ""
		if a.URL != "" {
			pr = fmt.Sprintf("[#%d](%s)", a.Number, a.URL)
		}
		status := a.Display()
		if a.Dashboard {
			status = "dashboard " + status
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", a.Title, status, pr)
	}
	appendSummary(b.String())
}
