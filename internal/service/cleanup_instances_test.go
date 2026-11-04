package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/catalog"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/process"
	"golang.org/x/sys/unix"
)

func instanceCleanupFixture(t *testing.T) (*Service, domain.AppInstance, catalog.Artifact, cleanupChecks) {
	t.Helper()
	s, a, _ := cleanupFixture(t)
	s.Backend = backend.Adapter{Runner: &absenceCleanupRunner{}}
	family := rpcValue[domain.AppFamily](t, s, "family.create", map[string]string{"name": "cleanup app"})
	release := rpcValue[domain.AppRelease](t, s, "release.create", map[string]any{"family_id": family.ID, "platform": "linux", "arch": "amd64", "artifacts": []string{a.SHA256}})
	recipe := rpcValue[struct {
		ID string `json:"id"`
	}](t, s, "recipe.add", map[string]any{"schema_version": 1, "id": "cleanup", "revision": "1", "platform": "linux", "architecture": "amd64", "strategy": "appimage", "image": "docker.io/library/ubuntu@sha256:" + strings.Repeat("a", 64), "entry": "squashfs-root/AppRun"})
	instance := rpcValue[domain.AppInstance](t, s, "instance.create", map[string]string{"release_id": release.ID, "recipe_id": recipe.ID, "name": "private instance"})
	if e := os.MkdirAll(instance.PrivateData, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(instance.PrivateData, "notes.txt"), []byte("personal notes"), 0600); e != nil {
		t.Fatal(e)
	}
	proc := t.TempDir()
	if e := os.Mkdir(filepath.Join(proc, "self"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(proc, "self", "mountinfo"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	return s, instance, a, cleanupChecks{procRoot: proc}
}
func saveInstanceCleanupPlan(t *testing.T, s *Service, v domain.AppInstance, op string, checks cleanupChecks) domain.Plan {
	t.Helper()
	p, e := s.planInstanceCleanup(context.Background(), op, v.ID, true, true, checks)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.save(context.Background(), "plan", p.ID, p, ref("plan", p.ID, "instance", v.ID, "target")); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestForgetInstanceDetachesCatalogAndPreservesEverythingElse(t *testing.T) {
	s, v, a, checks := instanceCleanupFixture(t)
	ctx := context.Background()
	release, e := get[domain.AppRelease](ctx, s.Store, "release", v.ReleaseID)
	if e != nil {
		t.Fatal(e)
	}
	family, e := get[domain.AppFamily](ctx, s.Store, "family", release.FamilyID)
	if e != nil {
		t.Fatal(e)
	}
	family.DefaultInstanceID = v.ID
	if e = s.save(ctx, "family", family.ID, family, ref("family", family.ID, "instance", v.ID, "default")); e != nil {
		t.Fatal(e)
	}
	external := filepath.Join(t.TempDir(), "external")
	if e = os.Mkdir(external, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(external, "keep"), []byte("outside"), 0600); e != nil {
		t.Fatal(e)
	}
	binding := domain.FileBinding{ID: domain.NewID(), InstanceID: v.ID, HostPath: external, TargetPath: "/mnt/zah/files", Access: "write", State: "needs_binding"}
	if e = s.save(ctx, "binding", binding.ID, binding, ref("binding", binding.ID, "instance", v.ID, "binding")); e != nil {
		t.Fatal(e)
	}
	capability := domain.CapabilityResult{ID: domain.NewID(), InstanceID: v.ID, Capability: "sound", State: "unknown"}
	if e = s.save(ctx, "capability", capability.ID, capability, ref("capability", capability.ID, "instance", v.ID, "instance")); e != nil {
		t.Fatal(e)
	}
	p := saveInstanceCleanupPlan(t, s, v, "instance.forget", checks)
	result, e := s.executeInstanceCleanup(ctx, p, checks)
	if e != nil || result.State != "forgotten" || !result.PrivateDataPreserved {
		t.Fatalf("forget %+v %v", result, e)
	}
	for _, path := range []string{filepath.Join(v.PrivateData, "notes.txt"), filepath.Join(external, "keep"), a.Path} {
		if _, e = os.Stat(path); e != nil {
			t.Fatal("preserved data lost", e)
		}
	}
	for _, kind := range []string{"instance", "binding", "capability"} {
		rows, e := s.Store.List(ctx, kind)
		if e != nil || len(rows) != 0 {
			t.Fatalf("live catalog retained %s %+v %v", kind, rows, e)
		}
	}
	audit, e := get[domain.AppInstance](ctx, s.Store, "instance", v.ID)
	if e != nil || audit.State != "forgotten" {
		t.Fatal("audit tombstone missing", e)
	}
	family, e = get[domain.AppFamily](ctx, s.Store, "family", family.ID)
	if e != nil || family.DefaultInstanceID != "" {
		t.Fatal("default still points at forgotten instance", e)
	}
	refs, e := s.Store.References(ctx, "artifact", a.SHA256)
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range refs {
		if r.FromKind == "instance" {
			t.Fatal("instance material reference not detached")
		}
	}
	rows, refs, e := s.Store.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range rows {
		if r.Kind == "instance" || r.Kind == "binding" || r.Kind == "capability" || r.Kind == "environment" {
			t.Fatal("forgotten closure leaked into snapshot", r.Kind)
		}
	}
	if e = validateCatalogSnapshot(catalogSnapshot{Format: catalogBundleFormat, SchemaVersion: 1, Records: rows, References: refs}); e != nil {
		t.Fatal("snapshot invalid after forgetting", e)
	}
	if _, e = s.executeInstanceCleanup(ctx, p, checks); e != nil {
		t.Fatal("forget not idempotent", e)
	}
}
func TestForgetInstanceRollsBackReferenceChanges(t *testing.T) {
	s, v, _, checks := instanceCleanupFixture(t)
	p := saveInstanceCleanupPlan(t, s, v, "instance.forget", checks)
	if _, e := s.Store.DB.Exec(`CREATE TRIGGER fail_forget BEFORE DELETE ON resource_refs BEGIN SELECT RAISE(ABORT,'injected'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.executeInstanceCleanup(context.Background(), p, checks); e == nil {
		t.Fatal("injected rollback failed")
	}
	after, e := get[domain.AppInstance](context.Background(), s.Store, "instance", v.ID)
	if e != nil || after.State != "unprepared" {
		t.Fatal("partial tombstone committed", e)
	}
	refs, e := s.Store.References(context.Background(), "release", v.ReleaseID)
	if e != nil || len(refs) != 1 {
		t.Fatal("partial detach", e)
	}
}
func TestPrivateDataRemovalDoesNotFollowLinksOrRemoveSiblingBindings(t *testing.T) {
	s, v, a, checks := instanceCleanupFixture(t)
	outside := filepath.Join(t.TempDir(), "music")
	if e := os.WriteFile(outside, []byte("external song"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(outside, filepath.Join(v.PrivateData, "outside-link")); e != nil {
		t.Fatal(e)
	}
	restored := filepath.Join(filepath.Dir(v.PrivateData), "bindings", "copy")
	if e := os.MkdirAll(restored, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(restored, "keep"), []byte("restored shared data"), 0600); e != nil {
		t.Fatal(e)
	}
	nested := filepath.Join(v.PrivateData, "readonly")
	if e := os.Mkdir(nested, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(nested, "file"), []byte("delete"), 0400); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(nested, 0500); e != nil {
		t.Fatal(e)
	}
	p := saveInstanceCleanupPlan(t, s, v, "instance.data.remove", checks)
	result, e := s.executeInstanceCleanup(context.Background(), p, checks)
	if e != nil || result.State != "private_data_removed" || result.PrivateDataPreserved {
		t.Fatalf("remove %+v %v", result, e)
	}
	if _, e = os.Lstat(v.PrivateData); !os.IsNotExist(e) {
		t.Fatal("HOME retained", e)
	}
	for _, path := range []string{outside, a.Path, filepath.Join(restored, "keep")} {
		if _, e = os.Stat(path); e != nil {
			t.Fatal("unselected data removed", e)
		}
	}
	after, e := get[domain.AppInstance](context.Background(), s.Store, "instance", v.ID)
	if e != nil || after.State != "unprepared" {
		t.Fatal("original state not restored", e)
	}
	if result, e = s.executeInstanceCleanup(context.Background(), p, checks); e != nil || result.BytesRemoved != 0 {
		t.Fatalf("repeat deletion %+v %v", result, e)
	}
}
func TestPrivateCleanupRejectsNewDataMountAndOpenReferences(t *testing.T) {
	for _, variant := range []string{"changed", "mount", "descriptor", "cwd", "mapped", "root_symlink", "special", "lease", "runtime_label_only"} {
		t.Run(variant, func(t *testing.T) {
			s, v, _, checks := instanceCleanupFixture(t)
			p := saveInstanceCleanupPlan(t, s, v, "instance.data.remove", checks)
			switch variant {
			case "changed":
				if e := os.WriteFile(filepath.Join(v.PrivateData, "new"), []byte("new user content"), 0600); e != nil {
					t.Fatal(e)
				}
			case "mount":
				if e := os.WriteFile(filepath.Join(checks.procRoot, "self", "mountinfo"), []byte("1 0 0:1 / "+v.PrivateData+" rw - tmpfs tmpfs rw\n"), 0600); e != nil {
					t.Fatal(e)
				}
			case "descriptor", "cwd", "mapped":
				proc := filepath.Join(checks.procRoot, "123")
				if e := os.MkdirAll(filepath.Join(proc, "fd"), 0700); e != nil {
					t.Fatal(e)
				}
				cwd := "/"
				if variant == "cwd" {
					cwd = v.PrivateData
				}
				if e := os.Symlink(cwd, filepath.Join(proc, "cwd")); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink("/", filepath.Join(proc, "root")); e != nil {
					t.Fatal(e)
				}
				maps := []byte{}
				if variant == "descriptor" {
					if e := os.Symlink(filepath.Join(v.PrivateData, "notes.txt"), filepath.Join(proc, "fd", "3")); e != nil {
						t.Fatal(e)
					}
				}
				if variant == "mapped" {
					tree, e := inspectCleanupTree(context.Background(), s.Paths.Data, v.ID, v.PrivateData)
					if e != nil {
						t.Fatal(e)
					}
					for _, entry := range tree {
						if entry.Kind == "file" {
							maps = []byte(fmt.Sprintf("0000-1000 rw-p 00000000 %x:%x %d /alias\n", unix.Major(entry.Device), unix.Minor(entry.Device), entry.Inode))
						}
					}
				}
				if e := os.WriteFile(filepath.Join(proc, "maps"), maps, 0600); e != nil {
					t.Fatal(e)
				}
			case "root_symlink":
				old := v.PrivateData + "-kept"
				if e := os.Rename(v.PrivateData, old); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(old, v.PrivateData); e != nil {
					t.Fatal(e)
				}
			case "special":
				if e := unix.Mkfifo(filepath.Join(v.PrivateData, "pipe"), 0600); e != nil {
					t.Fatal(e)
				}
			case "lease":
				env, e := get[domain.Environment](context.Background(), s.Store, "environment", v.EnvironmentID)
				if e != nil {
					t.Fatal(e)
				}
				env.LaunchLease = true
				if e = s.save(context.Background(), "environment", env.ID, env); e != nil {
					t.Fatal(e)
				}
			case "runtime_label_only":
				env, e := get[domain.Environment](context.Background(), s.Store, "environment", v.EnvironmentID)
				if e != nil {
					t.Fatal(e)
				}
				env.State = "removed"
				env.Identity = strings.Repeat("b", 64)
				if e = s.save(context.Background(), "environment", env.ID, env); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := s.executeInstanceCleanup(context.Background(), p, checks); e == nil {
				t.Fatal("unsafe deletion accepted")
			}
		})
	}
}

type absenceCleanupRunner struct {
	err     error
	present bool
	calls   int
}

func (r *absenceCleanupRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	r.calls++
	if c.Program != "docker" || !strings.Contains(strings.Join(c.Args, " "), "container ls") {
		return process.Result{}, errors.New("unexpected command")
	}
	out := process.Result{ExitCode: 0}
	if r.present {
		out.Stdout = strings.Repeat("b", 64)
	}
	return out, r.err
}
func TestArchivedCleanupRequiresSuccessfulRemovalAndLiveAbsence(t *testing.T) {
	for _, variant := range []string{"absent", "daemon_failure", "recreated"} {
		t.Run(variant, func(t *testing.T) {
			s, v, _, checks := instanceCleanupFixture(t)
			ctx := context.Background()
			env, e := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
			if e != nil {
				t.Fatal(e)
			}
			env.State = "removed"
			env.Identity = strings.Repeat("b", 64)
			env.CreationEvidence, _ = json.Marshal([]backend.Evidence{{ResourceID: env.Identity, State: "removed", Result: process.Result{ExitCode: 0}}})
			if e = s.save(ctx, "environment", env.ID, env); e != nil {
				t.Fatal(e)
			}
			v.State = "archived"
			if e = s.save(ctx, "instance", v.ID, v); e != nil {
				t.Fatal(e)
			}
			runner := &absenceCleanupRunner{}
			if variant == "daemon_failure" {
				runner.err = errors.New("daemon unavailable")
			}
			if variant == "recreated" {
				runner.present = true
			}
			s.Backend = backend.Adapter{Runner: runner}
			p, e := s.planInstanceCleanup(ctx, "instance.data.remove", v.ID, true, true, checks)
			if variant != "absent" {
				if e == nil {
					t.Fatal("absence failure accepted")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = s.save(ctx, "plan", p.ID, p, ref("plan", p.ID, "instance", v.ID, "target")); e != nil {
				t.Fatal(e)
			}
			if _, e = s.executeInstanceCleanup(ctx, p, checks); e != nil {
				t.Fatal(e)
			}
			if runner.calls < 4 {
				t.Fatal("absence not checked at planning and execution")
			}
		})
	}
}
func TestPrivateCleanupPartialFailureRequiresNewReviewedPlan(t *testing.T) {
	s, v, _, checks := instanceCleanupFixture(t)
	ctx := context.Background()
	p := saveInstanceCleanupPlan(t, s, v, "instance.data.remove", checks)
	j, _, e := s.Store.SubmitJob(ctx, p.ID, "delete-private")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Store.TransitionJob(ctx, j.ID, domain.Queued, domain.Running, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Store.DB.Exec(`CREATE TRIGGER fail_private_finish BEFORE UPDATE ON app_instances WHEN json_extract(OLD.data,'$.state')='data_removal_pending' AND json_extract(NEW.data,'$.state')='unprepared' BEGIN SELECT RAISE(ABORT,'injected'); END`); e != nil {
		t.Fatal(e)
	}
	result, e := s.executeInstanceCleanup(ctx, p, checks)
	if e == nil || result.State != "data_removal_pending" {
		t.Fatalf("failure evidence %+v %v", result, e)
	}
	if _, e = os.Stat(v.PrivateData); !os.IsNotExist(e) {
		t.Fatal("did not reach destructive boundary", e)
	}
	if e = s.Store.TransitionJob(ctx, j.ID, domain.Running, domain.Failed, "finish persistence failed"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Store.DB.Exec(`DROP TRIGGER fail_private_finish`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.executeInstanceCleanup(ctx, p, checks); e == nil {
		t.Fatal("blindly replayed partial deletion")
	}
	retry := saveInstanceCleanupPlan(t, s, v, "instance.data.remove", checks)
	result, e = s.executeInstanceCleanup(ctx, retry, checks)
	if e != nil || result.State != "private_data_removed" {
		t.Fatalf("reviewed retry %+v %v", result, e)
	}
	restored, e := get[domain.AppInstance](ctx, s.Store, "instance", v.ID)
	if e != nil || restored.State != "unprepared" {
		t.Fatal("lost original source state", e)
	}
}

func TestDiscardingReviewReleasesInstanceCleanupProtection(t *testing.T) {
	s, v, _, checks := instanceCleanupFixture(t)
	ctx := context.Background()
	planned := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.install", "input": map[string]string{"id": v.ID}})
	if _, e := s.planInstanceCleanup(ctx, "instance.forget", v.ID, false, false, checks); !errors.Is(e, domain.ErrReferenced) {
		t.Fatalf("unreviewed install plan not protected: %v", e)
	}
	if _, e := s.Store.DiscardPlan(ctx, planned.ID); e != nil {
		t.Fatal(e)
	}
	p := saveInstanceCleanupPlan(t, s, v, "instance.forget", checks)
	if _, e := s.executeInstanceCleanup(ctx, p, checks); e != nil {
		t.Fatal("discarded review still blocks cleanup", e)
	}
}
func TestPrivateCleanupRequiresBothExplicitConfirmations(t *testing.T) {
	s, v, _, checks := instanceCleanupFixture(t)
	for _, flags := range [][2]bool{{false, false}, {true, false}, {false, true}} {
		if _, e := s.planInstanceCleanup(context.Background(), "instance.data.remove", v.ID, flags[0], flags[1], checks); e == nil {
			t.Fatal("accepted missing confirmation", flags)
		}
	}
}
func TestPrivateCleanupDetectsLiveFileDescriptor(t *testing.T) {
	s, v, _, checks := instanceCleanupFixture(t)
	ctx := context.Background()
	tree, e := inspectCleanupTree(ctx, s.Paths.Data, v.ID, v.PrivateData)
	if e != nil {
		t.Fatal(e)
	}
	open, e := os.Open(filepath.Join(v.PrivateData, "notes.txt"))
	if e != nil {
		t.Fatal(e)
	}
	defer open.Close()
	proc := filepath.Join(checks.procRoot, "321")
	if e = os.MkdirAll(filepath.Join(proc, "fd"), 0700); e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"cwd", "root"} {
		if e = os.Symlink("/", filepath.Join(proc, field)); e != nil {
			t.Fatal(e)
		}
	}
	if e = os.Symlink(fmt.Sprintf("/proc/self/fd/%d", open.Fd()), filepath.Join(proc, "fd", "open")); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(proc, "maps"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	if e = ensureNoCleanupWriters(ctx, v.PrivateData, tree, checks.procRoot); e == nil || !strings.Contains(e.Error(), "open descriptor") {
		t.Fatalf("live descriptor not detected: %v", e)
	}
	if e = open.Close(); e != nil {
		t.Fatal(e)
	}
	if e = ensureNoCleanupWriters(ctx, v.PrivateData, tree, checks.procRoot); e != nil {
		t.Fatal("closed descriptor still treated as active", e)
	}
}
func TestNeverInitializedLinuxDataCleanupStillChecksActualAbsence(t *testing.T) {
	s, v, _, checks := instanceCleanupFixture(t)
	runner := &absenceCleanupRunner{present: true}
	s.Backend = backend.Adapter{Runner: runner}
	if _, e := s.planInstanceCleanup(context.Background(), "instance.data.remove", v.ID, true, true, checks); e == nil {
		t.Fatal("unrecorded runtime accepted because state was unprepared")
	}
	if runner.calls == 0 {
		t.Fatal("never checked actual backend")
	}
}

func TestPrivateTreeConcurrentNewEntryFailsWithoutFollowingIt(t *testing.T) {
	s, v, _, _ := instanceCleanupFixture(t)
	ctx := context.Background()
	tree, e := inspectCleanupTree(ctx, s.Paths.Data, v.ID, v.PrivateData)
	if e != nil {
		t.Fatal(e)
	}
	external := filepath.Join(t.TempDir(), "external.txt")
	if e = os.WriteFile(external, []byte("preserve"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(external, filepath.Join(v.PrivateData, "zz-new-link")); e != nil {
		t.Fatal(e)
	}
	removed, e := removeCleanupTree(ctx, v.PrivateData, tree)
	if e == nil || removed == 0 {
		t.Fatalf("expected partial removal before detecting new entry, bytes=%d err=%v", removed, e)
	}
	if data, e := os.ReadFile(external); e != nil || string(data) != "preserve" {
		t.Fatal("new external link was followed", e)
	}
	if _, e = os.Lstat(filepath.Join(v.PrivateData, "zz-new-link")); e != nil {
		t.Fatal("unreviewed entry deleted", e)
	}
}

// Both plan review and the worker must fail before touching a runtime when HOME
// already has a known cleanup blocker. The runner records any accidental call.
func TestManagementRemovalPrecheckPreservesEnvironmentAndData(t *testing.T) {
	for _, stage := range []string{"plan", "execute"} {
		t.Run(stage, func(t *testing.T) {
			s, v, _, _ := instanceCleanupFixture(t)
			ctx := context.Background()
			env, e := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
			if e != nil {
				t.Fatal(e)
			}
			env.State, env.Identity = "stopped", strings.Repeat("b", 64)
			v.State = "ready"
			if e = s.save(ctx, "environment", env.ID, env); e != nil {
				t.Fatal(e)
			}
			if e = s.save(ctx, "instance", v.ID, v); e != nil {
				t.Fatal(e)
			}
			if e = unix.Mkfifo(filepath.Join(v.PrivateData, "blocked.fifo"), 0600); e != nil {
				t.Fatal(e)
			}
			runner := &absenceCleanupRunner{}
			s.Backend = backend.Adapter{Runner: runner}
			input, _ := json.Marshal(removeInstanceInput{ID: v.ID, PrecheckPrivateData: true})
			if stage == "plan" {
				raw, _ := json.Marshal(planRequest{Operation: "instance.remove", Input: input})
				_, e = s.Handle(ctx, "plan.create", raw)
			} else {
				_, e = s.executeInstance(ctx, domain.Plan{ID: domain.NewID(), Operation: "instance.remove", InstanceID: v.ID, Input: input, RecipeRevision: v.RecipeRevision, ArtifactDigests: []string{v.OriginalArtifact}})
			}
			if e == nil || !strings.Contains(e.Error(), "尚未删除") {
				t.Fatalf("precheck result: %v", e)
			}
			if runner.calls != 0 {
				t.Fatal("precheck called runtime backend")
			}
			current, e := get[domain.Environment](ctx, s.Store, "environment", env.ID)
			if e != nil || current.State != "stopped" || current.Identity != env.Identity {
				t.Fatal("environment changed", e)
			}
			if b, e := os.ReadFile(filepath.Join(v.PrivateData, "notes.txt")); e != nil || string(b) != "personal notes" {
				t.Fatal("private data changed", e)
			}
			plans, e := s.Store.List(ctx, "plan")
			if e != nil || len(plans) != 0 {
				t.Fatal("failed precheck retained a deletion plan", e)
			}
		})
	}
}
