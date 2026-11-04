package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/process"
)

type repairedEntryRunner struct {
	v        domain.AppInstance
	identity string
	missing  bool
	calls    []process.Command
}

func (f *repairedEntryRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.calls = append(f.calls, c)
	if c.Program == "docker" && len(c.Args) > 1 && c.Args[0] == "container" && c.Args[1] == "inspect" {
		b, _ := json.Marshal([]any{map[string]any{"Id": f.identity, "Name": "/zah-" + f.v.ID, "Config": map[string]any{"Labels": map[string]string{"org.zah.instance": f.v.ID}, "Env": []string{"HOME=" + f.v.PrivateData}}, "Mounts": []any{map[string]any{"Type": "bind", "Source": f.v.PrivateData, "Destination": f.v.PrivateData, "RW": true}}, "State": map[string]bool{"Running": true}}})
		return process.Result{Stdout: string(b)}, nil
	}
	args := strings.Join(c.Args, "|")
	if c.Program == "distrobox" && strings.HasSuffix(args, "id|-u") {
		return process.Result{Stdout: strconv.Itoa(os.Geteuid())}, nil
	}
	if c.Program == "distrobox" && strings.Contains(args, "|test|-x|") {
		if f.missing {
			return process.Result{}, errors.New("entry missing")
		}
		return process.Result{}, nil
	}
	return process.Result{}, errors.New("unexpected command: installer replay is prohibited")
}
func TestRepairedLinuxInstallationVerification(t *testing.T) {
	for _, mode := range []string{"valid", "missing_entry", "identity_drift", "launch_lease", "launch_token", "wrong_state"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, v, _, _ := instanceCleanupFixture(t)
			v.State = "needs_inspection"
			if e := s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
				t.Fatal(e)
			}
			env, e := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
			if e != nil {
				t.Fatal(e)
			}
			env.Identity = strings.Repeat("a", 64)
			s.Store.Put(ctx, record("environment", env.ID, env))
			f := &repairedEntryRunner{v: v, identity: env.Identity, missing: mode == "missing_entry"}
			s.Backend = backend.Adapter{Runner: f}
			plan := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.verify", "input": map[string]string{"id": v.ID}})
			switch mode {
			case "identity_drift":
				env.Identity = strings.Repeat("b", 64)
			case "launch_lease":
				env.LaunchLease = true
			case "launch_token":
				env.LaunchToken = "unresolved"
			case "wrong_state":
				v.State = "launching"
				s.Store.Put(ctx, record("instance", v.ID, v))
			}
			s.Store.Put(ctx, record("environment", env.ID, env))
			_, e = s.Execute(ctx, plan)
			current, _ := get[domain.AppInstance](ctx, s.Store, "instance", v.ID)
			if mode == "valid" {
				if e != nil || current.State != "ready" {
					t.Fatalf("%s %v", current.State, e)
				}
			} else if e == nil || current.State == "ready" {
				t.Fatal("unsafe verification succeeded", current, e)
			}
			if mode != "valid" && mode != "missing_entry" && len(f.calls) > 0 {
				t.Fatal("ran commands after frozen preconditions changed")
			}
			for _, c := range f.calls {
				if c.Program == "distrobox" && (c.SafeCancel || c.Timeout != 0) {
					t.Fatal("initialization could be interrupted")
				}
			}
		})
	}
}
func TestVerificationPlanRejectsUnresolvedLaunch(t *testing.T) {
	ctx := context.Background()
	s, v, _, _ := instanceCleanupFixture(t)
	v.State = "needs_inspection"
	s.Store.Put(ctx, record("instance", v.ID, v))
	env, _ := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	env.Identity = strings.Repeat("a", 64)
	env.LaunchLease = true
	s.Store.Put(ctx, record("environment", env.ID, env))
	raw, _ := json.Marshal(map[string]any{"operation": "instance.verify", "input": map[string]string{"id": v.ID}})
	if _, e := s.Handle(ctx, "plan.create", raw); e == nil {
		t.Fatal("unresolved launch accepted as installation recovery")
	}
}
