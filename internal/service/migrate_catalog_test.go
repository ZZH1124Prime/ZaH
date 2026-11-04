package service

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/catalog"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/infra"
	"github.com/ZZH1124Prime/ZaH/internal/migration"
	"github.com/ZZH1124Prime/ZaH/internal/recipe"
)

func catalogService(t *testing.T) *Service {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	s, e := Open(infra.Paths{Data: filepath.Join(root, "data"), State: filepath.Join(root, "state"), Config: filepath.Join(root, "config"), Cache: filepath.Join(root, "cache"), Runtime: filepath.Join(root, "runtime")})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestCatalogMaterialsRoundtripRequiresReinstall(t *testing.T) {
	ctx := context.Background()
	src := catalogService(t)
	installer := filepath.Join(t.TempDir(), "安装材料.bin")
	if e := os.WriteFile(installer, []byte("independent archival bytes"), 0600); e != nil {
		t.Fatal(e)
	}
	a, e := src.Artifacts().Import(ctx, installer)
	if e != nil {
		t.Fatal(e)
	}
	if e = src.save(ctx, "artifact", a.SHA256, a); e != nil {
		t.Fatal(e)
	}
	r := recipe.Recipe{SchemaVersion: 1, ID: "app", Revision: "1", Platform: "linux", Architecture: "amd64", Strategy: "appimage", Image: "docker.io/library/ubuntu@sha256:" + strings.Repeat("a", 64), Entry: "squashfs-root/AppRun"}
	rid := r.Digest()
	values := []domain.Record{record("recipe", rid, r), record("family", "old-family", domain.AppFamily{ID: "old-family", Name: "应用", DefaultInstanceID: "old-instance"}), record("release", "old-release", domain.AppRelease{ID: "old-release", FamilyID: "old-family", Platform: "linux", Arch: "amd64", Artifacts: []string{a.SHA256}}), record("environment", "old-env", domain.Environment{ID: "old-env", Name: "zah-old-instance", Backend: "distrobox-docker", State: "ready", Identity: "old-container", Ownership: "managed", Registration: json.RawMessage(`{"verified":"old-host"}`)}), record("instance", "old-instance", domain.AppInstance{ID: "old-instance", ReleaseID: "old-release", EnvironmentID: "old-env", RecipeRevision: rid, OriginalArtifact: a.SHA256, Name: "日常", State: "ready", PrivateData: "/old/home/private", Entry: []string{"/old/entry"}}), record("binding", "old-binding", domain.FileBinding{ID: "old-binding", InstanceID: "old-instance", HostPath: "/old/music", TargetPath: "/mnt/zah/music", Access: "read", State: "verified", Device: 42, Inode: 3}), record("capability", "old-cap", domain.CapabilityResult{ID: "old-cap", InstanceID: "old-instance", Capability: "sound", State: "passed", Evidence: "source host only"})}
	for _, v := range values {
		if e = src.Store.Put(ctx, v); e != nil {
			t.Fatal(e)
		}
	}
	archive := filepath.Join(t.TempDir(), "catalog.zahbundle")
	inspection, e := src.ExportCatalogBundle(ctx, archive)
	if e != nil {
		t.Fatal(e)
	}
	if inspection.Manifest.OfflineReady || len(inspection.Manifest.Omissions) == 0 {
		t.Fatal("claimed complete/offline backup")
	}
	dst := catalogService(t)
	result, e := dst.RestoreCatalogBundle(ctx, archive)
	if e != nil {
		t.Fatal(e)
	}
	if result.IDMap["instance:old-instance"] == "old-instance" || result.State != "needs_reinstall_and_rebind" {
		t.Fatalf("bad remap %+v", result)
	}
	instance, e := get[domain.AppInstance](ctx, dst.Store, "instance", result.IDMap["instance:old-instance"])
	if e != nil {
		t.Fatal(e)
	}
	if instance.State != "unprepared" || len(instance.Entry) != 0 || !strings.HasPrefix(instance.PrivateData, dst.Paths.Data) {
		t.Fatalf("unsafe restored instance %+v", instance)
	}
	binding, e := get[domain.FileBinding](ctx, dst.Store, "binding", result.IDMap["binding:old-binding"])
	if e != nil {
		t.Fatal(e)
	}
	if binding.HostPath != "" || binding.Device != 0 || binding.State != "needs_binding" {
		t.Fatalf("unsafe binding %+v", binding)
	}
	cap, e := get[domain.CapabilityResult](ctx, dst.Store, "capability", result.IDMap["capability:old-cap"])
	if e != nil || cap.State != "historical_unverified" {
		t.Fatalf("old verification trusted %+v %v", cap, e)
	}
	env, e := get[domain.Environment](ctx, dst.Store, "environment", instance.EnvironmentID)
	if e != nil || env.Identity != "" || len(env.Registration) != 0 || env.Name != "zah-"+instance.ID {
		t.Fatalf("old runtime reused %+v %v", env, e)
	}
	os.Remove(installer)
	if _, e = dst.Artifacts().Verify(ctx, a.SHA256); e != nil {
		t.Fatal(e)
	}
	second, e := dst.RestoreCatalogBundle(ctx, archive)
	if e != nil {
		t.Fatal(e)
	}
	if second.IDMap["instance:old-instance"] == instance.ID {
		t.Fatal("second import overwrote instance")
	}
	artifacts, e := dst.Store.List(ctx, "artifact")
	if e != nil || len(artifacts) != 1 {
		t.Fatalf("digest dedup %d %v", len(artifacts), e)
	}
	refs, e := dst.Store.References(ctx, "artifact", a.SHA256)
	if e != nil || len(refs) < 4 {
		b, _ := json.Marshal(refs)
		t.Fatalf("missing material references %s %v", b, e)
	}
}

func TestCatalogRestoreFailureLeavesExistingState(t *testing.T) {
	ctx := context.Background()
	src := catalogService(t)
	source := filepath.Join(t.TempDir(), "new-material")
	if e := os.WriteFile(source, []byte("new bytes"), 0600); e != nil {
		t.Fatal(e)
	}
	imported, e := src.Artifacts().Import(ctx, source)
	if e != nil {
		t.Fatal(e)
	}
	if e = src.save(ctx, "artifact", imported.SHA256, imported); e != nil {
		t.Fatal(e)
	}
	if e = src.save(ctx, "family", "family", domain.AppFamily{ID: "family", Name: "family"}); e != nil {
		t.Fatal(e)
	}
	archive := filepath.Join(t.TempDir(), "catalog.zahbundle")
	if _, e = src.ExportCatalogBundle(ctx, archive); e != nil {
		t.Fatal(e)
	}
	dst := catalogService(t)
	existingSource := filepath.Join(t.TempDir(), "existing")
	if e = os.WriteFile(existingSource, []byte("existing independent bytes"), 0600); e != nil {
		t.Fatal(e)
	}
	existing, e := dst.Artifacts().Import(ctx, existingSource)
	if e != nil {
		t.Fatal(e)
	}
	if e = dst.save(ctx, "artifact", existing.SHA256, existing); e != nil {
		t.Fatal(e)
	}
	if _, e = dst.Store.DB.Exec(`CREATE TRIGGER reject_import BEFORE INSERT ON records WHEN NEW.kind='family' BEGIN SELECT RAISE(ABORT,'injected failure'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e = dst.RestoreCatalogBundle(ctx, archive); e == nil {
		t.Fatal("injected database failure ignored")
	}
	p, e := dst.Artifacts().Path(imported.SHA256)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Lstat(p); !os.IsNotExist(e) {
		t.Fatalf("new material leaked after failed import: %v", e)
	}
	if _, e = dst.Artifacts().Verify(ctx, existing.SHA256); e != nil {
		t.Fatal("existing material changed", e)
	}
	rows, e := dst.Store.List(ctx, "artifact")
	if e != nil || len(rows) != 1 || rows[0].ID != existing.SHA256 {
		t.Fatalf("existing catalog changed: %+v %v", rows, e)
	}
	families, e := dst.Store.List(ctx, "family")
	if e != nil || len(families) != 0 {
		t.Fatal("partial catalog imported", e)
	}
}

func TestBackendRestorePreparationRollsBackBeforeRegistration(t *testing.T) {
	ctx := context.Background()
	s := catalogService(t)
	format := "zah.test-environment"
	snapshot := catalogSnapshot{Format: format, SchemaVersion: 1, Records: []domain.Record{record("family", "f", domain.AppFamily{ID: "f", Name: "family"})}}
	raw, e := json.Marshal(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	archive := filepath.Join(t.TempDir(), "environment.zahbundle")
	if e = migration.Export(ctx, archive, migration.Snapshot{Catalog: raw, Bindings: json.RawMessage(`{}`)}, nil, migration.Limits{}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RestoreCatalogBundle(ctx, archive); e == nil {
		t.Fatal("catalog wrapper accepted foreign format")
	}
	prepared := filepath.Join(s.Paths.Data, "prepared-only")
	callback := func(stage string, inspection migration.Inspection, records []domain.Record, idMap map[string]string) (func(), error) {
		if len(records) != 1 || idMap["family:f"] == "f" {
			t.Fatal("callback ran before remapping")
		}
		if _, e := os.Stat(filepath.Join(stage, "catalog.json")); e != nil {
			t.Fatal(e)
		}
		if e := os.Mkdir(prepared, 0700); e != nil {
			return nil, e
		}
		return func() { _ = os.Remove(prepared) }, nil
	}
	if _, e = s.Store.DB.Exec(`CREATE TRIGGER reject_prepared BEFORE INSERT ON records BEGIN SELECT RAISE(ABORT,'injected'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.restoreBundle(ctx, archive, format, callback); e == nil {
		t.Fatal("registration failure ignored")
	}
	if _, e = os.Stat(prepared); !os.IsNotExist(e) {
		t.Fatal("prepared data leaked", e)
	}
	rows, e := s.Store.List(ctx, "family")
	if e != nil || len(rows) != 0 {
		t.Fatal("partial registration", e)
	}
	if _, e = s.Store.DB.Exec(`DROP TRIGGER reject_prepared`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.restoreBundle(ctx, archive, format, callback); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(prepared); e != nil {
		t.Fatal("committed prepared data removed", e)
	}
}

func TestCatalogRestoreRevivesRemovedMaterialAtomically(t *testing.T) {
	for _, state := range []string{"removed", "removal_pending"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			s, material, _ := cleanupFixture(t)
			if err := s.save(ctx, "family", "old-family", domain.AppFamily{ID: "old-family", Name: "family"}); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(t.TempDir(), "before-removal.zahbundle")
			if _, err := s.ExportCatalogBundle(ctx, archive); err != nil {
				t.Fatal(err)
			}
			plan := cleanupPlan(t, s, material.SHA256)
			if _, err := s.ExecuteCleanup(ctx, plan); err != nil {
				t.Fatal(err)
			}
			tombstone, err := get[catalog.Artifact](ctx, s.Store, "artifact", material.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			tombstone.Availability = state
			if err = s.Store.Put(ctx, record("artifact", material.SHA256, tombstone)); err != nil {
				t.Fatal(err)
			}
			before, err := s.Store.Get(ctx, "artifact", material.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Store.DB.Exec(`CREATE TRIGGER reject_recovered_import BEFORE INSERT ON records WHEN NEW.kind='family' BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err = s.RestoreCatalogBundle(ctx, archive); err == nil {
				t.Fatal("failed import committed")
			}
			after, err := s.Store.Get(ctx, "artifact", material.SHA256)
			if err != nil || !bytes.Equal(before.Data, after.Data) {
				t.Fatalf("tombstone changed despite failed import: %s %v", after.Data, err)
			}
			if _, err = os.Lstat(material.Path); !os.IsNotExist(err) {
				t.Fatal("failed bundle restore leaked recovered bytes", err)
			}
			families, err := s.Store.List(ctx, "family")
			if err != nil || len(families) != 1 {
				t.Fatal("partial catalog published", err)
			}
			if _, err = s.Store.DB.Exec(`DROP TRIGGER reject_recovered_import`); err != nil {
				t.Fatal(err)
			}
			if _, err = s.RestoreCatalogBundle(ctx, archive); err != nil {
				t.Fatal(err)
			}
			recovered, err := get[catalog.Artifact](ctx, s.Store, "artifact", material.SHA256)
			if err != nil || recovered.Availability != "available" || recovered.Source != material.Source || recovered.Path != material.Path || recovered.Size != material.Size {
				t.Fatalf("recovery overwrote original facts: %+v %v", recovered, err)
			}
			if _, err = s.Artifacts().Verify(ctx, material.SHA256); err != nil {
				t.Fatal(err)
			}
			if _, err = s.ExportCatalogBundle(ctx, filepath.Join(t.TempDir(), "recovered.zahbundle")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCatalogRecoveryNeverOverwritesCorruptExistingObject(t *testing.T) {
	ctx := context.Background()
	s, material, _ := cleanupFixture(t)
	archive := filepath.Join(t.TempDir(), "good.zahbundle")
	if _, err := s.ExportCatalogBundle(ctx, archive); err != nil {
		t.Fatal(err)
	}
	tombstone := material
	tombstone.Availability = "removed"
	if err := s.Store.Put(ctx, record("artifact", material.SHA256, tombstone)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(material.Path, 0600); err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("existing untrusted modified object")
	if err := os.WriteFile(material.Path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreCatalogBundle(ctx, archive); err == nil {
		t.Fatal("corrupt existing object silently replaced")
	}
	actual, err := os.ReadFile(material.Path)
	if err != nil || !bytes.Equal(actual, corrupt) {
		t.Fatal("pre-existing object changed", err)
	}
	after, err := get[catalog.Artifact](ctx, s.Store, "artifact", material.SHA256)
	if err != nil || after.Availability != "removed" {
		t.Fatal("unverified bytes marked available", err)
	}
}
