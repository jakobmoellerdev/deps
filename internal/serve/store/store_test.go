package store

import (
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// testStore opens a store on a throwaway database: a valkey-server the test starts, or the
// one $DEPS_TEST_VALKEY points at (a container, as CI runs it). Without either the test is
// skipped, so the suite stays runnable on a plain checkout.
func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("DEPS_TEST_VALKEY")
	if url == "" {
		bin, err := exec.LookPath("valkey-server")
		if err != nil {
			if bin, err = exec.LookPath("redis-server"); err != nil {
				t.Skip("no valkey-server and no $DEPS_TEST_VALKEY")
			}
		}
		url = "redis://" + startServer(t, bin)
	}
	s, err := Open(t.Context(), url, nil)
	if err != nil {
		t.Skipf("valkey at %s: %v", url, err)
	}
	t.Cleanup(s.Close)
	// Every test starts from an empty database.
	if err := s.c.Do(t.Context(), s.c.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	return s
}

// startServer runs a valkey-server on a free port for the test.
func startServer(t *testing.T, bin string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "--port", port, "--save", "", "--appendonly", "no", "--dir", t.TempDir())
	cmd.Stdout, cmd.Stderr = os.NewFile(0, os.DevNull), os.NewFile(0, os.DevNull)
	if err := cmd.Start(); err != nil {
		t.Skipf("starting %s: %v", filepath.Base(bin), err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	for range 100 {
		if c, err := net.Dial("tcp", addr); err == nil {
			_ = c.Close()
			return addr
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Skip("valkey-server did not come up")
	return ""
}

func testRepo(id string, due time.Time) Repo {
	return Repo{
		ID: id, Host: "git.example", FullName: id[len("git.example/"):], Forge: "f", Kind: "forgejo",
		CloneURL: "https://git.example/" + id, Mode: ModeRun, Enabled: true,
		Interval: time.Hour, NextDue: due,
	}
}

func TestClaimLeasesEachRepositoryOnce(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	r := testRepo("git.example/o/n", time.Now().Add(-time.Minute))
	if created, err := s.Upsert(ctx, r); err != nil || !created {
		t.Fatalf("Upsert = %v, %v", created, err)
	}
	all := allBuckets()

	first, err := s.Claim(ctx, "w1", all, 10, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].ID != r.ID {
		t.Fatalf("first claim = %v", first)
	}
	// While the lease holds, nobody else gets it.
	second, err := s.Claim(ctx, "w2", all, 10, time.Second)
	if err != nil || len(second) != 0 {
		t.Fatalf("second claim = %v, %v; want none while the lease holds", second, err)
	}
	// Once the visibility timeout passes without an Extend, it is claimable again: a worker
	// that died needs no janitor.
	time.Sleep(1200 * time.Millisecond)
	third, err := s.Claim(ctx, "w2", all, 10, time.Minute)
	if err != nil || len(third) != 1 {
		t.Fatalf("third claim = %v, %v; want the repository back", third, err)
	}
}

func TestCompleteNeedsTheLease(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	r := testRepo("git.example/o/n", time.Now().Add(-time.Minute))
	if _, err := s.Upsert(ctx, r); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.Claim(ctx, "w1", allBuckets(), 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	next := time.Now().Add(time.Hour)
	ok, err := s.Complete(ctx, "w2", claimed[0], "ok", next)
	if err != nil || ok {
		t.Fatalf("Complete from the wrong worker = %v, %v; want false", ok, err)
	}
	got, err := s.Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastStatus != "" {
		t.Errorf("a lost lease wrote the status %q", got.LastStatus)
	}
	ok, err = s.Complete(ctx, "w1", claimed[0], "ok", next)
	if err != nil || !ok {
		t.Fatalf("Complete from the lease holder = %v, %v", ok, err)
	}
	if got, err = s.Get(ctx, r.ID); err != nil || got.LastStatus != "ok" {
		t.Fatalf("after Complete: %+v, %v", got, err)
	}
	if d := got.NextDue.Sub(next).Abs(); d > time.Second {
		t.Errorf("next due = %v, want %v", got.NextDue, next)
	}
	// Completed, so no longer claimable.
	if again, err := s.Claim(ctx, "w1", allBuckets(), 1, time.Minute); err != nil || len(again) != 0 {
		t.Errorf("claim after Complete = %v, %v", again, err)
	}
}

func TestExtendKeepsALongCycleLeased(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	r := testRepo("git.example/o/n", time.Now().Add(-time.Minute))
	if _, err := s.Upsert(ctx, r); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.Claim(ctx, "w1", allBuckets(), 1, time.Second); err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	for range 3 {
		time.Sleep(400 * time.Millisecond)
		if err := s.Extend(ctx, "w1", r.ID, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if stolen, err := s.Claim(ctx, "w2", allBuckets(), 1, time.Minute); err != nil || len(stolen) != 0 {
		t.Errorf("an extended lease was stolen: %v, %v", stolen, err)
	}
}

func TestClaimAnySteals(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	r := testRepo("git.example/o/n", time.Now().Add(-10*time.Minute))
	if _, err := s.Upsert(ctx, r); err != nil {
		t.Fatal(err)
	}
	// Nothing to steal yet: the grace has not passed.
	if got, err := s.ClaimAny(ctx, "w2", 5, time.Hour, time.Minute); err != nil || len(got) != 0 {
		t.Fatalf("ClaimAny within the grace = %v, %v", got, err)
	}
	got, err := s.ClaimAny(ctx, "w2", 5, time.Minute, time.Minute)
	if err != nil || len(got) != 1 {
		t.Fatalf("ClaimAny = %v, %v; want the stale repository", got, err)
	}
}

func TestEnqueueOnlyEverBringsForward(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	far := time.Now().Add(20 * time.Hour)
	r := testRepo("git.example/o/n", far)
	if _, err := s.Upsert(ctx, r); err != nil {
		t.Fatal(err)
	}
	soon := time.Now().Add(30 * time.Second)
	if ok, err := s.Enqueue(ctx, r.ID, soon, PriorityWebhook); err != nil || !ok {
		t.Fatalf("Enqueue = %v, %v", ok, err)
	}
	score := s.score(t, r.ID)
	// A second, later demand must not push it back out.
	if _, err := s.Enqueue(ctx, r.ID, time.Now().Add(10*time.Hour), PriorityWebhook); err != nil {
		t.Fatal(err)
	}
	if got := s.score(t, r.ID); got != score {
		t.Errorf("a later demand moved the due time from %d to %d", score, got)
	}
	// An unknown repository is not enqueued at all.
	if ok, err := s.Enqueue(ctx, "git.example/o/missing", soon, PriorityManual); err != nil || ok {
		t.Errorf("Enqueue of an unknown repository = %v, %v", ok, err)
	}
}

func TestDisableKeepsTheHistory(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	r := testRepo("git.example/o/n", time.Now().Add(-time.Minute))
	if _, err := s.Upsert(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRun(ctx, Run{ID: "run1", RepoID: r.ID, Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Disable(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Claim(ctx, "w1", allBuckets(), 1, time.Minute); err != nil || len(got) != 0 {
		t.Errorf("a disabled repository was claimed: %v, %v", got, err)
	}
	runs, err := s.Runs(ctx, r.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].ID != "run1" {
		t.Errorf("runs after Disable = %v, %v", runs, err)
	}
}

func TestUpsertKeepsSchedulingState(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	due := time.Now().Add(7 * time.Hour).Truncate(time.Millisecond)
	r := testRepo("git.example/o/n", due)
	if _, err := s.Upsert(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefaultBranch(ctx, r.ID, "main"); err != nil {
		t.Fatal(err)
	}
	// The next inventory refresh sees the repository again with a fresh due time.
	again := testRepo(r.ID, time.Now())
	created, err := s.Upsert(ctx, again)
	if err != nil || created {
		t.Fatalf("second Upsert = %v, %v; want it known", created, err)
	}
	got, err := s.Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.NextDue.Equal(due) {
		t.Errorf("next due = %v, want the smeared %v kept", got.NextDue, due)
	}
	if got.DefaultBranch != "main" {
		t.Errorf("default branch = %q, want it kept", got.DefaultBranch)
	}
}

func TestLockIsHeldByOneOwner(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	ok, err := s.Lock(ctx, "scheduler", "a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("first Lock = %v, %v", ok, err)
	}
	if ok, err := s.Lock(ctx, "scheduler", "b", time.Minute); err != nil || ok {
		t.Fatalf("second owner got the lock: %v, %v", ok, err)
	}
	if ok, err := s.Lock(ctx, "scheduler", "a", time.Minute); err != nil || !ok {
		t.Fatalf("renewal by the owner = %v, %v", ok, err)
	}
	if err := s.Unlock(ctx, "scheduler", "b"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Lock(ctx, "scheduler", "b", time.Minute); err != nil || ok {
		t.Error("Unlock by a stranger released the lock")
	}
	if err := s.Unlock(ctx, "scheduler", "a"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Lock(ctx, "scheduler", "b", time.Minute); err != nil || !ok {
		t.Fatalf("after Unlock = %v, %v", ok, err)
	}
}

func TestBudgetStopsAtTheLimit(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	for i := range 5 {
		ok, reset, err := s.Budget(ctx, "cred", 1, 5)
		if err != nil || !ok {
			t.Fatalf("request %d = %v, %v", i, ok, err)
		}
		if time.Until(reset) > time.Hour {
			t.Errorf("reset = %v", reset)
		}
	}
	if ok, _, err := s.Budget(ctx, "cred", 1, 5); err != nil || ok {
		t.Errorf("over the limit = %v, %v; want it refused", ok, err)
	}
	// Another credential has its own budget.
	if ok, _, err := s.Budget(ctx, "other", 1, 5); err != nil || !ok {
		t.Errorf("other credential = %v, %v", ok, err)
	}
}

func TestReportRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	body := []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, 0xff} // binary, as a compressed report is
	if err := s.PutReport(ctx, "run1", body, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := s.Report(ctx, "run1")
	if err != nil || string(got) != string(body) {
		t.Errorf("Report = %v, %v", got, err)
	}
	if got, err := s.Report(ctx, "missing"); err != nil || got != nil {
		t.Errorf("Report of an expired run = %v, %v", got, err)
	}
}

func TestWorkersAgeOut(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	if err := s.Heartbeat(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	live, err := s.Workers(ctx, time.Minute)
	if err != nil || len(live) != 1 || live[0] != "w1" {
		t.Fatalf("Workers = %v, %v", live, err)
	}
	if live, err := s.Workers(ctx, time.Nanosecond); err != nil || len(live) != 0 {
		t.Errorf("stale workers = %v, %v", live, err)
	}
	if err := s.Heartbeat(ctx, "w2"); err != nil {
		t.Fatal(err)
	}
	if err := s.Leave(ctx, "w2"); err != nil {
		t.Fatal(err)
	}
	if live, err := s.Workers(ctx, time.Minute); err != nil || len(live) != 0 {
		t.Errorf("after Leave = %v, %v", live, err)
	}
}

// score reads the due score of a repository.
func (s *Store) score(t *testing.T, id string) int64 {
	t.Helper()
	v, err := s.c.Do(t.Context(), s.c.B().Zscore().Key(keyDueAll).Member(id).Build()).AsFloat64()
	if err != nil {
		t.Fatal(err)
	}
	return int64(v)
}

func allBuckets() []uint16 {
	out := make([]uint16, Buckets)
	for i := range out {
		out[i] = uint16(i)
	}
	return out
}

// TestSmear checks the premise of the whole sizing: 100k repositories on one daily cadence
// must not come due together.
func TestSmear(t *testing.T) {
	const (
		repos    = 100_000
		interval = 24 * time.Hour
	)
	now := time.Now()
	minutes := map[int64]int{}
	for i := range repos {
		id := "git.example/org/repo-" + strconv.Itoa(i)
		due := Smear(id, now, interval)
		if d := due.Sub(now); d < 0 || d >= interval {
			t.Fatalf("%s is due in %v, outside the interval", id, d)
		}
		minutes[due.Unix()/60]++
	}
	mean := float64(repos) / interval.Minutes()
	worst := 0
	for _, n := range minutes {
		worst = max(worst, n)
	}
	if float64(worst) > 3*mean {
		t.Errorf("busiest minute holds %d repositories, mean %.1f: the herd is not broken up", worst, mean)
	}
	t.Logf("mean %.1f repositories per minute, busiest %d", mean, worst)
}

func TestOwnedBucketsCoverAndBalance(t *testing.T) {
	workers := []string{"a-1", "b-2", "c-3", "d-4"}
	seen := map[uint16]string{}
	for _, w := range workers {
		for _, b := range OwnedBuckets(w, workers) {
			if other, dup := seen[b]; dup {
				t.Fatalf("bucket %d owned by %s and %s", b, other, w)
			}
			seen[b] = w
		}
	}
	if len(seen) != Buckets {
		t.Fatalf("buckets covered = %d, want %d", len(seen), Buckets)
	}
	counts := map[string]int{}
	for _, w := range seen {
		counts[w]++
	}
	want := float64(Buckets) / float64(len(workers))
	for w, n := range counts {
		if math.Abs(float64(n)-want) > want/2 {
			t.Errorf("%s owns %d buckets, want about %.0f", w, n, want)
		}
	}
	// Removing a worker moves only its own share.
	before := OwnedBuckets("a-1", workers)
	after := OwnedBuckets("a-1", workers[:3])
	if len(after) < len(before) {
		t.Errorf("a worker lost buckets when another left: %d -> %d", len(before), len(after))
	}
}

func TestNextBackoff(t *testing.T) {
	now := time.Now()
	const interval = 24 * time.Hour
	ok := NextBackoff("id", now, interval, 0).Sub(now)
	if ok < interval-interval/10 || ok > interval+interval/10 {
		t.Errorf("success backoff = %v, want the cadence ±10%%", ok)
	}
	prev := time.Duration(0)
	for fail := 1; fail <= 6; fail++ {
		d := NextBackoff("id", now, interval, fail).Sub(now)
		if d > interval+interval/10 {
			t.Errorf("failure %d backoff = %v, want at most the cadence", fail, d)
		}
		if d < prev {
			t.Errorf("failure %d backoff %v is shorter than failure %d's %v", fail, d, fail-1, prev)
		}
		prev = d
	}
}

func TestScanListsEveryRepository(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	for i := range 25 {
		if _, err := s.Upsert(ctx, testRepo(fmt.Sprintf("git.example/o/n%d", i), time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	var cursor uint64
	for range 100 {
		batch, next, err := s.Scan(ctx, cursor, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range batch {
			seen[r.ID] = true
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	if len(seen) != 25 {
		t.Errorf("Scan saw %d repositories, want 25", len(seen))
	}
	if n, err := s.Count(ctx); err != nil || n != 25 {
		t.Errorf("Count = %d, %v", n, err)
	}
}
