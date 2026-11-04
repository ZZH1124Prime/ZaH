package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/storage"
)

type executorFunc func(context.Context, domain.Plan) (any, error)

func (f executorFunc) Execute(ctx context.Context, p domain.Plan) (any, error) { return f(ctx, p) }
func jobFixture(t *testing.T) (*storage.Store, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(dir, "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, dir
}
func queuedJob(t *testing.T, s *storage.Store, operation string, envs ...string) domain.Job {
	t.Helper()
	p := domain.Plan{ID: domain.NewID(), Operation: operation, EnvironmentIDs: envs, Steps: []domain.Step{}, CreatedAt: time.Now().UTC()}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Put(context.Background(), domain.Record{Kind: "plan", ID: p.ID, Data: b}); err != nil {
		t.Fatal(err)
	}
	j, fresh, err := s.SubmitJob(context.Background(), p.ID, domain.NewID())
	if err != nil || !fresh {
		t.Fatalf("submit fresh=%v: %v", fresh, err)
	}
	return j
}
func readJob(t *testing.T, s *storage.Store, id string) domain.Job {
	t.Helper()
	r, err := s.Get(context.Background(), "job", id)
	if err != nil {
		t.Fatal(err)
	}
	var j domain.Job
	if err = json.Unmarshal(r.Data, &j); err != nil {
		t.Fatal(err)
	}
	return j
}
func awaitRun(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not finish")
		return nil
	}
}
func awaitStarted(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not start")
	}
}

func TestRunClaimsOnceAndRefusesReplay(t *testing.T) {
	s, dir := jobFixture(t)
	j := queuedJob(t, s, "test.copy", "environment-alpha")
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	execute := executorFunc(func(ctx context.Context, p domain.Plan) (any, error) {
		calls.Add(1)
		if ID(ctx) != j.ID {
			return nil, errors.New("job context identity missing")
		}
		close(started)
		<-release
		return map[string]bool{"copied": true}, nil
	})
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), s, dir, j.ID, execute) }()
	awaitStarted(t, started)
	if err := Run(context.Background(), s, dir, j.ID, execute); err == nil {
		t.Error("concurrent worker claimed same job")
	}
	running := readJob(t, s, j.ID)
	if running.State != domain.Running || !Alive(running.WorkerPID, running.WorkerStart) {
		t.Errorf("running evidence=%+v", running)
	}
	close(release)
	if err := awaitRun(t, done); err != nil {
		t.Fatal(err)
	}
	final := readJob(t, s, j.ID)
	if final.State != domain.Succeeded || string(final.Result) != "{\"copied\":true}" {
		t.Fatalf("final evidence=%+v", final)
	}
	if err := Run(context.Background(), s, dir, j.ID, execute); err == nil {
		t.Fatal("completed work replayed")
	}
	if calls.Load() != 1 {
		t.Fatalf("executions=%d", calls.Load())
	}
}

func TestDurableCancellationAndCompletedEvidence(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel-safe", true: "already-committed"}[committed], func(t *testing.T) {
			s, dir := jobFixture(t)
			j := queuedJob(t, s, "test.transaction")
			started := make(chan struct{})
			done := make(chan error, 1)
			execute := executorFunc(func(ctx context.Context, p domain.Plan) (any, error) {
				close(started)
				<-ctx.Done()
				if committed {
					return map[string]bool{"committed": true}, nil
				}
				return map[string]bool{"committed": false}, ctx.Err()
			})
			go func() { done <- Run(context.Background(), s, dir, j.ID, execute) }()
			awaitStarted(t, started)
			if err := s.TransitionJob(context.Background(), j.ID, domain.Running, domain.CancelRequested, ""); err != nil {
				t.Fatal(err)
			}
			err := awaitRun(t, done)
			final := readJob(t, s, j.ID)
			if committed {
				if err != nil || final.State != domain.Succeeded || string(final.Result) != "{\"committed\":true}" {
					t.Fatalf("committed transaction lost: %+v %v", final, err)
				}
			} else {
				if !errors.Is(err, context.Canceled) || final.State != domain.Cancelled {
					t.Fatalf("cancel failed: %+v %v", final, err)
				}
			}
		})
	}
}

func TestManualAndFailureNeverReplay(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		state domain.JobState
	}{{"manual", backend.ErrManual, domain.WaitingUser}, {"failure", errors.New("simulated failure"), domain.Failed}} {
		t.Run(tc.name, func(t *testing.T) {
			s, dir := jobFixture(t)
			j := queuedJob(t, s, "test.operation")
			var calls int
			execute := executorFunc(func(context.Context, domain.Plan) (any, error) {
				calls++
				return map[string]string{"stage": "checked"}, tc.err
			})
			if err := Run(context.Background(), s, dir, j.ID, execute); !errors.Is(err, tc.err) {
				t.Fatal(err)
			}
			final := readJob(t, s, j.ID)
			if final.State != tc.state || len(final.Result) == 0 {
				t.Fatalf("evidence=%+v", final)
			}
			if err := Run(context.Background(), s, dir, j.ID, execute); err == nil || calls != 1 {
				t.Fatalf("replayed manual/failed work calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestLaunchReleasesGlobalButRetainsEnvironmentLock(t *testing.T) {
	s, dir := jobFixture(t)
	launch := queuedJob(t, s, "instance.launch", "environment-zulu", "environment-alpha", "environment-zulu")
	same := queuedJob(t, s, "test.modify", "environment-alpha")
	other := queuedJob(t, s, "test.modify", "environment-bravo")
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), s, dir, launch.ID, executorFunc(func(context.Context, domain.Plan) (any, error) { close(started); <-release; return nil, nil }))
	}()
	awaitStarted(t, started)
	calls := 0
	execute := executorFunc(func(context.Context, domain.Plan) (any, error) { calls++; return nil, nil })
	if err := Run(context.Background(), s, dir, same.ID, execute); err == nil {
		t.Error("same environment allowed concurrent modification")
	}
	if got := readJob(t, s, same.ID); got.State != domain.Queued {
		t.Errorf("blocked job changed: %+v", got)
	}
	if err := Run(context.Background(), s, dir, other.ID, execute); err != nil {
		t.Errorf("unrelated environment blocked by application lifetime: %v", err)
	}
	close(release)
	if err := awaitRun(t, done); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), s, dir, same.ID, execute); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("modification executions=%d", calls)
	}
}

func TestRecoverChecksBootAndStartIdentityWithoutReplay(t *testing.T) {
	s, _ := jobFixture(t)
	ctx := context.Background()
	identity, err := Identity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	pending := queuedJob(t, s, "test.pending")
	live := queuedJob(t, s, "test.live")
	stale := queuedJob(t, s, "test.stale")
	claimed := queuedJob(t, s, "test.claimed")
	for _, j := range []domain.Job{live, stale} {
		if err = s.TransitionJob(ctx, j.ID, domain.Queued, domain.Running, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.SetWorker(ctx, live.ID, os.Getpid(), identity); err != nil {
		t.Fatal(err)
	}
	for _, j := range []domain.Job{stale, claimed} {
		if err = s.SetWorker(ctx, j.ID, os.Getpid(), "different-boot:1"); err != nil {
			t.Fatal(err)
		}
	}
	if err = Recover(ctx, s); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   string
		want domain.JobState
	}{{pending.ID, domain.Queued}, {live.ID, domain.Running}, {stale.ID, domain.Interrupted}, {claimed.ID, domain.Interrupted}} {
		if j := readJob(t, s, tc.id); j.State != tc.want {
			t.Errorf("job=%s got=%s want=%s", tc.id, j.State, tc.want)
		}
	}
	if err = Recover(ctx, s); err != nil {
		t.Fatalf("recovery not stable: %v", err)
	}
}

func TestRunRejectsUnsafeIDsBeforeExecuting(t *testing.T) {
	s, dir := jobFixture(t)
	called := false
	execute := executorFunc(func(context.Context, domain.Plan) (any, error) { called = true; return nil, nil })
	for _, id := range []string{"../job-id", "short", "UPPERCASE-id", "job/id-unsafe"} {
		if err := Run(context.Background(), s, dir, id, execute); err == nil {
			t.Errorf("accepted %q", id)
		}
	}
	j := queuedJob(t, s, "test.invalid-env", "../unsafe-environment")
	if err := Run(context.Background(), s, dir, j.ID, execute); err == nil {
		t.Error("accepted unsafe environment lock path")
	}
	if called {
		t.Fatal("executor called for invalid lock identity")
	}
}

// TestWorkerHelper is activated only in a test-owned subprocess. Its executor
// blocks after persisting running state; no application/backend is invoked.
func TestWorkerHelper(t *testing.T) {
	if os.Getenv("ZAH_JOB_HELPER") != "1" {
		return
	}
	s, err := storage.Open(os.Getenv("ZAH_JOB_DATABASE"))
	if err != nil {
		os.Exit(40)
	}
	defer s.Close()
	err = Run(context.Background(), s, os.Getenv("ZAH_JOB_RUNTIME"), os.Getenv("ZAH_JOB_ID"), executorFunc(func(ctx context.Context, p domain.Plan) (any, error) {
		if err := os.WriteFile(os.Getenv("ZAH_JOB_READY"), []byte("running"), 0600); err != nil {
			return nil, err
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	if err != nil {
		os.Exit(41)
	}
	os.Exit(0)
}

func TestKilledWorkerIsInterruptedAndNeverReplayed(t *testing.T) {
	s, dir := jobFixture(t)
	j := queuedJob(t, s, "test.crash", "environment-crash")
	ready := filepath.Join(dir, "worker.ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerHelper$")
	cmd.Env = append(os.Environ(), "ZAH_JOB_HELPER=1", "ZAH_JOB_DATABASE="+filepath.Join(dir, "catalog.sqlite"), "ZAH_JOB_RUNTIME="+dir, "ZAH_JOB_ID="+j.ID, "ZAH_JOB_READY="+ready, "GORACE=atexit_sleep_ms=0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not reach executor")
		}
		time.Sleep(5 * time.Millisecond)
	}
	running := readJob(t, s, j.ID)
	if running.State != domain.Running || running.WorkerPID != cmd.Process.Pid || !Alive(running.WorkerPID, running.WorkerStart) {
		t.Fatalf("worker evidence=%+v", running)
	}
	if err := Recover(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if readJob(t, s, j.ID).State != domain.Running {
		t.Fatal("live worker marked interrupted")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("killed worker returned success")
	}
	if err := Recover(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	interrupted := readJob(t, s, j.ID)
	if interrupted.State != domain.Interrupted {
		t.Fatalf("worker failure lost: %+v", interrupted)
	}
	called := false
	if err := Run(context.Background(), s, dir, j.ID, executorFunc(func(context.Context, domain.Plan) (any, error) { called = true; return nil, nil })); err == nil || called {
		t.Fatalf("interrupted work replayed called=%v err=%v", called, err)
	}
}
