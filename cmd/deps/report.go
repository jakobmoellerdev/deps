package main

import (
	"cmp"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/bot"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
)

// jsonReport is the --report document: the summary as data. Field names are part of the
// documented interface (deps help output).
type jsonReport struct {
	Command      string         `json:"command"`
	Dependencies int            `json:"dependencies"`
	DurationMS   int64          `json:"durationMs"`
	PRs          []jsonPR       `json:"prs"`
	Other        []jsonAction   `json:"otherActions"`
	Pending      []jsonPending  `json:"heldBack"`
	Warnings     []jsonWarning  `json:"warnings"`
	Skipped      []jsonSkip     `json:"skipped"`
	Errors       []jsonError    `json:"errors"`
	Counts       map[string]int `json:"counts"`
}

type jsonPR struct {
	Branch    string       `json:"branch"`
	Title     string       `json:"title"`
	Status    string       `json:"status,omitempty"` // run only
	Automerge string       `json:"automerge,omitempty"`
	Number    int          `json:"number,omitzero"`
	URL       string       `json:"url,omitempty"`
	Waits     []string     `json:"waitsFor,omitempty"`
	Reason    string       `json:"reason,omitempty"`
	Updates   []jsonUpdate `json:"updates"`
}

type jsonUpdate struct {
	Name       string   `json:"name"`
	Manager    string   `json:"manager"`
	File       string   `json:"file"`
	Line       int      `json:"line,omitzero"`
	From       string   `json:"from,omitempty"`
	To         string   `json:"to,omitempty"`
	Type       string   `json:"type"`
	Lockfile   bool     `json:"lockfile,omitzero"`
	Advisories []string `json:"advisories,omitempty"`
}

type jsonAction struct {
	Branch    string `json:"branch,omitempty"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Number    int    `json:"number,omitzero"`
	URL       string `json:"url,omitempty"`
	Dashboard bool   `json:"dashboard,omitzero"`
}

type jsonPending struct {
	Name    string `json:"name"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Version string `json:"version"`
	Reason  string `json:"reason"`
}

type jsonWarning struct {
	Name   string `json:"name"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Kind   string `json:"kind"` // deprecated or abandoned
	Reason string `json:"reason"`
}

type jsonSkip struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type jsonError struct {
	Message string `json:"message"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitzero"`
}

func reportUpdate(u dep.Update) jsonUpdate {
	ju := jsonUpdate{Name: u.Dep.Name, Manager: u.Dep.Manager, File: u.Dep.File, Type: string(u.Type), Lockfile: u.Lockfile}
	if u.Maintenance() {
		ju.Type = "lockFileMaintenance"
		return ju
	}
	ju.Line = u.Dep.Line
	ju.From, ju.To = u.FromTo()
	for _, a := range u.Advisories {
		ju.Advisories = append(ju.Advisories, a.ID)
	}
	return ju
}

// counts are the numbers of the step outputs: updates, PRs and, for run, PR outcomes.
func (s *summary) counts() map[string]int {
	c := map[string]int{"updates": len(s.Res.Updates), "prs": len(s.Groups), "skipped": len(s.Res.Skipped), "heldBack": len(s.Res.Pending), "errors": len(s.Errors)}
	if !s.Run {
		return c
	}
	n := countActions(s.Actions)
	c["created"], c["updated"], c["closed"], c["upToDate"] = n.created, n.updated, n.closed, n.upToDate
	c["automerged"], c["prsSkipped"] = n.automerged, n.skipped
	return c
}

// outcomes tally the PR actions of a run by outcome; dashboard actions are not counted.
type outcomes struct {
	created, updated, closed, upToDate, skipped, automerged int
	// would and wouldAutomerge count dry-run actions; they stay out of the report.
	would, wouldAutomerge int
}

func countActions(actions []bot.Action) outcomes {
	var n outcomes
	for _, a := range actions {
		if a.Dashboard {
			continue
		}
		switch a.Automerge {
		case bot.AutomergeRequested, bot.AutomergeMerged:
			n.automerged++
		case bot.AutomergeWould:
			n.wouldAutomerge++
		}
		switch {
		case a.Status == bot.StatusCreated:
			n.created++
		case a.Status == bot.StatusUpdated || a.Status == bot.StatusRebased:
			n.updated++
		case a.Status == bot.StatusClosed || a.Status == bot.StatusDeleted:
			n.closed++
		case a.Status == bot.StatusUpToDate:
			n.upToDate++
		case strings.HasPrefix(a.Status, "skipped"):
			n.skipped++
		default:
			n.would++
		}
	}
	return n
}

func (s *summary) report() jsonReport {
	r := jsonReport{
		Command: s.Command, Dependencies: s.deps(), DurationMS: s.Elapsed.Milliseconds(),
		PRs: []jsonPR{}, Other: []jsonAction{}, Pending: []jsonPending{}, Warnings: []jsonWarning{}, Skipped: []jsonSkip{}, Errors: []jsonError{},
		Counts: s.counts(),
	}
	for _, g := range s.Groups {
		pr := jsonPR{Branch: g.Branch, Title: g.Title, Updates: make([]jsonUpdate, len(g.Updates))}
		for i, u := range g.Updates {
			pr.Updates[i] = reportUpdate(u)
		}
		if a, ok := s.action(g); ok {
			pr.Status, pr.Automerge, pr.Number, pr.URL, pr.Waits, pr.Reason = a.Status, a.Automerge, a.Number, a.URL, a.Waits, a.Reason
		}
		r.PRs = append(r.PRs, pr)
	}
	for _, a := range s.others() {
		r.Other = append(r.Other, jsonAction{Branch: a.Branch, Title: a.Title, Status: a.Display(), Number: a.Number, URL: a.URL, Dashboard: a.Dashboard})
	}
	for _, p := range s.Res.Pending {
		r.Pending = append(r.Pending, jsonPending{Name: p.Dep.Name, File: p.Dep.File, Line: p.Dep.Line, Version: p.Version, Reason: p.Reason})
	}
	for _, w := range s.Res.Warnings {
		r.Warnings = append(r.Warnings, jsonWarning{Name: w.Dep.Name, File: w.Dep.File, Line: w.Dep.Line, Kind: w.Kind, Reason: w.Reason})
	}
	for _, sr := range s.skipReasons() {
		r.Skipped = append(r.Skipped, jsonSkip{Reason: sr.Reason, Count: len(sr.Deps)})
	}
	for _, e := range s.Errors {
		je := jsonError{Message: e.Error()}
		if de, ok := errors.AsType[*engine.DepError](e); ok {
			je.File, je.Line = de.File, de.Line
		} else if fe, ok := errors.AsType[*engine.FileError](e); ok {
			je.File = fe.File
		}
		r.Errors = append(r.Errors, je)
	}
	return r
}

// writeReport writes the JSON report to path (replacing it).
func (s *summary) writeReport(path string) error {
	b, err := json.Marshal(s.report(), json.Deterministic(true))
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// writeOutputs appends the counts as step outputs to the GITHUB_OUTPUT file (GitHub, Gitea
// and Forgejo all provide it), one `<kebab-case name>=<n>` line each.
func (s *summary) writeOutputs(path string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	c := s.counts()
	var b strings.Builder
	for _, k := range outputNames {
		if n, ok := c[k]; ok {
			b.WriteString(kebab(k) + "=" + strconv.Itoa(n) + "\n")
		}
	}
	_, err = f.WriteString(b.String())
	return cmp.Or(err, f.Close())
}

// outputNames orders the step outputs.
var outputNames = []string{"updates", "prs", "created", "updated", "closed", "upToDate", "automerged", "prsSkipped", "heldBack", "skipped", "errors"}

func kebab(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('-')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// writeMachine writes the --report file and the step outputs, reporting failures on stderr.
func (s *summary) writeMachine(stderr io.Writer, reportPath string) {
	if reportPath != "" {
		if err := s.writeReport(reportPath); err != nil {
			fmt.Fprintf(stderr, "deps: report: %v\n", err)
		}
	}
	if p := os.Getenv("GITHUB_OUTPUT"); p != "" {
		if err := s.writeOutputs(p); err != nil {
			fmt.Fprintf(stderr, "deps: step outputs: %v\n", err)
		}
	}
}
