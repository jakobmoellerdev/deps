package serve

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/ghapp"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/serve/store"
)

// schedulerLoop runs the duties only one replica may do: refreshing the repository
// inventory and smearing the due times of the repositories it adds. The lock makes it one
// replica at a time; losing it mid-pass is harmless, since every write is idempotent.
func (d *Daemon) schedulerLoop(ctx context.Context) {
	renew := time.NewTicker(10 * time.Second)
	defer renew.Stop()
	refresh := time.NewTicker(d.cfg.InventoryInterval.D())
	defer refresh.Stop()
	d.tryLead(ctx)
	if d.leader.Load() {
		d.refresh(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-renew.C:
			d.tryLead(ctx)
		case <-refresh.C:
			if d.leader.Load() {
				d.refresh(ctx)
			}
		}
	}
}

// tryLead takes or renews the scheduler lock.
func (d *Daemon) tryLead(ctx context.Context) {
	ok, err := d.st.Lock(ctx, schedulerLock, d.worker, 30*time.Second)
	if err != nil {
		slog.WarnContext(ctx, "scheduler lock", "err", err)
		return
	}
	if ok != d.leader.Load() {
		slog.InfoContext(ctx, "scheduler role", "leader", ok, "worker", d.worker)
	}
	d.leader.Store(ok)
}

func (d *Daemon) refresh(ctx context.Context) {
	if err := d.refreshInventory(ctx); err != nil {
		d.metrics.Inc("deps_inventory_refresh_total", map[string]string{"result": "failed"})
		slog.ErrorContext(ctx, "inventory refresh", "err", err)
		return
	}
	d.metrics.Inc("deps_inventory_refresh_total", map[string]string{"result": "ok"})
}

// refreshInventory lists the repositories of every forge, adds the new ones with a smeared
// first due time, and disables the ones that disappeared or stopped matching. A disabled
// repository keeps its record, so its run history stays readable.
func (d *Daemon) refreshInventory(ctx context.Context) error {
	start := time.Now()
	client := httpx.NewClient()
	seen := map[string]bool{}
	var added, disabled int
	for i := range d.cfg.Forges {
		f := &d.cfg.Forges[i]
		repos, err := d.listForge(ctx, f, client)
		if err != nil {
			return err
		}
		for _, r := range repos {
			seen[r.ID] = true
			created, err := d.st.Upsert(ctx, r)
			if err != nil {
				return err
			}
			if created {
				added++
			}
		}
	}
	known, err := d.st.IDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range known {
		if seen[id] {
			continue
		}
		r, err := d.st.Get(ctx, id)
		if err != nil || !r.Enabled {
			continue
		}
		if err := d.st.Disable(ctx, id); err != nil {
			return err
		}
		disabled++
	}
	if added > 0 {
		d.metrics.Add("deps_inventory_repositories_total", map[string]string{"action": "added"}, float64(added))
	}
	if disabled > 0 {
		d.metrics.Add("deps_inventory_repositories_total", map[string]string{"action": "disabled"}, float64(disabled))
	}
	if n, err := d.st.Count(ctx); err == nil {
		d.repos.Set(n)
	}
	slog.InfoContext(ctx, "inventory refreshed", "repositories", len(seen), "added", added,
		"disabled", disabled, "duration", time.Since(start))
	return nil
}

// listForge resolves one forge to the repositories it manages.
func (d *Daemon) listForge(ctx context.Context, f *Forge, client *http.Client) ([]store.Repo, error) {
	if app, ok := d.apps[f.Name]; ok {
		return d.listApp(ctx, f, app)
	}
	info := platform.Info{Kind: platform.Kind(f.Kind), ServerURL: f.ServerURL, APIURL: f.APIURL, Host: hostOf(f.ServerURL)}
	var out []store.Repo
	for _, owner := range f.OwnerGlobs() {
		names, err := platform.ListRepos(ctx, info, d.tokens[f.Name], client, owner)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if r, ok := d.repoFor(f, name, 0); ok {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// listApp resolves the repositories of every installation of a GitHub App. The installation
// is stored with the repository, so a cycle knows which token to mint.
func (d *Daemon) listApp(ctx context.Context, f *Forge, app *ghapp.App) ([]store.Repo, error) {
	installations, err := app.Installations(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.Repo
	for _, in := range installations {
		names, err := app.Repositories(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if r, ok := d.repoFor(f, name, in.ID); ok {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// repoFor builds the store record of a repository, if a rule of the forge manages it.
func (d *Daemon) repoFor(f *Forge, fullName string, installation int64) (store.Repo, bool) {
	rule := f.Match(fullName)
	if rule == nil || !rule.Managed() {
		return store.Repo{}, false
	}
	host := hostOf(f.ServerURL)
	id := store.RepoID(host, fullName)
	interval := rule.Interval.D()
	if interval <= 0 {
		interval = d.cfg.Interval.D()
	}
	return store.Repo{
		ID: id, Host: host, FullName: fullName, Forge: f.Name, Kind: f.Kind,
		APIURL: f.APIURL, ServerURL: f.ServerURL,
		CloneURL:     f.ServerURL + "/" + fullName + ".git",
		Credential:   f.Name,
		Installation: installation,
		Base:         rule.Base,
		Mode:         rule.Mode,
		Enabled:      true,
		Interval:     interval,
		// A new repository is due at a deterministic offset inside one interval. This smear
		// is what keeps a fleet on one cadence from coming due together.
		NextDue: store.Smear(id, time.Now(), interval),
	}, true
}

// hostOf is the host of a server URL.
func hostOf(serverURL string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(serverURL, "https://"), "http://")
	host, _, _ := strings.Cut(s, "/")
	return host
}
