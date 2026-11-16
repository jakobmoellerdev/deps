package store

import (
	"context"
	"hash/fnv"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"
)

// The due ZSET score doubles as a visibility timeout: claiming a repository bumps its score
// to now+visibility and takes a lease, and the entry becomes claimable again once that
// passes. A crashed worker therefore needs no janitor; its repositories simply come due
// again. Every enqueue uses ZADD ... LT, so an earlier demand (a webhook, a manual trigger)
// always wins and nothing is ever pushed further out by a concurrent writer.

// claimScript takes up to ARGV[3] due repositories of the worker's own buckets.
// KEYS: the bucket ZSETs. ARGV: nowMs, visibilityMs, limit, worker.
const claimScript = `
local now = tonumber(ARGV[1])
local vis = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local worker = ARGV[4]
local out = {}
for i = 1, #KEYS do
  if #out >= limit then break end
  local ids = redis.call('ZRANGEBYSCORE', KEYS[i], '-inf', now, 'LIMIT', 0, limit * 2)
  for j = 1, #ids do
    if #out >= limit then break end
    local id = ids[j]
    if redis.call('SET', 'deps:lease:' .. id, worker, 'NX', 'PX', vis) then
      redis.call('ZADD', KEYS[i], now + vis, id)
      redis.call('ZADD', 'deps:due:all', now + vis, id)
      out[#out + 1] = id
    end
  end
end
return out
`

// claimAnyScript steals repositories nobody picked up for graceMs: the owning worker is gone
// or is behind. ARGV: nowMs, graceMs, visibilityMs, limit, worker.
const claimAnyScript = `
local now = tonumber(ARGV[1])
local grace = tonumber(ARGV[2])
local vis = tonumber(ARGV[3])
local limit = tonumber(ARGV[4])
local worker = ARGV[5]
local out = {}
local ids = redis.call('ZRANGEBYSCORE', 'deps:due:all', '-inf', now - grace, 'LIMIT', 0, limit * 4)
for j = 1, #ids do
  if #out >= limit then break end
  local id = ids[j]
  if redis.call('SET', 'deps:lease:' .. id, worker, 'NX', 'PX', vis) then
    local bucket = redis.call('HGET', 'deps:repo:' .. id, 'bucket')
    if bucket then redis.call('ZADD', 'deps:due:' .. bucket, now + vis, id) end
    redis.call('ZADD', 'deps:due:all', now + vis, id)
    out[#out + 1] = id
  end
end
return out
`

// completeScript records a finished cycle, but only while the worker still holds the lease:
// a worker whose lease was stolen must not move the due time of a cycle someone else is
// running.
//
// The next due time is written absolutely, not with LT: claiming pushed the score out to the
// visibility timeout, so an LT write could never move it back and the repository would come
// due again a visibility later, forever. A demand that arrived *during* the cycle (a webhook,
// a manual trigger) is kept in the rerun field and wins over the cadence, so it is not lost.
// ARGV: id, worker, nextScore, status, lastRunMs, failCount, bucket.
const completeScript = `
local id = ARGV[1]
local worker = ARGV[2]
if redis.call('GET', 'deps:lease:' .. id) ~= worker then return 0 end
redis.call('DEL', 'deps:lease:' .. id)
local score = tonumber(ARGV[3])
local rerun = redis.call('HGET', 'deps:repo:' .. id, 'rerun')
if rerun and rerun ~= '' and tonumber(rerun) and tonumber(rerun) < score then
  score = tonumber(rerun)
end
redis.call('ZADD', 'deps:due:' .. ARGV[7], score, id)
redis.call('ZADD', 'deps:due:all', score, id)
redis.call('HSET', 'deps:repo:' .. id,
  'nextDueMs', ARGV[3], 'lastStatus', ARGV[4], 'lastRunAtMs', ARGV[5], 'failCount', ARGV[6],
  'priority', '0', 'rerun', '')
return 1
`

// enqueueScript brings a repository forward. While a cycle holds the lease the demand is
// also recorded in the rerun field, so completing that cycle schedules another one instead
// of overwriting the demand with the cadence. ARGV: id, score, priority, bucket.
const enqueueScript = `
local id = ARGV[1]
if redis.call('EXISTS', 'deps:repo:' .. id) == 0 then return 0 end
if redis.call('HGET', 'deps:repo:' .. id, 'enabled') ~= 'true' then return 0 end
local score = tonumber(ARGV[2])
redis.call('ZADD', 'deps:due:' .. ARGV[4], 'LT', score, id)
redis.call('ZADD', 'deps:due:all', 'LT', score, id)
redis.call('HSET', 'deps:repo:' .. id, 'priority', ARGV[3])
if redis.call('EXISTS', 'deps:lease:' .. id) == 1 then
  local prev = redis.call('HGET', 'deps:repo:' .. id, 'rerun')
  if not prev or prev == '' or tonumber(prev) == nil or tonumber(prev) > score then
    redis.call('HSET', 'deps:repo:' .. id, 'rerun', ARGV[2])
  end
end
return 1
`

// unlockScript releases a leader lock only if it is still ours.
const unlockScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end
return 0
`

// Claim takes up to n due repositories of the worker's own buckets and leases them for vis.
func (s *Store) Claim(ctx context.Context, worker string, buckets []uint16, n int, vis time.Duration) ([]Repo, error) {
	if n <= 0 || len(buckets) == 0 {
		return nil, nil
	}
	keys := make([]string, len(buckets))
	for i, b := range buckets {
		keys[i] = keyDue + strconv.Itoa(int(b))
	}
	now := time.Now()
	ids, err := s.claim.Exec(ctx, s.c, keys, []string{
		strconv.FormatInt(ms(now), 10), strconv.FormatInt(vis.Milliseconds(), 10), strconv.Itoa(n), worker,
	}).AsStrSlice()
	if err != nil {
		return nil, err
	}
	return s.repos(ctx, ids), nil
}

// ClaimAny steals repositories that stayed due for grace, whatever bucket they are in: the
// worker that owned them is gone, or cannot keep up.
func (s *Store) ClaimAny(ctx context.Context, worker string, n int, grace, vis time.Duration) ([]Repo, error) {
	if n <= 0 {
		return nil, nil
	}
	now := time.Now()
	ids, err := s.claimAny.Exec(ctx, s.c, []string{keyDueAll}, []string{
		strconv.FormatInt(ms(now), 10), strconv.FormatInt(grace.Milliseconds(), 10),
		strconv.FormatInt(vis.Milliseconds(), 10), strconv.Itoa(n), worker,
	}).AsStrSlice()
	if err != nil {
		return nil, err
	}
	return s.repos(ctx, ids), nil
}

// repos reads the claimed repositories, skipping the ones that disappeared.
func (s *Store) repos(ctx context.Context, ids []string) []Repo {
	out := make([]Repo, 0, len(ids))
	for _, id := range ids {
		r, err := s.Get(ctx, id)
		if err != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

// Extend renews the lease and pushes the visibility timeout out, for a cycle still running.
func (s *Store) Extend(ctx context.Context, worker, id string, vis time.Duration) error {
	now := time.Now()
	// The lease is only renewed while it is still ours; the due score follows it.
	ok, err := s.c.Do(ctx, s.c.B().Set().Key(keyLease+id).Value(worker).Xx().Px(vis).Build()).ToString()
	if err != nil || ok == "" {
		return err
	}
	score := float64(ms(now.Add(vis)))
	for _, res := range s.c.DoMulti(ctx,
		s.c.B().Zadd().Key(keyDue+strconv.Itoa(int(BucketOf(id)))).Gt().ScoreMember().ScoreMember(score, id).Build(),
		s.c.B().Zadd().Key(keyDueAll).Gt().ScoreMember().ScoreMember(score, id).Build(),
	) {
		if err := res.Error(); err != nil {
			return err
		}
	}
	return nil
}

// Complete records the result of a cycle and schedules the next one. It reports false when
// the lease was lost meanwhile, in which case nothing was written: another worker owns the
// repository now.
func (s *Store) Complete(ctx context.Context, worker string, r Repo, status string, next time.Time) (bool, error) {
	n, err := s.complete.Exec(ctx, s.c, []string{keyRepo + r.ID}, []string{
		r.ID, worker, strconv.FormatInt(ms(next), 10), status,
		strconv.FormatInt(ms(time.Now()), 10), strconv.Itoa(r.FailCount),
		strconv.Itoa(int(BucketOf(r.ID))),
	}).AsInt64()
	return n == 1, err
}

// Enqueue brings a repository forward to at with priority. An earlier entry stays earlier
// (ZADD ... LT), so a burst of webhooks collapses into one cycle.
func (s *Store) Enqueue(ctx context.Context, id string, at time.Time, priority int) (bool, error) {
	n, err := s.enqueue.Exec(ctx, s.c, []string{keyRepo + id}, []string{
		id, strconv.FormatInt(dueScore(at, priority), 10), strconv.Itoa(priority),
		strconv.Itoa(int(BucketOf(id))),
	}).AsInt64()
	return n == 1, err
}

// NextBackoff is when a repository is due again: its cadence after a success, and an
// exponential backoff capped at the cadence after a failure. The jitter is derived from the
// id, so the smear stays deterministic and a flapping repository does not drift into a herd.
func NextBackoff(id string, now time.Time, interval time.Duration, failCount int) time.Time {
	d := interval
	if failCount > 0 {
		d = 5 * time.Minute
		for range min(failCount-1, 10) {
			d *= 2
			if d >= interval {
				break
			}
		}
		d = min(d, interval)
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	_, _ = h.Write([]byte(strconv.FormatInt(now.Unix(), 10)))
	// ±10 %, deterministic for one id and second.
	jitter := time.Duration(int64(h.Sum64()%uint64(d/5+1))) - d/10
	return now.Add(d + jitter)
}

// Heartbeat marks the worker alive; Workers lists the ones that still are.
func (s *Store) Heartbeat(ctx context.Context, worker string) error {
	return s.c.Do(ctx, s.c.B().Zadd().Key(keyWorkers).ScoreMember().
		ScoreMember(float64(ms(time.Now())), worker).Build()).Error()
}

// Workers are the workers that heartbeated within maxAge; the older entries are removed.
func (s *Store) Workers(ctx context.Context, maxAge time.Duration) ([]string, error) {
	cutoff := ms(time.Now().Add(-maxAge))
	if err := s.c.Do(ctx, s.c.B().Zremrangebyscore().Key(keyWorkers).
		Min("-inf").Max(strconv.FormatInt(cutoff, 10)).Build()).Error(); err != nil {
		return nil, err
	}
	return s.c.Do(ctx, s.c.B().Zrange().Key(keyWorkers).Min("-inf").Max("+inf").Byscore().Build()).AsStrSlice()
}

// Leave removes a worker from the live set, so a replica shutting down hands its buckets
// over at once instead of after the heartbeat timeout.
func (s *Store) Leave(ctx context.Context, worker string) error {
	return s.c.Do(ctx, s.c.B().Zrem().Key(keyWorkers).Member(worker).Build()).Error()
}

// OwnedBuckets is the partitions worker owns among workers, by rendezvous hashing: bucket b
// belongs to the worker maximizing fnv64a(worker|b). Adding or removing a worker moves only
// its own share, and a disagreement during a rollout is harmless, since the lease still
// admits one worker per repository.
func OwnedBuckets(worker string, workers []string) []uint16 {
	if len(workers) == 0 {
		return nil
	}
	var out []uint16
	for b := range uint16(Buckets) {
		best, bestScore := "", uint64(0)
		for _, w := range workers {
			if s := rendezvous(w, b); s > bestScore || (s == bestScore && w > best) {
				best, bestScore = w, s
			}
		}
		if best == worker {
			out = append(out, b)
		}
	}
	return out
}

// rendezvous scores a (worker, bucket) pair. FNV alone leaves the high bits of a short
// suffix dominated by the worker name, which would hand every bucket to one worker, so the
// bucket is folded in and run through a splitmix64 finalizer: changing it changes the whole
// score.
func rendezvous(worker string, bucket uint16) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(worker))
	x := h.Sum64() ^ (uint64(bucket)+1)*0x9E3779B97F4A7C15
	x ^= x >> 30
	x *= 0xBF58476D1CE4E5B9
	x ^= x >> 27
	x *= 0x94D049BB133111EB
	x ^= x >> 31
	return x
}

// Lock takes the named leader lock for ttl, reporting whether it is ours now. The scheduler
// duties (inventory refresh, retention) run only under it; it is renewed by taking it again
// with the same owner.
func (s *Store) Lock(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	set, err := s.c.Do(ctx, s.c.B().Set().Key(keyLock+name).Value(owner).Nx().Px(ttl).Build()).ToString()
	if err != nil && !valkey.IsValkeyNil(err) {
		return false, err
	}
	if set != "" {
		return true, nil
	}
	// Already held: renew it if it is ours.
	held, err := s.c.Do(ctx, s.c.B().Get().Key(keyLock+name).Build()).ToString()
	if err != nil && !valkey.IsValkeyNil(err) {
		return false, err
	}
	if held != owner {
		return false, nil
	}
	return true, s.c.Do(ctx, s.c.B().Pexpire().Key(keyLock+name).Milliseconds(ttl.Milliseconds()).Build()).Error()
}

// Unlock releases the lock if it is still ours.
func (s *Store) Unlock(ctx context.Context, name, owner string) error {
	return s.unlock.Exec(ctx, s.c, []string{keyLock + name}, []string{owner}).Error()
}
