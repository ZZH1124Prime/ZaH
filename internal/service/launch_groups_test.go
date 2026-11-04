package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/process"
)

type stoppedGroupRunner struct {
	id, instance string
	running      bool
	calls        int
}

func (f *stoppedGroupRunner) Run(context.Context, process.Command) (process.Result, error) {
	f.calls++
	state := "exited"
	pid := 0
	if f.running {
		state = "running"
		pid = 42
	}
	return process.Result{Stdout: fmt.Sprintf(`[{"Id":%q,"Name":%q,"Config":{"Labels":{"org.zah.instance":%q}},"State":{"Status":%q,"Running":%t,"Paused":false,"Restarting":false,"Dead":false,"Pid":%d}}]`, f.id, "/zah-"+f.instance, f.instance, state, f.running, pid)}, nil
}
func TestConflictGroupFirstLaunchAndDurableStoppedProof(t *testing.T) {
	s, first := fullSource(t)
	ctx := context.Background()
	first.ConflictGroups = []string{"exclusive-audio"}
	if e := s.Store.Put(ctx, record("instance", first.ID, first)); e != nil {
		t.Fatal(e)
	}
	second := first
	second.ID = "second-instance"
	second.EnvironmentID = "second-environment"
	env := domain.Environment{ID: second.EnvironmentID, Name: "zah-" + second.ID, Backend: "distrobox-docker", Ownership: "managed", Identity: strings.Repeat("c", 64)}
	for _, r := range []domain.Record{record("instance", second.ID, second), record("environment", env.ID, env)} {
		if e := s.Store.Put(ctx, r); e != nil {
			t.Fatal(e)
		}
	}
	prior, _ := get[domain.Environment](ctx, s.Store, "environment", first.EnvironmentID)
	unlock, e := s.claimLaunch(ctx, first, &prior)
	if e != nil {
		t.Fatal("first configured launch blocked", e)
	}
	// Another worker cannot take the same group while the first launcher owns it.
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	if release, e := s.claimLaunch(short, second, &env); e == nil {
		release()
		t.Fatal("simultaneous conflict group claim")
	}
	cancel()
	if release, e := s.claimLaunch(ctx, second, &env); e == nil || !errors.Is(e, domain.ErrConflict) {
		if release != nil {
			release()
		}
		t.Fatal("live occupancy must report a conflict", e)
	}
	unlock()
	proof := &stoppedGroupRunner{id: prior.Identity, instance: first.ID, running: true}
	s.Backend = backend.Adapter{Runner: proof}
	if release, e := s.claimLaunch(ctx, second, &env); e == nil {
		release()
		t.Fatal("released durable lease while app may live")
	}
	proof.running = false
	release, e := s.claimLaunch(ctx, second, &env)
	if e != nil {
		t.Fatal(e)
	}
	release()
	old, _ := get[domain.Environment](ctx, s.Store, "environment", first.EnvironmentID)
	current, _ := get[domain.Environment](ctx, s.Store, "environment", second.EnvironmentID)
	if old.LaunchLease || !current.LaunchLease || proof.calls != 2 {
		t.Fatalf("incorrect lease transfer %+v %+v %d", old, current, proof.calls)
	}
}
func TestInterruptedConflictLeaseNeverAutomaticallyReleased(t *testing.T) {
	s, v := fullSource(t)
	ctx := context.Background()
	v.ConflictGroups = []string{"exclusive"}
	v.State = "launching"
	if e := s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
		t.Fatal(e)
	}
	env, _ := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	env.LaunchLease = true
	if e := s.Store.Put(ctx, record("environment", env.ID, env)); e != nil {
		t.Fatal(e)
	}
	// The new candidate overlaps but must not turn a momentary stopped observation
	// into permission while an orphan launcher from the interrupted job may survive.
	other := v
	other.ID = "other-instance"
	other.EnvironmentID = "other-environment"
	other.State = "ready"
	otherEnv := env
	otherEnv.ID = other.EnvironmentID
	otherEnv.Name = "zah-" + other.ID
	otherEnv.LaunchLease = false
	for _, r := range []domain.Record{record("instance", other.ID, other), record("environment", otherEnv.ID, otherEnv)} {
		if e := s.Store.Put(ctx, r); e != nil {
			t.Fatal(e)
		}
	}
	fake := &stoppedGroupRunner{id: env.Identity, instance: v.ID}
	s.Backend = backend.Adapter{Runner: fake}
	if release, e := s.claimLaunch(ctx, other, &otherEnv); e == nil {
		release()
		t.Fatal("released interrupted lease")
	}
	if fake.calls != 0 {
		t.Fatal("should not infer absence of orphan launch from container state")
	}
	// Plan creation itself can now accept a ready instance with configured groups.
	raw, _ := json.Marshal(map[string]any{"operation": "instance.launch", "input": map[string]string{"id": other.ID}})
	if _, e := s.Handle(ctx, "plan.create", raw); e != nil {
		t.Fatal("planning configured group", e)
	}
}
