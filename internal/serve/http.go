package serve

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/serve/store"
)

// Handler is the daemon's HTTP surface: probes, metrics, the control API and the forge
// webhooks.
func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeText(w, http.StatusOK, "ok\n")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := d.Ready(r.Context()); err != nil {
			writeText(w, http.StatusServiceUnavailable, err.Error()+"\n")
			return
		}
		writeText(w, http.StatusOK, "ok\n")
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		d.metrics.SetGauge("deps_queue_due", d.dueAll.Get)
		if _, err := d.metrics.WriteTo(w); err != nil {
			slog.DebugContext(r.Context(), "writing metrics", "err", err)
		}
	})
	mux.HandleFunc("GET /api/v1/repos", d.authed(d.listRepos))
	mux.HandleFunc("GET /api/v1/repos/{id}", d.authed(d.getRepo))
	mux.HandleFunc("POST /api/v1/repos/{id}/run", d.authed(d.triggerRepo))
	mux.HandleFunc("GET /api/v1/runs", d.authed(d.listRuns))
	mux.HandleFunc("GET /api/v1/runs/{id}/report", d.authed(d.getReport))
	mux.HandleFunc("POST /webhooks/{forge}", d.webhook)
	return mux
}

// authed checks the API bearer token. The probes, the metrics and the webhooks are exempt:
// the first three carry no secret and the webhooks carry a signature of their own.
func (d *Daemon) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.apiToken != "" {
			given, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(given), []byte(d.apiToken)) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}
		next(w, r)
	}
}

func writeText(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("writing a response", "err", err)
	}
}

// repoJSON is a repository as the API renders it.
type repoJSON struct {
	ID         string    `json:"id"`
	Host       string    `json:"host"`
	FullName   string    `json:"fullName"`
	Forge      string    `json:"forge"`
	Mode       string    `json:"mode"`
	Enabled    bool      `json:"enabled"`
	IntervalMS int64     `json:"intervalMs"`
	NextDue    time.Time `json:"nextDue"`
	LastRunAt  time.Time `json:"lastRunAt,omitzero"`
	LastStatus string    `json:"lastStatus,omitempty"`
	FailCount  int       `json:"failCount"`
}

func toRepoJSON(r store.Repo) repoJSON {
	return repoJSON{
		ID: r.ID, Host: r.Host, FullName: r.FullName, Forge: r.Forge, Mode: string(r.Mode),
		Enabled: r.Enabled, IntervalMS: r.Interval.Milliseconds(), NextDue: r.NextDue,
		LastRunAt: r.LastRunAt, LastStatus: r.LastStatus, FailCount: r.FailCount,
	}
}

func (d *Daemon) listRepos(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cursor, _ := strconv.ParseUint(q.Get("cursor"), 10, 64)
	limit, _ := strconv.ParseInt(q.Get("limit"), 10, 64)
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	repos, next, err := d.st.Scan(r.Context(), cursor, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	status := q.Get("status")
	out := make([]repoJSON, 0, len(repos))
	for _, repo := range repos {
		if status != "" && repo.LastStatus != status {
			continue
		}
		out = append(out, toRepoJSON(repo))
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": out, "cursor": next})
}

// repoID reads the {id} path value, which is URL-escaped because an id holds slashes.
func repoID(r *http.Request) (string, error) {
	id, err := url.PathUnescape(r.PathValue("id"))
	if err != nil {
		return "", err
	}
	return strings.ToLower(id), nil
}

func (d *Daemon) getRepo(w http.ResponseWriter, r *http.Request) {
	id, err := repoID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	repo, err := d.st.Get(r.Context(), id)
	if errors.Is(err, store.ErrNoRepo) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such repository"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, toRepoJSON(repo))
}

func (d *Daemon) triggerRepo(w http.ResponseWriter, r *http.Request) {
	id, err := repoID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	ok, err := d.st.Enqueue(r.Context(), id, time.Now(), store.PriorityManual)
	switch {
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	case !ok:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such repository, or it is disabled"})
	default:
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "repository": id})
	}
}

// runJSON is a run as the API renders it.
type runJSON struct {
	ID         string    `json:"id"`
	Repository string    `json:"repository"`
	Worker     string    `json:"worker"`
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"startedAt"`
	DurationMS int64     `json:"durationMs"`
	Created    int       `json:"created"`
	Updated    int       `json:"updated"`
	Closed     int       `json:"closed"`
	UpToDate   int       `json:"upToDate"`
	Errors     int       `json:"errors"`
	Message    string    `json:"message,omitempty"`
}

func (d *Daemon) listRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.ParseInt(q.Get("limit"), 10, 64)
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	runs, err := d.st.Runs(r.Context(), strings.ToLower(q.Get("repo")), limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]runJSON, 0, len(runs))
	for _, run := range runs {
		out = append(out, runJSON{
			ID: run.ID, Repository: run.RepoID, Worker: run.Worker, Status: run.Status,
			StartedAt: run.StartedAt, DurationMS: run.Duration.Milliseconds(),
			Created: run.Created, Updated: run.Updated, Closed: run.Closed,
			UpToDate: run.UpToDate, Errors: run.Errors, Message: run.Message,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

func (d *Daemon) getReport(w http.ResponseWriter, r *http.Request) {
	body, err := d.st.Report(r.Context(), r.PathValue("id"))
	switch {
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	case body == nil:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no report; it may have expired"})
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}

// Enqueue brings a repository forward from outside the HTTP surface (the tests, and a
// future CLI trigger).
func (d *Daemon) Enqueue(ctx context.Context, id string, at time.Time, priority int) (bool, error) {
	return d.st.Enqueue(ctx, id, at, priority)
}
