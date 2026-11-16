package serve

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/serve/store"
)

// maxWebhookBody bounds a delivery; GitHub's own limit is 25 MB, and deps only reads a few
// fields out of it.
const maxWebhookBody = 5 << 20

// webhookDebounce is how far out an accepted delivery schedules the cycle. A burst of pushes
// to the same repository collapses into one cycle, because the enqueue only ever brings the
// due time forward.
const webhookDebounce = 30 * time.Second

// webhook handles a forge delivery: the signature is verified over the raw body before
// anything is parsed, so an unsigned payload never reaches the decoder.
func (d *Daemon) webhook(w http.ResponseWriter, r *http.Request) {
	forge := r.PathValue("forge")
	f := d.forgeOf(forge)
	if f == nil {
		d.countWebhook(forge, "invalid")
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such forge"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		d.countWebhook(forge, "invalid")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unreadable body"})
		return
	}
	if !verifySignature(r.Header, body, d.secrets[forge]) {
		d.countWebhook(forge, "invalid")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad signature"})
		return
	}
	event := eventName(r.Header)
	ids, refresh := d.webhookTargets(f, event, body)
	if refresh {
		// An installation changed: the inventory has to list it again. The request's context
		// ends with the response, so the refresh gets one that outlives it.
		ctx := context.WithoutCancel(r.Context())
		go func() {
			if err := d.refreshInventory(ctx); err != nil {
				slog.WarnContext(ctx, "inventory refresh after an installation event", "err", err)
			}
		}()
	}
	if len(ids) == 0 {
		d.countWebhook(forge, "ignored")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	at := time.Now().Add(webhookDebounce)
	queued := 0
	for _, id := range ids {
		ok, err := d.st.Enqueue(r.Context(), id, at, store.PriorityWebhook)
		if err != nil {
			d.countWebhook(forge, "invalid")
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if ok {
			queued++
		}
	}
	if queued == 0 {
		d.countWebhook(forge, "ignored")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	d.countWebhook(forge, "accepted")
	slog.InfoContext(r.Context(), "webhook", "forge", forge, "event", event, "repositories", ids)
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": ids})
}

func (d *Daemon) countWebhook(forge, result string) {
	d.metrics.Inc("deps_webhooks_total", map[string]string{"forge": forge, "result": result})
}

// eventName is the event of a delivery, whatever forge sent it.
func eventName(h http.Header) string {
	for _, k := range []string{"X-GitHub-Event", "X-Gitea-Event", "X-Forgejo-Event", "X-Gitlab-Event"} {
		if v := h.Get(k); v != "" {
			return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(v), " Hook"))
		}
	}
	return ""
}

// verifySignature checks the delivery against the configured secret. Without a secret the
// endpoint refuses everything: an unauthenticated trigger that can make the daemon clone and
// run repository code is not something to leave open by default.
func verifySignature(h http.Header, body []byte, secret string) bool {
	if secret == "" {
		return false
	}
	// GitHub, Gitea and Forgejo: hex HMAC-SHA256 over the raw body.
	for _, k := range []string{"X-Hub-Signature-256", "X-Gitea-Signature", "X-Forgejo-Signature"} {
		v := h.Get(k)
		if v == "" {
			continue
		}
		got, err := hex.DecodeString(strings.TrimPrefix(v, "sha256="))
		if err != nil {
			return false
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		return hmac.Equal(got, mac.Sum(nil))
	}
	// GitLab sends the secret itself.
	if v := h.Get("X-Gitlab-Token"); v != "" {
		return subtle.ConstantTimeCompare([]byte(v), []byte(secret)) == 1
	}
	return false
}

// hookPayload is the part of a delivery deps reads; the field names of GitHub, Gitea,
// Forgejo and GitLab overlap enough for one shape.
type hookPayload struct {
	Ref        string `json:"ref"`
	Action     string `json:"action"`
	Repository struct {
		FullName          string `json:"full_name"`
		PathWithNamespace string `json:"path_with_namespace"`
		DefaultBranch     string `json:"default_branch"`
		HTMLURL           string `json:"html_url"`
	} `json:"repository"`
	Project struct {
		PathWithNamespace string `json:"path_with_namespace"`
		DefaultBranch     string `json:"default_branch"`
	} `json:"project"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	ObjectAttributes struct {
		Action string `json:"action"`
	} `json:"object_attributes"`
}

// webhookTargets decides what a delivery means: which repositories to bring forward, and
// whether the inventory has to be listed again.
//
// The events that matter are the ones that change what a cycle would do: a push to the base
// branch, an edit of the dashboard issue (which is how a checkbox becomes an update within
// seconds instead of a day), and a pull request that was closed, reopened or edited (the
// rebase checkbox, the stop-updating label).
func (d *Daemon) webhookTargets(f *Forge, event string, body []byte) (ids []string, refresh bool) {
	var p hookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, false
	}
	switch event {
	case "installation", "installation_repositories":
		return nil, true
	case "push", "issues", "issue_comment", "pull_request", "merge_request", "pull_request_review", "note":
	default:
		return nil, false
	}
	fullName := p.Repository.FullName
	if fullName == "" {
		fullName = cmpOr(p.Repository.PathWithNamespace, p.Project.PathWithNamespace)
	}
	if fullName == "" {
		return nil, false
	}
	if event == "push" {
		base := cmpOr(p.Repository.DefaultBranch, p.Project.DefaultBranch)
		branch := strings.TrimPrefix(p.Ref, "refs/heads/")
		if rule := f.Match(fullName); rule != nil && rule.Base != "" {
			base = rule.Base
		}
		// A push to one of deps' own update branches is deps' own work; only the base
		// branch changes what the next cycle would do.
		if base != "" && branch != base {
			return nil, false
		}
	}
	if rule := f.Match(fullName); rule == nil || !rule.Managed() {
		return nil, false
	}
	return []string{store.RepoID(hostOf(f.ServerURL), fullName)}, false
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
