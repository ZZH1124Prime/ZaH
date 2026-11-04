package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/migration"
)

func TestWorkbenchAutomaticallyExportsWindowsAndRestoresSavedData(t *testing.T) {
	s, v, _ := windowsLifecycleService(t)
	ctx := context.Background()
	r, _, err := s.request(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bottle.yml", "system.reg", "user.reg", "userdef.reg"} {
		if err = os.WriteFile(filepath.Join(r.Windows.Bottle.Path, name), []byte("fixture metadata"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(r.Windows.Bottle.Path, "drive_c/saved.txt"), []byte("saved application data"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "windows.zahbundle")
	// The ordinary migration UI supplies only instance IDs, never a manual archive.
	result, err := s.ExportWorkbenchBundle(ctx, WorkbenchBundleInput{Path: path, ColdConfirmed: true, Instances: []WorkbenchInstanceInput{{ID: v.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "workbench_snapshot_packaged_needs_target_restore" {
		t.Fatal(result.State)
	}
	extracted := filepath.Join(t.TempDir(), "extracted")
	if _, err = migration.Import(ctx, path, extracted, migration.Limits{}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(extracted, "environments", v.ID, "bottle.tar.gz")
	if _, err = os.Stat(archive); err != nil {
		t.Fatal(err)
	}
	dst := catalogService(t)
	restored, err := dst.RestoreEnvironmentBundle(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := get[domain.AppInstance](ctx, dst.Store, "instance", restored.IDMap["instance:"+v.ID])
	if err != nil || got.State != "needs_runtime_restore" {
		t.Fatalf("%+v %v", got, err)
	}
	if b, e := os.ReadFile(filepath.Join(got.PrivateData, "notes.txt")); e != nil || string(b) != "personal notes" {
		t.Fatalf("saved HOME lost: %q %v", b, e)
	}
}

func TestWorkbenchPlanFreezesSelectionAndExecutionLocks(t *testing.T) {
	s, v := fullSource(t)
	ctx := context.Background()
	input := map[string]any{"path": filepath.Join(t.TempDir(), "all.zahbundle"), "cold_confirmed": true}
	p := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "workbench.export", "input": input})
	var in WorkbenchBundleInput
	if e := json.Unmarshal(p.Input, &in); e != nil {
		t.Fatal(e)
	}
	if !in.IncludeCatalog || len(in.Instances) != 1 || in.Instances[0].ID != v.ID || len(p.EnvironmentIDs) != 1 || p.EnvironmentIDs[0] != v.EnvironmentID {
		t.Fatalf("plan omitted frozen identities: %+v %+v", p, in)
	}
	// The complete pipeline uses a deterministic backend runner and isolated data.
	result, e := s.Execute(ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	if len(result.(WorkbenchBundleResult).Instances) != 1 {
		t.Fatalf("%+v", result)
	}
	// A moved environment must not be accessed under the old environment lock.
	v.EnvironmentID = "different-environment"
	if e = s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Execute(ctx, p); e == nil {
		t.Fatal("executed after environment identity changed")
	}
}

func TestWorkbenchPlanRejectsUnconfirmedOrIncompleteCatalog(t *testing.T) {
	s, v := fullSource(t)
	ctx := context.Background()
	second := v
	second.ID = "another-instance"
	if e := s.Store.Put(ctx, record("instance", second.ID, second)); e != nil {
		t.Fatal(e)
	}
	for _, in := range []WorkbenchBundleInput{
		{Path: filepath.Join(t.TempDir(), "cold.zahbundle"), Instances: []WorkbenchInstanceInput{{ID: v.ID}}},
		{Path: filepath.Join(t.TempDir(), "partial.zahbundle"), ColdConfirmed: true, IncludeCatalog: true, Instances: []WorkbenchInstanceInput{{ID: v.ID}}},
	} {
		raw, _ := json.Marshal(map[string]any{"operation": "workbench.export", "input": in})
		if _, e := s.Handle(ctx, "plan.create", raw); e == nil {
			t.Fatal("accepted incomplete reviewed backup")
		}
	}
}
