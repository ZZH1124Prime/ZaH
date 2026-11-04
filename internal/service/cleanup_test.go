package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/catalog"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

func cleanupFixture(t *testing.T) (*Service, catalog.Artifact, string) {
	t.Helper()
	s := catalogService(t)
	source := filepath.Join(t.TempDir(), "original.bin")
	if e := os.WriteFile(source, []byte("material to archive independently"), 0600); e != nil {
		t.Fatal(e)
	}
	a, e := s.Artifacts().Import(context.Background(), source)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.save(context.Background(), "artifact", a.SHA256, a); e != nil {
		t.Fatal(e)
	}
	return s, a, source
}
func cleanupPlan(t *testing.T, s *Service, id string) domain.Plan {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"operation": "artifact.remove", "id": id})
	p, e := s.PlanCleanup(context.Background(), raw)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.save(context.Background(), "plan", p.ID, p, ref("plan", p.ID, "artifact", id, "input")); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestCleanupRemovesOnlyManagedCopy(t *testing.T) {
	s, a, source := cleanupFixture(t)
	ctx := context.Background()
	p := cleanupPlan(t, s, a.SHA256)
	result, e := s.ExecuteCleanup(ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	if result.State != "removed" || result.BytesRemoved != a.Size || !result.AuditRetained || !result.PrivateDataPreserved {
		t.Fatalf("bad evidence %+v", result)
	}
	if _, e = os.Lstat(a.Path); !os.IsNotExist(e) {
		t.Fatal("managed copy still exists", e)
	}
	if b, e := os.ReadFile(source); e != nil || len(b) == 0 {
		t.Fatal("original file changed", e)
	}
	tombstone, e := get[catalog.Artifact](ctx, s.Store, "artifact", a.SHA256)
	if e != nil || tombstone.Availability != "removed" || tombstone.Source.OriginalPath != source {
		t.Fatalf("audit lost %+v %v", tombstone, e)
	}
	if _, e = s.Store.Get(ctx, "plan", p.ID); e != nil {
		t.Fatal("plan lost", e)
	}
	if result, e = s.ExecuteCleanup(ctx, p); e != nil || result.BytesRemoved != 0 {
		t.Fatalf("repeated removal: %+v %v", result, e)
	}
	if _, e = s.ExportCatalogBundle(ctx, filepath.Join(t.TempDir(), "out.zahbundle")); e == nil {
		t.Fatal("unavailable material exported as complete")
	}
}
func TestCleanupBlocksCatalogAndActivePlanReferences(t *testing.T) {
	for _, kind := range []string{"release", "instance", "unsubmitted_plan", "queued_plan", "interrupted_plan", "terminal_plan_with_queued_second_job"} {
		t.Run(kind, func(t *testing.T) {
			s, a, _ := cleanupFixture(t)
			ctx := context.Background()
			fromKind := kind
			id := "dependent"
			if kind == "release" || kind == "instance" {
				if e := s.save(ctx, kind, id, map[string]string{"id": id}, ref(kind, id, "artifact", a.SHA256, "material")); e != nil {
					t.Fatal(e)
				}
			} else {
				fromKind = "plan"
				if e := s.save(ctx, "plan", id, domain.Plan{ID: id, Operation: "install"}, ref("plan", id, "artifact", a.SHA256, "input")); e != nil {
					t.Fatal(e)
				}
				if kind != "unsubmitted_plan" {
					j, _, e := s.Store.SubmitJob(ctx, id, "first")
					if e != nil {
						t.Fatal(e)
					}
					switch kind {
					case "interrupted_plan":
						e = s.Store.TransitionJob(ctx, j.ID, domain.Queued, domain.Interrupted, "")
					case "terminal_plan_with_queued_second_job":
						e = s.Store.TransitionJob(ctx, j.ID, domain.Queued, domain.Cancelled, "")
						if e == nil {
							// Legacy databases can contain multiple jobs for a plan; cleanup must still protect them.
							e = s.save(ctx, "job", "legacy-second", domain.Job{ID: "legacy-second", PlanID: id, IdempotencyKey: "second", State: domain.Queued}, ref("job", "legacy-second", "plan", id, "plan"))
						}
					}
					if e != nil {
						t.Fatal(e)
					}
				}
			}
			_ = fromKind
			raw, _ := json.Marshal(map[string]string{"operation": "artifact.remove", "id": a.SHA256})
			if _, e := s.PlanCleanup(ctx, raw); !errors.Is(e, domain.ErrReferenced) {
				t.Fatalf("active dependency accepted %v", e)
			}
			if _, e := s.Artifacts().Verify(ctx, a.SHA256); e != nil {
				t.Fatal("planning modified material", e)
			}
		})
	}
}
func TestCleanupAllowsRetainedTerminalAudit(t *testing.T) {
	s, a, _ := cleanupFixture(t)
	ctx := context.Background()
	id := "past-plan"
	if e := s.save(ctx, "plan", id, domain.Plan{ID: id, Operation: "previous"}, ref("plan", id, "artifact", a.SHA256, "input")); e != nil {
		t.Fatal(e)
	}
	j, _, e := s.Store.SubmitJob(ctx, id, "previous")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Store.TransitionJob(ctx, j.ID, domain.Queued, domain.Cancelled, ""); e != nil {
		t.Fatal(e)
	}
	p := cleanupPlan(t, s, a.SHA256)
	if _, e = s.ExecuteCleanup(ctx, p); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Store.Get(ctx, "job", j.ID); e != nil {
		t.Fatal("historical job deleted", e)
	}
}
func TestCleanupRejectsReplacedMaterialAndLinks(t *testing.T) {
	for _, variant := range []string{"replaced", "symlink", "hardlink", "external_record_path"} {
		t.Run(variant, func(t *testing.T) {
			s, a, source := cleanupFixture(t)
			p := cleanupPlan(t, s, a.SHA256)
			switch variant {
			case "replaced":
				b, e := os.ReadFile(a.Path)
				if e != nil {
					t.Fatal(e)
				}
				temp := filepath.Join(filepath.Dir(a.Path), "replacement")
				if e = os.WriteFile(temp, b, 0400); e != nil {
					t.Fatal(e)
				}
				if e = os.Rename(temp, a.Path); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				if e := os.Remove(a.Path); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(source, a.Path); e != nil {
					t.Fatal(e)
				}
			case "hardlink":
				if e := os.Link(a.Path, filepath.Join(t.TempDir(), "alias")); e != nil {
					t.Fatal(e)
				}
			case "external_record_path":
				a.Path = source
				if e := s.save(context.Background(), "artifact", a.SHA256, a); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := s.ExecuteCleanup(context.Background(), p); e == nil {
				t.Fatal("unsafe removal accepted")
			}
			if _, e := os.Stat(source); e != nil {
				t.Fatal("original source affected", e)
			}
		})
	}
}
func TestCleanupCancelledBeforeBoundary(t *testing.T) {
	s, a, _ := cleanupFixture(t)
	p := cleanupPlan(t, s, a.SHA256)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := s.ExecuteCleanup(ctx, p); e == nil {
		t.Fatal("cancel ignored")
	}
	if _, e := s.Artifacts().Verify(context.Background(), a.SHA256); e != nil {
		t.Fatal("material removed after cancel", e)
	}
}
func TestCleanupPendingOutcomeCanBeExplicitlyReconciled(t *testing.T) {
	s, a, _ := cleanupFixture(t)
	ctx := context.Background()
	p := cleanupPlan(t, s, a.SHA256)
	job, _, e := s.Store.SubmitJob(ctx, p.ID, "remove")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Store.TransitionJob(ctx, job.ID, domain.Queued, domain.Running, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Store.DB.Exec(`CREATE TRIGGER fail_tombstone BEFORE UPDATE ON artifacts WHEN json_extract(NEW.data,'$.availability')='removed' BEGIN SELECT RAISE(ABORT,'injected'); END`); e != nil {
		t.Fatal(e)
	}
	result, e := s.ExecuteCleanup(ctx, p)
	if e == nil || result.State != "removal_pending" {
		t.Fatalf("partial deletion reported as success %+v %v", result, e)
	}
	if _, e = os.Lstat(a.Path); !os.IsNotExist(e) {
		t.Fatal("test did not reach unlink", e)
	}
	if e = s.Store.TransitionJob(ctx, job.ID, domain.Running, domain.Failed, "tombstone failure"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Store.DB.Exec(`DROP TRIGGER fail_tombstone`); e != nil {
		t.Fatal(e)
	}
	retry := cleanupPlan(t, s, a.SHA256)
	result, e = s.ExecuteCleanup(ctx, retry)
	if e != nil || result.State != "removed" || result.BytesRemoved != 0 {
		t.Fatalf("reconciliation %+v %v", result, e)
	}
}
