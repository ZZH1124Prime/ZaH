package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/process"
	"github.com/ZZH1124Prime/ZaH/internal/recipe"
)

type maintenanceEntryRunner struct {
	*repairedEntryRunner
	entries      []backend.LinuxProgramCandidate
	inventoryErr error
}

func (f *maintenanceEntryRunner) Run(ctx context.Context, c process.Command) (process.Result, error) {
	for _, arg := range c.Args {
		if arg == "/usr/bin/python3" {
			raw, _ := json.Marshal(f.entries)
			return process.Result{Stdout: string(raw)}, f.inventoryErr
		}
	}
	return f.repairedEntryRunner.Run(ctx, c)
}

func TestLinuxMaintenanceRefreshesSavedEntry(t *testing.T) {
	for _, mode := range []string{"unchanged", "removed", "changed", "inventory_error"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, v, _, _ := instanceCleanupFixture(t)
			v.State = "ready"
			v.Installation = &domain.Installation{RequestSHA256: strings.Repeat("a", 64), Format: "deb", Mode: "native", Stage: "complete"}
			r, env, err := s.request(ctx, v)
			if err != nil {
				t.Fatal(err)
			}
			candidate := backend.LinuxProgramCandidate{Name: "Test app", Kind: "desktop", Path: "/usr/share/applications/test.desktop", SHA256: strings.Repeat("a", 64)}
			r.Recipe.Strategy = "deb"
			r.Recipe.Entry = candidate.Path
			r.Recipe.LinuxEntry = &recipe.LinuxEntry{Kind: candidate.Kind, Path: candidate.Path, SHA256: candidate.SHA256}
			v.Entry = []string{candidate.Path}
			v.RecipeRevision = r.Recipe.Digest()
			env.Identity = strings.Repeat("b", 64)
			env.State = "environment_prepared"
			for _, row := range []domain.Record{record("recipe", v.RecipeRevision, r.Recipe), record("instance", v.ID, v), record("environment", env.ID, env)} {
				if err = s.Store.Put(ctx, row); err != nil {
					t.Fatal(err)
				}
			}
			f := &maintenanceEntryRunner{repairedEntryRunner: &repairedEntryRunner{v: v, identity: env.Identity}, entries: []backend.LinuxProgramCandidate{candidate}}
			switch mode {
			case "removed":
				f.entries = []backend.LinuxProgramCandidate{}
			case "changed":
				f.entries[0].SHA256 = strings.Repeat("c", 64)
			case "inventory_error":
				f.inventoryErr = errors.New("inventory unavailable")
			}
			s.Backend = backend.Adapter{Runner: f}
			err = s.refreshLinuxInstalledEntry(ctx, v, r, env)
			if (err != nil) != (mode == "inventory_error") {
				t.Fatal(err)
			}
			got, err := get[domain.AppInstance](ctx, s.Store, "instance", v.ID)
			if err != nil {
				t.Fatal(err)
			}
			invalid := mode == "removed" || mode == "changed"
			if (got.Installation.Stage == "awaiting_completion") != invalid {
				t.Fatal(got)
			}
			saved, _, err := s.request(ctx, got)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Recipe.PendingEntry != invalid || (saved.Recipe.LinuxEntry == nil) != invalid {
				t.Fatal(saved.Recipe)
			}
			if invalid {
				if len(got.Entry) != 0 {
					t.Fatal(got.Entry)
				}
				p := domain.Plan{Operation: "instance.launch"}
				if err = s.validateInstancePlan(ctx, &p, got); err == nil {
					t.Fatal("removed entry still launchable")
				}
			} else if got.RecipeRevision != v.RecipeRevision {
				t.Fatal("unchanged or unreadable entry overwritten")
			}
		})
	}
}

func TestExternallyRemovedLinuxEntryDoesNotLeaveLaunchLease(t *testing.T) {
	ctx := context.Background()
	s, v, _, _ := instanceCleanupFixture(t)
	v.State = "ready"
	v.Installation = &domain.Installation{RequestSHA256: strings.Repeat("a", 64), Format: "deb", Mode: "native", Stage: "complete"}
	r, env, err := s.request(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	r.Recipe.Strategy = "deb"
	r.Recipe.Entry = "/usr/share/applications/test.desktop"
	r.Recipe.LinuxEntry = &recipe.LinuxEntry{Kind: "desktop", Path: r.Recipe.Entry, SHA256: strings.Repeat("a", 64)}
	v.Entry = []string{r.Recipe.Entry}
	v.RecipeRevision = r.Recipe.Digest()
	env.Identity = strings.Repeat("b", 64)
	env.State = "stopped"
	for _, row := range []domain.Record{record("recipe", v.RecipeRevision, r.Recipe), record("instance", v.ID, v), record("environment", env.ID, env)} {
		if err = s.Store.Put(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	f := &maintenanceEntryRunner{repairedEntryRunner: &repairedEntryRunner{v: v, identity: env.Identity}, entries: []backend.LinuxProgramCandidate{}}
	s.Backend = backend.Adapter{Runner: f}
	s.supervisorBinary = serviceSupervisorFixture(t)
	plan := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.launch", "input": map[string]string{"id": v.ID}})
	if _, err = s.Execute(ctx, plan); !errors.Is(err, backend.ErrLinuxEntryChanged) {
		t.Fatal(err)
	}
	current, err := get[domain.AppInstance](ctx, s.Store, "instance", v.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := get[domain.Environment](ctx, s.Store, "environment", env.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != "ready" || current.Installation.Stage != "awaiting_completion" || len(current.Entry) != 0 {
		t.Fatal(current)
	}
	if after.LaunchLease || after.LaunchToken != "" || after.StopRequested || after.State != "environment_prepared" {
		t.Fatal(after)
	}
	for _, c := range f.calls {
		for _, arg := range c.Args {
			if arg == "supervise" {
				t.Fatal("application launcher invoked despite missing entry")
			}
		}
	}
}
