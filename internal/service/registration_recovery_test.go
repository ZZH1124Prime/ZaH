package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

func TestRegistrationRecoveryRequiresRejectedLatestLaunch(t *testing.T) {
	for _, mode := range []string{"rejected", "unknown", "worker_alive", "started", "newer_launch", "stop_requested"} {
		t.Run(mode, func(t *testing.T) {
			s, v, _ := windowsLifecycleService(t)
			ctx := context.Background()
			_, env, err := s.request(ctx, v)
			if err != nil {
				t.Fatal(err)
			}
			var registration backend.BottlesRegistration
			if err = json.Unmarshal(env.Registration, &registration); err != nil {
				t.Fatal(err)
			}
			v.State = "launching"
			env.State = "launching"
			env.LaunchLease = true
			env.LaunchToken = "rejected-launch"
			if mode == "stop_requested" {
				env.StopRequested = true
			}
			proof := backend.Evidence{ResourceID: env.Identity, State: "launch_not_started"}
			if mode == "unknown" {
				proof.State = "application_state_unknown"
			}
			raw, _ := json.Marshal(proof)
			p := domain.Plan{ID: domain.NewID(), Operation: "instance.launch", InstanceID: v.ID}
			j := domain.Job{ID: domain.NewID(), PlanID: p.ID, State: domain.Failed, WorkerPID: 2147483647, Result: raw, CreatedAt: time.Now().UTC()}
			if mode == "worker_alive" {
				j.WorkerPID = os.Getpid()
			}
			for _, r := range []domain.Record{record("instance", v.ID, v), record("environment", env.ID, env), record("plan", p.ID, p), record("job", j.ID, j)} {
				if err = s.Store.Put(ctx, r); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "started" {
				dir := filepath.Join(v.PrivateData, ".zah-runtime")
				os.MkdirAll(dir, 0700)
				os.WriteFile(filepath.Join(dir, "run-"+env.LaunchToken+".started"), []byte("started"), 0600)
			}
			if mode == "newer_launch" {
				j.ID = domain.NewID()
				j.State = domain.Running
				j.CreatedAt = j.CreatedAt.Add(time.Second)
				s.Store.Put(ctx, record("job", j.ID, j))
			}
			input, _ := json.Marshal(manualRegistrationInput{ID: v.ID, BottlePath: registration.Bottle.Path, ExecutablePath: registration.ExecutablePath, ProgramName: registration.ProgramName})
			_, err = s.registerManual(ctx, domain.Plan{InstanceID: v.ID, RecipeRevision: v.RecipeRevision, Input: input})
			current, _ := get[domain.Environment](ctx, s.Store, "environment", env.ID)
			if mode == "rejected" {
				if err != nil || current.LaunchLease || current.LaunchToken != "" {
					t.Fatalf("recovery: %+v %v", current, err)
				}
			} else if err == nil || !current.LaunchLease || current.LaunchToken != env.LaunchToken {
				t.Fatalf("unsafe recovery: %+v %v", current, err)
			}
		})
	}
}
