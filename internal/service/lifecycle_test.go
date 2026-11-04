package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/process"
	"github.com/ZZH1124Prime/ZaH/internal/recipe"
)

type supervisedRunner struct {
	mu                       sync.Mutex
	instance, home, identity string
	running                  bool
	stopCalls                int
	entered                  chan struct{}
	release                  chan struct{}
	once                     sync.Once
	noProof, wrongIdentity   bool
}

func (f *supervisedRunner) Run(ctx context.Context, c process.Command) (process.Result, error) {
	if c.Program == "distrobox" && len(c.Args) > 0 {
		for i, arg := range c.Args {
			if arg == "supervise" {
				nonce, marker := c.Args[i+1], c.Args[i+2]
				f.mu.Lock()
				f.running = true
				f.mu.Unlock()
				b, _ := json.Marshal(process.SuperviseStarted{Protocol: process.SuperviseProtocol, Nonce: nonce, PID: 42})
				if err := os.WriteFile(marker, b, 0600); err != nil {
					return process.Result{}, err
				}
				close(f.entered)
				if f.release != nil {
					select {
					case <-ctx.Done():
						return process.Result{}, ctx.Err()
					case <-f.release:
					}
				}
				if f.noProof {
					return process.Result{}, errors.New("launcher terminated")
				}
				b, _ = json.Marshal(backend.SupervisedCompletion{Protocol: process.SuperviseProtocol, Nonce: nonce, Completed: true, EntryExitCode: 0, Reaped: 2})
				return process.Result{Stdout: string(b)}, nil
			}
		}
		if strings.HasSuffix(strings.Join(c.Args, " "), "id -u") {
			return process.Result{Stdout: fmt.Sprint(os.Geteuid())}, nil
		}
	}
	if c.Program == "docker" && len(c.Args) > 1 && c.Args[1] == "stop" {
		if len(c.Args) != 5 || c.Args[4] != f.identity {
			return process.Result{}, errors.New("not an exact managed stop")
		}
		f.mu.Lock()
		f.stopCalls++
		f.running = false
		f.mu.Unlock()
		if f.release != nil {
			f.once.Do(func() { close(f.release) })
		}
		return process.Result{}, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.identity
	if f.wrongIdentity {
		id = strings.Repeat("e", 64)
	}
	status, pid := "exited", 0
	if f.running {
		status = "running"
		pid = 42
	}
	body, _ := json.Marshal([]any{map[string]any{"Id": id, "Name": "/zah-" + f.instance, "Config": map[string]any{"Labels": map[string]string{"org.zah.instance": f.instance}, "Env": []string{"HOME=" + f.home}}, "Mounts": []any{map[string]any{"Type": "bind", "Source": f.home, "Destination": f.home, "RW": true}}, "State": map[string]any{"Running": f.running, "Status": status, "Pid": pid, "Paused": false, "Restarting": false, "Dead": false}}})
	return process.Result{Stdout: string(body)}, nil
}
func supervisedService(t *testing.T) (*Service, domain.AppInstance, *supervisedRunner) {
	t.Helper()
	s, v, _, _ := instanceCleanupFixture(t)
	ctx := context.Background()
	v.State = "ready"
	env, e := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	if e != nil {
		t.Fatal(e)
	}
	env.Identity = strings.Repeat("b", 64)
	env.State = "stopped"
	if e = s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
		t.Fatal(e)
	}
	if e = s.Store.Put(ctx, record("environment", env.ID, env)); e != nil {
		t.Fatal(e)
	}
	s.supervisorBinary = serviceSupervisorFixture(t)
	configureSupervised(t, s, &v)
	f := &supervisedRunner{instance: v.ID, home: v.PrivateData, identity: env.Identity, entered: make(chan struct{})}
	s.Backend = backend.Adapter{Runner: f}
	return s, v, f
}
func TestNaturalSupervisedExitStopsExactEnvironment(t *testing.T) {
	s, v, f := supervisedService(t)
	p := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.launch", "input": map[string]string{"id": v.ID}})
	if _, e := s.Execute(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	env, _ := get[domain.Environment](context.Background(), s.Store, "environment", v.EnvironmentID)
	if f.stopCalls != 1 || env.LaunchLease || env.State != "stopped" || env.LaunchToken != "" {
		t.Fatalf("incorrect automatic cleanup %+v stops=%d", env, f.stopCalls)
	}
}
func TestStopAllCoordinatesActiveSupervisor(t *testing.T) {
	s, v, f := supervisedService(t)
	f.release = make(chan struct{})
	f.noProof = true
	p := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.launch", "input": map[string]string{"id": v.ID}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ended := make(chan error, 1)
	go func() { _, e := s.Execute(ctx, p); ended <- e }()
	select {
	case <-f.entered:
	case <-ctx.Done():
		t.Fatal("supervisor did not enter")
	}
	stop := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "applications.stopAll", "input": map[string]bool{"confirmed": true}})
	result, e := s.Execute(ctx, stop)
	if e != nil {
		t.Fatal(result, e)
	}
	if e = <-ended; e != nil {
		t.Fatal(e)
	}
	env, _ := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	if f.stopCalls != 1 || env.LaunchLease || env.StopRequested || env.State != "stopped" {
		t.Fatalf("unsafe stop result %+v %d", env, f.stopCalls)
	}
}
func TestStopAllRejectsUnconfirmedAndChangedIdentity(t *testing.T) {
	s, _, f := supervisedService(t)
	ctx := context.Background()
	for _, input := range []any{map[string]any{"confirmed": false}, map[string]any{"confirmed": true, "container": "production"}} {
		b, _ := json.Marshal(map[string]any{"operation": "applications.stopAll", "input": input})
		if _, e := s.Handle(ctx, "plan.create", b); e == nil {
			t.Fatal("unsafe confirmation accepted")
		}
	}
	p := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "applications.stopAll", "input": map[string]bool{"confirmed": true}})
	f.wrongIdentity = true
	if _, e := s.Execute(ctx, p); e == nil {
		t.Fatal("changed backend identity stopped")
	}
	if f.stopCalls != 0 {
		t.Fatal("stop command reached unrelated identity")
	}
}
func TestFamilyExclusionIsMandatoryWithoutRecipeGroups(t *testing.T) {
	s, v, _ := supervisedService(t)
	ctx := context.Background()
	env, _ := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	release, e := s.claimLaunch(ctx, v, &env)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	other := v
	other.ID = domain.NewID()
	other.EnvironmentID = domain.NewID()
	otherEnv := env
	otherEnv.ID = other.EnvironmentID
	otherEnv.Name = "zah-" + other.ID
	otherEnv.LaunchLease = false
	// Same app, even a distinct release and no optional conflict groups.
	rel, _ := get[domain.AppRelease](ctx, s.Store, "release", v.ReleaseID)
	rel.ID = domain.NewID()
	other.ReleaseID = rel.ID
	for _, r := range []domain.Record{record("release", rel.ID, rel), record("instance", other.ID, other), record("environment", otherEnv.ID, otherEnv)} {
		if e = s.Store.Put(ctx, r); e != nil {
			t.Fatal(e)
		}
	}
	if unlock, e := s.claimLaunch(ctx, other, &otherEnv); e == nil {
		unlock()
		t.Fatal("same family allowed concurrently")
	}
	// A different family is independently runnable.
	rel.FamilyID = domain.NewID()
	if e = s.Store.Put(ctx, record("release", rel.ID, rel)); e != nil {
		t.Fatal(e)
	}
	unlock, e := s.claimLaunch(ctx, other, &otherEnv)
	if e != nil {
		t.Fatal("different family blocked", e)
	}
	unlock()
}
func TestLegacyInterruptedLauncherNeverStoppedBeforeHandshake(t *testing.T) {
	s, v, f := supervisedService(t)
	ctx := context.Background()
	v.State = "launching"
	if e := s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
		t.Fatal(e)
	}
	p := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "applications.stopAll", "input": map[string]bool{"confirmed": true}})
	if _, e := s.Execute(ctx, p); e == nil {
		t.Fatal("legacy pending launch reported stopped")
	}
	if f.stopCalls != 0 {
		t.Fatal("stopped before launcher entered")
	}
}

func TestStartedProofDoesNotBlockOnFIFO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "marker")
	if e := syscall.Mkfifo(p, 0600); e != nil {
		t.Fatal(e)
	}
	done := make(chan bool, 1)
	go func() { done <- startedProof(p, strings.Repeat("a", 32)) }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("accepted FIFO")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked opening FIFO")
	}
}
func TestStopAllRejectsNewTargetsAndCancelsFrozenQueuedLaunch(t *testing.T) {
	s, v, f := supervisedService(t)
	ctx := context.Background()
	// Snapshot no eligible environment, then register an already-running target.
	env, _ := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	v.State = "unprepared"
	if e := s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
		t.Fatal(e)
	}
	empty := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "applications.stopAll", "input": map[string]bool{"confirmed": true}})
	v.State = "ready"
	if e := s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Execute(ctx, empty); e == nil {
		t.Fatal("stale empty plan reported all stopped")
	}
	if f.stopCalls != 0 {
		t.Fatal("expanded stale stop scope")
	}
	launch := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.launch", "input": map[string]string{"id": v.ID}})
	j := rpcValue[domain.Job](t, s, "job.submit", map[string]string{"plan_id": launch.ID, "idempotency_key": "queued-test"})
	batch := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "applications.stopAll", "input": map[string]bool{"confirmed": true}})
	if _, e := s.Execute(ctx, batch); e != nil {
		t.Fatal(e)
	}
	current, _ := get[domain.Job](ctx, s.Store, "job", j.ID)
	if current.State != domain.Cancelled {
		t.Fatal("queued app could launch after stop", current.State)
	}
	// New queued work after a confirmation is never silently ignored.
	after := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "applications.stopAll", "input": map[string]bool{"confirmed": true}})
	newLaunch := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.launch", "input": map[string]string{"id": v.ID}})
	_ = rpcValue[domain.Job](t, s, "job.submit", map[string]string{"plan_id": newLaunch.ID, "idempotency_key": "new-queued-test"})
	if _, e := s.Execute(ctx, after); e == nil {
		t.Fatal("new queued launch ignored")
	}
	_ = env
}

func configureSupervised(t *testing.T, s *Service, v *domain.AppInstance) {
	t.Helper()
	ctx := context.Background()
	r, e := get[recipe.Recipe](ctx, s.Store, "recipe", v.RecipeRevision)
	if e != nil {
		t.Fatal(e)
	}
	r.Lifecycle = "process-tree"
	v.RecipeRevision = r.Digest()
	if e = s.Store.Put(ctx, record("recipe", r.Digest(), r)); e != nil {
		t.Fatal(e)
	}
	if e = s.Store.Put(ctx, record("instance", v.ID, *v)); e != nil {
		t.Fatal(e)
	}
}

func TestUnverifiedLifecycleNeverAutomaticallyStopsEnvironment(t *testing.T) {
	s, v, f := supervisedService(t)
	ctx := context.Background()
	r, e := get[recipe.Recipe](ctx, s.Store, "recipe", v.RecipeRevision)
	if e != nil {
		t.Fatal(e)
	}
	r.Lifecycle = ""
	v.RecipeRevision = r.Digest()
	if e = s.Store.Put(ctx, record("recipe", r.Digest(), r)); e != nil {
		t.Fatal(e)
	}
	if e = s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
		t.Fatal(e)
	}
	p := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.launch", "input": map[string]string{"id": v.ID}})
	if _, e = s.Execute(ctx, p); e != nil {
		t.Fatal(e)
	}
	env, _ := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	if f.stopCalls != 0 || !env.LaunchLease {
		t.Fatal("unverified launcher exit treated as app exit", env, f.stopCalls)
	}
}

func TestDefaultLinuxHandshakeKeepsEnvironmentForExplicitStop(t *testing.T) {
	s, v, f := supervisedService(t)
	ctx := context.Background()
	rec, e := s.Store.Get(ctx, "recipe", v.RecipeRevision)
	if e != nil {
		t.Fatal(e)
	}
	var recipeValue recipe.Recipe
	if e = json.Unmarshal(rec.Data, &recipeValue); e != nil {
		t.Fatal(e)
	}
	recipeValue.Lifecycle = ""
	id := recipeValue.Digest()
	if e = s.Store.Put(ctx, record("recipe", id, recipeValue)); e != nil {
		t.Fatal(e)
	}
	v.RecipeRevision = id
	if e = s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
		t.Fatal(e)
	}
	p := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.launch", "input": map[string]string{"id": v.ID}})
	if _, e = s.Execute(ctx, p); e != nil {
		t.Fatal(e)
	}
	env, e := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	if e != nil {
		t.Fatal(e)
	}
	if f.stopCalls != 0 || !env.LaunchLease || env.State != "launcher_exited_application_unknown" {
		t.Fatalf("default recipe inferred auto-stop: %+v", env)
	}
	stop := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.stop", "input": map[string]string{"id": v.ID}})
	if _, e = s.Execute(ctx, stop); e != nil {
		t.Fatal(e)
	}
	if f.stopCalls != 1 {
		t.Fatal("explicit stop missing")
	}
}
func TestRemovalRejectsOutstandingLaunchLease(t *testing.T) {
	s, v, _ := supervisedService(t)
	ctx := context.Background()
	env, e := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	if e != nil {
		t.Fatal(e)
	}
	env.LaunchLease = true
	env.LaunchToken = "pending"
	if e = s.Store.Put(ctx, record("environment", env.ID, env)); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(map[string]any{"operation": "instance.remove", "input": map[string]string{"id": v.ID}})
	if _, e = s.Handle(ctx, "plan.create", raw); e == nil {
		t.Fatal("removal accepted an unresolved launch")
	}
}
