// Package store is the control plane of `deps serve`: the repository inventory, the due
// queue, the leases, the run history and the shared caches, all in Valkey.
//
// Valkey is the only external dependency the daemon has, and it holds no state that cannot
// be rebuilt: the forge is the source of truth (the update branches, the fingerprints in the
// PR bodies, the dashboard issue), so losing Valkey loses scheduling bookkeeping and run
// history, which the next inventory refresh recreates.
package store

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"hash/fnv"
	"iter"
	"strconv"
	"strings"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Buckets is how many partitions the due queue is split into. Workers own buckets by
// rendezvous hashing over the live worker set, which keeps one repository on one worker
// while the set is stable and needs no coordinator when it changes.
const Buckets = 1024

// Key prefixes. They all start with deps: so a Valkey shared with something else stays
// separable, and the HTTP cache has its own prefix so it can be dropped in one sweep.
const (
	keyRepo    = "deps:repo:"
	keyRepos   = "deps:repos"
	keyDue     = "deps:due:"
	keyDueAll  = "deps:due:all"
	keyLease   = "deps:lease:"
	keyWorkers = "deps:workers"
	keyRuns    = "deps:runs"
	keyRunsOf  = "deps:runs:"
	keyReport  = "deps:report:"
	keyBudget  = "deps:budget:"
	keyLock    = "deps:lock:"
	// KeyCache is the prefix of the shared HTTP metadata cache.
	KeyCache = "deps:c:"
)

// Mode is what a cycle of a repository does.
type Mode string

const (
	// ModeRun opens and updates pull requests, as `deps run` does.
	ModeRun Mode = "run"
	// ModePlan only resolves and reports; it makes no forge write at all.
	ModePlan Mode = "plan"
)

// Priority orders a demand against the scheduled cadence; it is folded into the due score,
// so a webhook or a manual trigger overtakes the backlog without a second queue.
const (
	PriorityScheduled = 0
	PriorityWebhook   = 1
	PriorityManual    = 2
)

// priorityLead is how much earlier one priority step makes a repository due.
const priorityLead = int64(time.Hour / time.Millisecond)

// Repo is one managed repository.
type Repo struct {
	ID            string // "<host>/<fullName>", lower-cased
	Host          string
	FullName      string
	Forge         string // the forge's name in the serve config
	Kind          string // platform.Kind
	APIURL        string
	ServerURL     string
	CloneURL      string
	Credential    string // which credential of the forge authenticates it
	Installation  int64  // GitHub App installation; 0 for a static token
	Base          string // the base branch; "" = the repository's default branch
	DefaultBranch string // resolved once by the first cycle, reused afterwards
	Mode          Mode
	Enabled       bool
	Bucket        uint16
	Interval      time.Duration
	NextDue       time.Time
	Priority      int
	FailCount     int
	LastRunAt     time.Time
	LastStatus    string
}

// RepoID is the identity of a repository in the store.
func RepoID(host, fullName string) string {
	return strings.ToLower(host + "/" + fullName)
}

// BucketOf is the due partition of an id.
func BucketOf(id string) uint16 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return uint16(h.Sum32() % Buckets)
}

// Smear spreads a repository's first due time deterministically over one interval. This is
// what turns a fleet of repositories on the same cadence from a thundering herd into a flat
// stream: 100k repositories on a daily cadence are 1.16 cycles per second smeared, and 27.8
// per second if they all come due together.
func Smear(id string, now time.Time, interval time.Duration) time.Time {
	if interval <= 0 {
		return now
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	return now.Add(time.Duration(h.Sum64() % uint64(interval)))
}

// Run is one recorded cycle.
type Run struct {
	ID        string
	RepoID    string
	Worker    string
	Status    string // "ok", "failed", "rate-limited", "running"
	StartedAt time.Time
	Duration  time.Duration
	Created   int
	Updated   int
	Closed    int
	UpToDate  int
	Errors    int
	Message   string
}

// Store is the Valkey control plane.
type Store struct {
	c valkey.Client

	claim    *valkey.Lua
	claimAny *valkey.Lua
	complete *valkey.Lua
	enqueue  *valkey.Lua
	unlock   *valkey.Lua
}

// Open connects to url (redis://host:port/db) and loads the scripts.
func Open(ctx context.Context, url string, tlsConf *tls.Config) (*Store, error) {
	opt, err := valkey.ParseURL(url)
	if err != nil {
		return nil, err
	}
	if tlsConf != nil {
		opt.TLSConfig = tlsConf
	}
	c, err := valkey.NewClient(opt)
	if err != nil {
		return nil, err
	}
	s := &Store{
		c:        c,
		claim:    valkey.NewLuaScript(claimScript),
		claimAny: valkey.NewLuaScript(claimAnyScript),
		complete: valkey.NewLuaScript(completeScript),
		enqueue:  valkey.NewLuaScript(enqueueScript),
		unlock:   valkey.NewLuaScript(unlockScript),
	}
	if err := s.Ping(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the connections.
func (s *Store) Close() { s.c.Close() }

// Ping reports whether Valkey answers; it backs the readiness probe.
func (s *Store) Ping(ctx context.Context) error {
	return s.c.Do(ctx, s.c.B().Ping().Build()).Error()
}

// Client exposes the connection for the caches built on the same Valkey (the HTTP metadata
// cache and the GitHub App token cache), so the daemon opens one connection pool.
func (s *Store) Client() valkey.Client { return s.c }

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

// fields renders a repository as its hash.
func (r Repo) fields() []string {
	return []string{
		"host", r.Host,
		"fullName", r.FullName,
		"forge", r.Forge,
		"kind", r.Kind,
		"apiUrl", r.APIURL,
		"serverUrl", r.ServerURL,
		"cloneUrl", r.CloneURL,
		"credential", r.Credential,
		"installation", strconv.FormatInt(r.Installation, 10),
		"base", r.Base,
		"defaultBranch", r.DefaultBranch,
		"mode", string(r.Mode),
		"enabled", strconv.FormatBool(r.Enabled),
		"bucket", strconv.Itoa(int(r.Bucket)),
		"intervalMs", strconv.FormatInt(r.Interval.Milliseconds(), 10),
		"nextDueMs", strconv.FormatInt(ms(r.NextDue), 10),
		"priority", strconv.Itoa(r.Priority),
		"failCount", strconv.Itoa(r.FailCount),
		"lastRunAtMs", strconv.FormatInt(ms(r.LastRunAt), 10),
		"lastStatus", r.LastStatus,
	}
}

// repoFromFields parses a repository hash.
func repoFromFields(id string, f map[string]string) Repo {
	atoi := func(k string) int { n, _ := strconv.Atoi(f[k]); return n }
	atoi64 := func(k string) int64 { n, _ := strconv.ParseInt(f[k], 10, 64); return n }
	return Repo{
		ID:            id,
		Host:          f["host"],
		FullName:      f["fullName"],
		Forge:         f["forge"],
		Kind:          f["kind"],
		APIURL:        f["apiUrl"],
		ServerURL:     f["serverUrl"],
		CloneURL:      f["cloneUrl"],
		Credential:    f["credential"],
		Installation:  atoi64("installation"),
		Base:          f["base"],
		DefaultBranch: f["defaultBranch"],
		Mode:          Mode(f["mode"]),
		Enabled:       f["enabled"] == "true",
		Bucket:        uint16(atoi("bucket")),
		Interval:      time.Duration(atoi64("intervalMs")) * time.Millisecond,
		NextDue:       fromMS(atoi64("nextDueMs")),
		Priority:      atoi("priority"),
		FailCount:     atoi("failCount"),
		LastRunAt:     fromMS(atoi64("lastRunAtMs")),
		LastStatus:    f["lastStatus"],
	}
}

// Upsert adds a repository or refreshes the fields the inventory owns, keeping the
// scheduling state (nextDue, failCount, lastStatus, defaultBranch) of one that is known
// already. It reports whether the repository is new.
func (s *Store) Upsert(ctx context.Context, r Repo) (bool, error) {
	r.Bucket = BucketOf(r.ID)
	old, err := s.Get(ctx, r.ID)
	known := err == nil && old.Host != ""
	if known {
		r.NextDue, r.FailCount, r.LastStatus, r.LastRunAt = old.NextDue, old.FailCount, old.LastStatus, old.LastRunAt
		r.DefaultBranch = old.DefaultBranch
		if r.Interval == 0 {
			r.Interval = old.Interval
		}
	}
	cmds := []valkey.Completed{
		s.c.B().Hset().Key(keyRepo + r.ID).FieldValue().FieldValueIter(pairs(r.fields())).Build(),
		s.c.B().Sadd().Key(keyRepos).Member(r.ID).Build(),
	}
	// The queue entry is only written when the repository enters it: for one that is already
	// queued the entry is the scheduler's, and rewriting it here would undo a claim's
	// visibility bump or a webhook's demand.
	if r.Enabled && (!known || !old.Enabled) {
		score := float64(dueScore(r.NextDue, r.Priority))
		cmds = append(cmds,
			s.c.B().Zadd().Key(keyDue+strconv.Itoa(int(r.Bucket))).ScoreMember().ScoreMember(score, r.ID).Build(),
			s.c.B().Zadd().Key(keyDueAll).ScoreMember().ScoreMember(score, r.ID).Build())
	}
	for _, res := range s.c.DoMulti(ctx, cmds...) {
		if err := res.Error(); err != nil {
			return false, err
		}
	}
	return !known, nil
}

// pairs turns a flat field list into the key/value sequence the command builder wants.
func pairs(flat []string) iter.Seq2[string, string] {
	return func(yield func(string, string) bool) {
		for i := 0; i+1 < len(flat); i += 2 {
			if !yield(flat[i], flat[i+1]) {
				return
			}
		}
	}
}

// dueScore folds the priority into the due time: one step is an hour's lead, so a webhook
// overtakes the scheduled backlog without a queue of its own.
func dueScore(due time.Time, priority int) int64 {
	return ms(due) - int64(priority)*priorityLead
}

// SetDefaultBranch records the branch a cycle resolved, so later cycles skip the lookup.
func (s *Store) SetDefaultBranch(ctx context.Context, id, branch string) error {
	return s.c.Do(ctx, s.c.B().Hset().Key(keyRepo+id).FieldValue().FieldValue("defaultBranch", branch).Build()).Error()
}

// Disable takes a repository out of the queues without forgetting it: its hash, and with it
// its run history, stays readable.
func (s *Store) Disable(ctx context.Context, id string) error {
	bucket := strconv.Itoa(int(BucketOf(id)))
	for _, res := range s.c.DoMulti(ctx,
		s.c.B().Hset().Key(keyRepo+id).FieldValue().FieldValue("enabled", "false").Build(),
		s.c.B().Zrem().Key(keyDue+bucket).Member(id).Build(),
		s.c.B().Zrem().Key(keyDueAll).Member(id).Build(),
	) {
		if err := res.Error(); err != nil {
			return err
		}
	}
	return nil
}

// ErrNoRepo means the store has no such repository.
var ErrNoRepo = errors.New("repository not found")

// Get reads one repository.
func (s *Store) Get(ctx context.Context, id string) (Repo, error) {
	f, err := s.c.Do(ctx, s.c.B().Hgetall().Key(keyRepo+id).Build()).AsStrMap()
	if err != nil {
		return Repo{}, err
	}
	if len(f) == 0 {
		return Repo{}, fmt.Errorf("%s: %w", id, ErrNoRepo)
	}
	return repoFromFields(id, f), nil
}

// Scan pages through the known repositories; cursor 0 starts, and a returned 0 ends.
func (s *Store) Scan(ctx context.Context, cursor uint64, n int64) ([]Repo, uint64, error) {
	e, err := s.c.Do(ctx, s.c.B().Sscan().Key(keyRepos).Cursor(cursor).Count(n).Build()).AsScanEntry()
	if err != nil {
		return nil, 0, err
	}
	out := make([]Repo, 0, len(e.Elements))
	for _, id := range e.Elements {
		r, err := s.Get(ctx, id)
		if err != nil {
			continue // disappeared between the scan and the read
		}
		out = append(out, r)
	}
	return out, e.Cursor, nil
}

// IDs returns every known repository id.
func (s *Store) IDs(ctx context.Context) ([]string, error) {
	return s.c.Do(ctx, s.c.B().Smembers().Key(keyRepos).Build()).AsStrSlice()
}

// Count is how many repositories are known.
func (s *Store) Count(ctx context.Context) (int64, error) {
	return s.c.Do(ctx, s.c.B().Scard().Key(keyRepos).Build()).AsInt64()
}

// Due is how many repositories are due now, for the queue metric. scope "" counts the whole
// fleet; a bucket list counts only those partitions.
func (s *Store) Due(ctx context.Context, now time.Time, buckets []uint16) (int64, error) {
	upto := strconv.FormatInt(ms(now), 10)
	if buckets == nil {
		return s.c.Do(ctx, s.c.B().Zcount().Key(keyDueAll).Min("-inf").Max(upto).Build()).AsInt64()
	}
	var total int64
	for _, b := range buckets {
		n, err := s.c.Do(ctx, s.c.B().Zcount().Key(keyDue+strconv.Itoa(int(b))).Min("-inf").Max(upto).Build()).AsInt64()
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// MarkRunning records that a cycle of the repository started, so the API and the inventory
// show what is in flight. A replica that dies mid-cycle leaves the mark until its next
// cycle overwrites it, which is what tells an operator the cycle was interrupted.
func (s *Store) MarkRunning(ctx context.Context, id string, at time.Time) error {
	return s.c.Do(ctx, s.c.B().Hset().Key(keyRepo+id).FieldValue().
		FieldValue("lastStatus", "running").
		FieldValue("lastRunAtMs", strconv.FormatInt(ms(at), 10)).Build()).Error()
}
