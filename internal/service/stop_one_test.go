package service

import (
	"context"
	"encoding/json"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"strings"
	"testing"
)

func TestSingleStopFreezesOneIdentityAndCanRetry(t *testing.T) {
	s, v, f := supervisedService(t)
	ctx := context.Background()
	f.running = true
	p := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.stop", "input": map[string]string{"id": v.ID}})
	if p.InstanceID != v.ID || len(p.EnvironmentIDs) != 0 {
		t.Fatal("single stop must use control gate, not live worker lock")
	}
	f.wrongIdentity = true
	if _, e := s.Execute(ctx, p); e == nil || f.stopCalls != 0 {
		t.Fatal("stopped foreign environment")
	}
	f.wrongIdentity = false
	out, e := s.Execute(ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	if out.(StopAllResult).Stopped != 1 || f.stopCalls != 1 {
		t.Fatal(out)
	}
	env, _ := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	if env.State != "stopped" || env.LaunchLease {
		t.Fatal(env)
	}
}
func TestSingleStopRejectsChangedFrozenIdentity(t *testing.T) {
	s, v, f := supervisedService(t)
	ctx := context.Background()
	p := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.stop", "input": map[string]string{"id": v.ID}})
	env, _ := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	env.Identity = strings.Repeat("c", 64)
	s.Store.Put(ctx, record("environment", env.ID, env))
	if _, e := s.Execute(ctx, p); e == nil || f.stopCalls != 0 {
		t.Fatal("changed identity accepted")
	}
}
func TestSingleStopRejectsCallerContainerID(t *testing.T) {
	s, v, _ := supervisedService(t)
	b, _ := json.Marshal(map[string]any{"operation": "instance.stop", "input": map[string]string{"id": v.ID, "identity": "foreign"}})
	if _, e := s.Handle(context.Background(), "plan.create", b); e == nil {
		t.Fatal("accepted caller identity")
	}
}

func TestStopAllDoesNotHideAnUnverifiedPartialInstallation(t *testing.T) {
	s, v, f := supervisedService(t)
	ctx := context.Background()
	v.State = "needs_inspection"
	if e := s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
		t.Fatal(e)
	}
	env, _ := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	env.Identity = ""
	env.State = "creation_result_unknown"
	if e := s.Store.Put(ctx, record("environment", env.ID, env)); e != nil {
		t.Fatal(e)
	}
	p := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "applications.stopAll", "input": map[string]bool{"confirmed": true}})
	var in stopAllIntent
	if e := json.Unmarshal(p.Input, &in); e != nil {
		t.Fatal(e)
	}
	if len(in.Targets) != 1 {
		t.Fatal("partial installation disappeared from stop scope")
	}
	value, e := s.Execute(ctx, p)
	if e == nil || value.(StopAllResult).Failed != 1 || f.stopCalls != 0 {
		t.Fatalf("unknown environment treated as stopped: %+v %v", value, e)
	}
}
