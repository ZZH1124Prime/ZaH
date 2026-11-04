package service

import (
	"context"
	"encoding/json"
	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/process"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entryRunner struct {
	*windowsLaunchRunner
	adds int
}

func (f *entryRunner) Run(ctx context.Context, c process.Command) (process.Result, error) {
	if len(c.Args) > 9 && c.Args[3] == "add" {
		var rows []map[string]any
		_ = json.Unmarshal([]byte(f.programs), &rows)
		rows = append(rows, map[string]any{"name": c.Args[7], "path": c.Args[9], "winebridge": false})
		b, _ := json.Marshal(rows)
		f.programs = string(b)
		f.adds++
		return process.Result{}, nil
	}
	return f.windowsLaunchRunner.Run(ctx, c)
}
func TestInstallerCompletionAndMaintenancePersistExplicitEntry(t *testing.T) {
	s, v, base := windowsLifecycleService(t)
	ctx := context.Background()
	v.Installation = &domain.Installation{RequestSHA256: strings.Repeat("a", 64), Format: "exe", Mode: "installer", SourceEntry: "program.exe", Stage: "awaiting_completion"}
	if e := s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
		t.Fatal(e)
	}
	r, env, e := s.request(ctx, v)
	if e != nil {
		t.Fatal(e)
	}
	entry := r.Windows.ExecutablePath
	source, e := backend.InstallerPath(r)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Dir(source), 0700); e != nil {
		t.Fatal(e)
	}
	data, _ := os.ReadFile(entry)
	if e = os.WriteFile(source, data, 0600); e != nil {
		t.Fatal(e)
	}
	f := &entryRunner{windowsLaunchRunner: base}
	s.Backend.Runner = f
	p := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "installation.finish", "input": map[string]any{"id": v.ID, "executable_path": entry, "confirmed": true}})
	if p.RecipeRevision != v.RecipeRevision || len(p.EnvironmentIDs) != 1 {
		t.Fatal(p)
	}
	if _, e = s.Execute(ctx, p); e != nil {
		t.Fatal(e)
	}
	current, _ := get[domain.AppInstance](ctx, s.Store, "instance", v.ID)
	if current.Installation.Stage != "complete" || current.RecipeRevision == v.RecipeRevision || len(current.Entry) != 1 {
		t.Fatal(current)
	}
	after, _ := get[domain.Environment](ctx, s.Store, "environment", env.ID)
	if after.Identity == env.Identity || after.State != "stopped" {
		t.Fatal(after)
	}
	maintenance := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "installation.maintenance", "input": map[string]any{"id": v.ID, "confirmed": true}})
	if _, e = s.Execute(ctx, maintenance); e != nil {
		t.Fatal(e)
	}
	current, _ = get[domain.AppInstance](ctx, s.Store, "instance", v.ID)
	if current.Installation.Stage != "installer_ready" || current.Entry[0] != "ZaH installer" {
		t.Fatal(current)
	}
	if f.adds != 2 {
		t.Fatal(f.adds)
	}
	if _, e = os.Stat(entry); e != nil {
		t.Fatal("maintenance removed installed app", e)
	}
}
func TestInstallerCompletionRejectsChangedEntryAndLaunchLease(t *testing.T) {
	s, v, f := windowsLifecycleService(t)
	ctx := context.Background()
	v.Installation = &domain.Installation{RequestSHA256: strings.Repeat("a", 64), Format: "exe", Mode: "installer", SourceEntry: "program.exe", Stage: "awaiting_completion"}
	_ = s.Store.Put(ctx, record("instance", v.ID, v))
	r, env, _ := s.request(ctx, v)
	input := map[string]any{"operation": "installation.finish", "input": map[string]any{"id": v.ID, "executable_path": r.Windows.ExecutablePath, "confirmed": true}}
	p := rpcValue[domain.Plan](t, s, "plan.create", input)
	env.LaunchLease = true
	_ = s.Store.Put(ctx, record("environment", env.ID, env))
	raw, _ := json.Marshal(input)
	if _, e := s.Handle(ctx, "plan.create", raw); e == nil {
		t.Fatal("accepted active installer")
	}
	if _, e := s.Execute(ctx, p); e == nil {
		t.Fatal("stale plan executed during launch")
	}
	env.LaunchLease = false
	_ = s.Store.Put(ctx, record("environment", env.ID, env))
	data, _ := os.ReadFile(r.Windows.ExecutablePath)
	_ = os.WriteFile(r.Windows.ExecutablePath, append(data, 1), 0600)
	if _, e := s.Execute(ctx, p); e == nil {
		t.Fatal("changed executable accepted")
	}
	current, _ := get[domain.AppInstance](ctx, s.Store, "instance", v.ID)
	if current.Installation.Stage != "awaiting_completion" || f.stops != 0 {
		t.Fatal(current)
	}
}

func TestExplicitFinalEntryMayBeTheOriginalPortableEXE(t *testing.T) {
	s, v, base := windowsLifecycleService(t)
	ctx := context.Background()
	v.Installation = &domain.Installation{RequestSHA256: strings.Repeat("a", 64), Format: "exe", Mode: "installer", SourceEntry: "program.exe", Stage: "awaiting_completion"}
	if e := s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
		t.Fatal(e)
	}
	r, _, e := s.request(ctx, v)
	if e != nil {
		t.Fatal(e)
	}
	original, e := backend.InstallerPath(r)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Dir(original), 0700); e != nil {
		t.Fatal(e)
	}
	bytes, e := os.ReadFile(r.Windows.ExecutablePath)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(original, bytes, 0600); e != nil {
		t.Fatal(e)
	}
	s.Backend.Runner = &entryRunner{windowsLaunchRunner: base}
	plan := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "installation.finish", "input": map[string]any{"id": v.ID, "executable_path": original, "confirmed": true}})
	if _, e = s.Execute(ctx, plan); e != nil {
		t.Fatal(e)
	}
	current, _ := get[domain.AppInstance](ctx, s.Store, "instance", v.ID)
	if current.Installation.Stage != "complete" {
		t.Fatal(current)
	}
}
