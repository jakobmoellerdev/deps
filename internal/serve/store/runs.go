package store

import (
	"context"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"
)

// maxGlobalRuns and maxRepoRuns bound the run history: the global stream is what the API
// lists, the per-repository one is the last few cycles of one repository.
const (
	maxGlobalRuns = 100000
	maxRepoRuns   = 20
)

// AddRun records a cycle in the global stream and in the repository's own.
func (s *Store) AddRun(ctx context.Context, r Run) error {
	f := []string{
		"id", r.ID,
		"repo", r.RepoID,
		"worker", r.Worker,
		"status", r.Status,
		"startedAtMs", strconv.FormatInt(ms(r.StartedAt), 10),
		"durationMs", strconv.FormatInt(r.Duration.Milliseconds(), 10),
		"created", strconv.Itoa(r.Created),
		"updated", strconv.Itoa(r.Updated),
		"closed", strconv.Itoa(r.Closed),
		"upToDate", strconv.Itoa(r.UpToDate),
		"errors", strconv.Itoa(r.Errors),
		"message", r.Message,
	}
	for _, res := range s.c.DoMulti(ctx,
		s.c.B().Xadd().Key(keyRuns).Maxlen().Almost().Threshold(strconv.Itoa(maxGlobalRuns)).Id("*").FieldValue().FieldValueIter(pairs(f)).Build(),
		s.c.B().Xadd().Key(keyRunsOf+r.RepoID).Maxlen().Almost().Threshold(strconv.Itoa(maxRepoRuns)).Id("*").FieldValue().FieldValueIter(pairs(f)).Build(),
	) {
		if err := res.Error(); err != nil {
			return err
		}
	}
	return nil
}

// Runs returns the last n cycles, newest first: of one repository, or of the whole fleet
// when repoID is empty.
func (s *Store) Runs(ctx context.Context, repoID string, n int64) ([]Run, error) {
	key := keyRuns
	if repoID != "" {
		key = keyRunsOf + repoID
	}
	entries, err := s.c.Do(ctx, s.c.B().Xrevrange().Key(key).End("+").Start("-").Count(n).Build()).AsXRange()
	if err != nil {
		if valkey.IsValkeyNil(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Run, 0, len(entries))
	for _, e := range entries {
		out = append(out, runFromFields(e.FieldValues))
	}
	return out, nil
}

func runFromFields(f map[string]string) Run {
	atoi := func(k string) int { n, _ := strconv.Atoi(f[k]); return n }
	atoi64 := func(k string) int64 { n, _ := strconv.ParseInt(f[k], 10, 64); return n }
	return Run{
		ID:        f["id"],
		RepoID:    f["repo"],
		Worker:    f["worker"],
		Status:    f["status"],
		StartedAt: fromMS(atoi64("startedAtMs")),
		Duration:  time.Duration(atoi64("durationMs")) * time.Millisecond,
		Created:   atoi("created"),
		Updated:   atoi("updated"),
		Closed:    atoi("closed"),
		UpToDate:  atoi("upToDate"),
		Errors:    atoi("errors"),
		Message:   f["message"],
	}
}

// PutReport stores the machine-readable report of a run for ttl.
func (s *Store) PutReport(ctx context.Context, runID string, body []byte, ttl time.Duration) error {
	return s.c.Do(ctx, s.c.B().Set().Key(keyReport+runID).Value(valkey.BinaryString(body)).
		Px(ttl).Build()).Error()
}

// Report returns the stored report, nil once its TTL passed.
func (s *Store) Report(ctx context.Context, runID string) ([]byte, error) {
	b, err := s.c.Do(ctx, s.c.B().Get().Key(keyReport+runID).Build()).AsBytes()
	if valkey.IsValkeyNil(err) {
		return nil, nil
	}
	return b, err
}

// Budget counts add requests against a credential's hourly limit and reports whether they
// fit. The budget of an API token is per account and does not grow with the number of
// replicas, so it is the real ceiling of the fleet; a cycle that would exceed it is
// rescheduled to the reset instead of spending the rest on errors.
func (s *Store) Budget(ctx context.Context, credential string, add, limit int) (bool, time.Time, error) {
	now := time.Now()
	hour := now.Truncate(time.Hour)
	key := keyBudget + credential + ":" + strconv.FormatInt(hour.Unix(), 10)
	reset := hour.Add(time.Hour)
	if limit <= 0 {
		return true, reset, nil
	}
	used, err := s.c.Do(ctx, s.c.B().Incrby().Key(key).Increment(int64(add)).Build()).AsInt64()
	if err != nil {
		return true, reset, err // a counter failure must not stop the fleet
	}
	if used == int64(add) {
		// First write of this hour: give the counter an expiry so it cannot pile up.
		_ = s.c.Do(ctx, s.c.B().Pexpire().Key(key).Milliseconds((2 * time.Hour).Milliseconds()).Build()).Error()
	}
	return used <= int64(limit), reset, nil
}

// TokenCache adapts the store to ghapp.TokenCache, so installation tokens are minted once
// for the fleet instead of once per replica.
type TokenCache struct{ S *Store }

func (c TokenCache) Get(ctx context.Context, key string) (string, time.Time, bool) {
	v, err := c.S.c.Do(ctx, c.S.c.B().Get().Key(key).Build()).ToString()
	if err != nil || v == "" {
		return "", time.Time{}, false
	}
	expMS, token, ok := cut(v)
	if !ok {
		return "", time.Time{}, false
	}
	return token, fromMS(expMS), true
}

func (c TokenCache) Put(ctx context.Context, key, token string, exp time.Time) error {
	ttl := time.Until(exp) - 5*time.Minute
	if ttl <= 0 {
		return nil
	}
	v := strconv.FormatInt(ms(exp), 10) + " " + token
	return c.S.c.Do(ctx, c.S.c.B().Set().Key(key).Value(v).Px(ttl).Build()).Error()
}

// cut splits "<expiryMs> <token>".
func cut(v string) (int64, string, bool) {
	for i := range len(v) {
		if v[i] == ' ' {
			n, err := strconv.ParseInt(v[:i], 10, 64)
			return n, v[i+1:], err == nil
		}
	}
	return 0, "", false
}
