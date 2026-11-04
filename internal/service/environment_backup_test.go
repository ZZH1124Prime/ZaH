package service

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/process"
	"github.com/ZZH1124Prime/ZaH/internal/recipe"
)

type fullBundleRunner struct {
	instance, imageID string
	image             []byte
	calls             int
}

func (f *fullBundleRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.calls++
	args := strings.Join(c.Args, "|")
	switch {
	case strings.HasPrefix(args, "container|inspect|"):
		return process.Result{Stdout: fmt.Sprintf(`[{"Id":%q,"Name":%q,"Config":{"Labels":{"org.zah.instance":%q}},"State":{"Running":false,"Status":"exited","Pid":0,"StartedAt":"a","FinishedAt":"b"}}]`, strings.Repeat("b", 64), "/zah-"+f.instance, f.instance)}, nil
	case strings.HasPrefix(args, "container|commit|"):
		return process.Result{Stdout: f.imageID}, nil
	case strings.HasPrefix(args, "image|save|--output|"):
		return process.Result{}, os.WriteFile(c.Args[3], f.image, 0600)
	}
	return process.Result{}, fmt.Errorf("unexpected fake command: %s", args)
}
func bundleImage(t *testing.T) ([]byte, string) {
	t.Helper()
	var layer bytes.Buffer
	w := tar.NewWriter(&layer)
	_ = w.WriteHeader(&tar.Header{Name: "etc/example", Mode: 0600, Size: 3})
	_, _ = w.Write([]byte("abc"))
	_ = w.Close()
	lh := sha256.Sum256(layer.Bytes())
	cfg, _ := json.Marshal(map[string]any{"architecture": "amd64", "os": "linux", "rootfs": map[string]any{"type": "layers", "diff_ids": []string{"sha256:" + hex.EncodeToString(lh[:])}}})
	ch := sha256.Sum256(cfg)
	configName := hex.EncodeToString(ch[:]) + ".json"
	manifest, _ := json.Marshal([]map[string]any{{"Config": configName, "RepoTags": []string{}, "Layers": []string{"layer/layer.tar"}}})
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	for _, entry := range []struct {
		n string
		b []byte
	}{{configName, cfg}, {"manifest.json", manifest}, {"layer/layer.tar", layer.Bytes()}} {
		_ = tw.WriteHeader(&tar.Header{Name: entry.n, Mode: 0600, Size: int64(len(entry.b))})
		_, _ = tw.Write(entry.b)
	}
	_ = tw.Close()
	return archive.Bytes(), "sha256:" + hex.EncodeToString(ch[:])
}
func fullSource(t *testing.T) (*Service, domain.AppInstance) {
	t.Helper()
	s := catalogService(t)
	ctx := context.Background()
	material := filepath.Join(t.TempDir(), "material")
	if e := os.WriteFile(material, []byte("installer"), 0600); e != nil {
		t.Fatal(e)
	}
	a, e := s.Artifacts().Import(ctx, material)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.save(ctx, "artifact", a.SHA256, a); e != nil {
		t.Fatal(e)
	}
	r := recipe.Recipe{SchemaVersion: 1, ID: "app", Revision: "1", Platform: "linux", Architecture: "amd64", Strategy: "appimage", Image: "docker.io/library/ubuntu@sha256:" + strings.Repeat("a", 64), Entry: "squashfs-root/AppRun"}
	id := "source-instance"
	v := domain.AppInstance{ID: id, Name: "应用", ReleaseID: "release", EnvironmentID: "environment", RecipeRevision: r.Digest(), OriginalArtifact: a.SHA256, State: "ready", PrivateData: filepath.Join(s.Paths.Data, "instances", id, "home")}
	if e = os.MkdirAll(filepath.Join(v.PrivateData, "empty"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(v.PrivateData, "笔记.txt"), []byte("persisted notes"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink("笔记.txt", filepath.Join(v.PrivateData, "notes-link")); e != nil {
		t.Fatal(e)
	}
	records := []domain.Record{record("family", "family", domain.AppFamily{ID: "family", Name: "应用", DefaultInstanceID: id}), record("family", "unselected", domain.AppFamily{ID: "unselected", Name: "不应打包"}), record("recipe", r.Digest(), r), record("release", "release", domain.AppRelease{ID: "release", FamilyID: "family", Platform: "linux", Arch: "amd64", Artifacts: []string{a.SHA256}}), record("environment", "environment", domain.Environment{ID: "environment", Backend: "distrobox-docker", Name: "zah-" + id, Ownership: "managed", Identity: strings.Repeat("b", 64), State: "stopped"}), record("instance", id, v)}
	for _, rec := range records {
		if e = s.Store.Put(ctx, rec); e != nil {
			t.Fatal(e)
		}
	}
	image, imageID := bundleImage(t)
	s.Backend = backend.Adapter{Runner: &fullBundleRunner{instance: id, imageID: imageID, image: image}}
	return s, v
}
func TestEnvironmentBundleRestoresDataNewIdentityAndPendingRuntime(t *testing.T) {
	ctx := context.Background()
	src, old := fullSource(t)
	archive := filepath.Join(t.TempDir(), "instance.zahbundle")
	exported, e := src.ExportEnvironmentBundle(ctx, EnvironmentBundleInput{ID: old.ID, Path: archive, ColdConfirmed: true})
	if e != nil {
		t.Fatal(e)
	}
	if exported.State != "instance_snapshot_packaged_needs_target_restore" || exported.Inspection.Manifest.OfflineReady {
		t.Fatalf("%+v", exported)
	}
	if e = os.Remove(filepath.Join(old.PrivateData, "笔记.txt")); e != nil {
		t.Fatal(e)
	}
	dst := catalogService(t)
	result, e := dst.RestoreEnvironmentBundle(ctx, archive)
	if e != nil {
		t.Fatal(e)
	}
	if result.State != "needs_runtime_restore_and_rebind" {
		t.Fatal(result.State)
	}
	v, e := get[domain.AppInstance](ctx, dst.Store, "instance", result.IDMap["instance:"+old.ID])
	if e != nil {
		t.Fatal(e)
	}
	if v.ID == old.ID || v.State != "needs_runtime_restore" || len(v.Entry) != 0 {
		t.Fatalf("%+v", v)
	}
	notes, e := os.ReadFile(filepath.Join(v.PrivateData, "notes-link"))
	if e != nil || string(notes) != "persisted notes" {
		t.Fatalf("%q %v", notes, e)
	}
	if _, e = os.Stat(filepath.Join(v.PrivateData, "empty")); e != nil {
		t.Fatal(e)
	}
	env, e := get[domain.Environment](ctx, dst.Store, "environment", v.EnvironmentID)
	if e != nil {
		t.Fatal(e)
	}
	var pending PendingEnvironmentRestore
	if e = json.Unmarshal(env.PendingRestore, &pending); e != nil {
		t.Fatal(e)
	}
	if env.Identity != "" || len(env.Registration) > 0 || pending.ImageID == "" || !strings.HasPrefix(pending.ArchivePath, dst.Paths.Data) {
		t.Fatalf("%+v %+v", env, pending)
	}
	if _, e = backend.InspectDockerArchive(ctx, pending.ArchivePath, backend.MaxSnapshotBytes); e != nil {
		t.Fatal(e)
	}
	families, e := dst.Store.List(ctx, "family")
	if e != nil || len(families) != 1 {
		t.Fatalf("selection leaked %d %v", len(families), e)
	}
	second, e := dst.RestoreEnvironmentBundle(ctx, archive)
	if e != nil {
		t.Fatal(e)
	}
	if second.IDMap["instance:"+old.ID] == v.ID {
		t.Fatal("second import overwrote existing instance")
	}
}
func TestEnvironmentRestoreDatabaseFailureRollsBackNewTree(t *testing.T) {
	ctx := context.Background()
	src, old := fullSource(t)
	archive := filepath.Join(t.TempDir(), "instance.zahbundle")
	if _, e := src.ExportEnvironmentBundle(ctx, EnvironmentBundleInput{ID: old.ID, Path: archive, ColdConfirmed: true}); e != nil {
		t.Fatal(e)
	}
	dst := catalogService(t)
	if _, e := dst.Store.DB.Exec(`CREATE TRIGGER reject_environment BEFORE INSERT ON records WHEN NEW.kind='environment' BEGIN SELECT RAISE(ABORT,'injected'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e := dst.RestoreEnvironmentBundle(ctx, archive); e == nil {
		t.Fatal("ignored failed atomic commit")
	}
	dirs, e := os.ReadDir(filepath.Join(dst.Paths.Data, "instances"))
	if e != nil || len(dirs) != 0 {
		t.Fatalf("new private data leaked: %v %v", dirs, e)
	}
	rows, e := dst.Store.List(ctx, "instance")
	if e != nil || len(rows) != 0 {
		t.Fatal("partial instance registration", e)
	}
}
func TestPrivateTreeRejectsExternalLinksAndMaliciousRestore(t *testing.T) {
	s, old := fullSource(t)
	external := filepath.Join(t.TempDir(), "external")
	if e := os.WriteFile(external, []byte("outside"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(external, filepath.Join(old.PrivateData, "outside-link")); e != nil {
		t.Fatal(e)
	}
	if _, e := collectPrivateTree(context.Background(), old.PrivateData); e == nil || !strings.Contains(e.Error(), "outside-link") {
		t.Fatalf("accepted external link: %v", e)
	}
	_ = s
	for _, tree := range []privateTree{{SchemaVersion: 1, Entries: []privateTreeEntry{{Path: ".", Kind: "directory", Mode: 0700}, {Path: "../escape", Kind: "file"}}}, {SchemaVersion: 1, Entries: []privateTreeEntry{{Path: ".", Kind: "directory", Mode: 0700}, {Path: "link", Kind: "symlink", Target: "../../etc"}}}} {
		if e := restorePrivateTree(context.Background(), t.TempDir(), "id", filepath.Join(t.TempDir(), "home"), tree, nil, map[string]bool{}); e == nil {
			t.Fatal("accepted malicious tree")
		}
	}
}

type workbenchRunner struct {
	instances map[string]string
	image     []byte
	imageID   string
}

func (f *workbenchRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	args := strings.Join(c.Args, "|")
	switch {
	case strings.HasPrefix(args, "container|inspect|"):
		id := c.Args[2]
		inst, ok := f.instances[id]
		if !ok {
			return process.Result{}, fmt.Errorf("unknown resource")
		}
		return process.Result{Stdout: fmt.Sprintf(`[{"Id":%q,"Name":%q,"Config":{"Labels":{"org.zah.instance":%q}},"State":{"Running":false,"Status":"exited","Pid":0,"StartedAt":"a","FinishedAt":"b"}}]`, id, "/zah-"+inst, inst)}, nil
	case strings.HasPrefix(args, "container|commit|"):
		return process.Result{Stdout: f.imageID}, nil
	case strings.HasPrefix(args, "image|save|--output|"):
		return process.Result{}, os.WriteFile(c.Args[3], f.image, 0600)
	}
	return process.Result{}, fmt.Errorf("unexpected command")
}
func TestWorkbenchBundleDeduplicatesAndRestoresAllAtomically(t *testing.T) {
	ctx := context.Background()
	s, first := fullSource(t)
	second := first
	second.ID = "second-instance"
	second.EnvironmentID = "second-environment"
	second.PrivateData = filepath.Join(s.Paths.Data, "instances", second.ID, "home")
	if e := os.MkdirAll(second.PrivateData, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(second.PrivateData, "second.txt"), []byte("second private data"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := s.Store.Put(ctx, record("instance", second.ID, second)); e != nil {
		t.Fatal(e)
	}
	env := domain.Environment{ID: second.EnvironmentID, Name: "zah-" + second.ID, Backend: "distrobox-docker", Ownership: "managed", Identity: strings.Repeat("c", 64), State: "stopped"}
	if e := s.Store.Put(ctx, record("environment", env.ID, env)); e != nil {
		t.Fatal(e)
	}
	image, id := bundleImage(t)
	s.Backend = backend.Adapter{Runner: &workbenchRunner{instances: map[string]string{strings.Repeat("b", 64): first.ID, strings.Repeat("c", 64): second.ID}, image: image, imageID: id}}
	orphanSource := filepath.Join(t.TempDir(), "unreferenced-material")
	if e := os.WriteFile(orphanSource, []byte("retained unreferenced material"), 0600); e != nil {
		t.Fatal(e)
	}
	orphan, e := s.Artifacts().Import(ctx, orphanSource)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.save(ctx, "artifact", orphan.SHA256, orphan); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ExportWorkbenchBundle(ctx, WorkbenchBundleInput{Path: filepath.Join(t.TempDir(), "partial.zahbundle"), ColdConfirmed: true, IncludeCatalog: true, Instances: []WorkbenchInstanceInput{{ID: first.ID}}}); e == nil {
		t.Fatal("claimed whole catalog with partial instance selection")
	}
	archive := filepath.Join(t.TempDir(), "workbench.zahbundle")
	exported, e := s.ExportWorkbenchBundle(ctx, WorkbenchBundleInput{Path: archive, ColdConfirmed: true})
	if e != nil {
		t.Fatal(e)
	}
	if len(exported.Instances) != 2 {
		t.Fatalf("%+v", exported)
	}
	materials := 0
	for name := range exported.Inspection.Checksums {
		if strings.HasPrefix(name, "objects/sha256/") {
			materials++
		}
	}
	if materials != 2 {
		t.Fatalf("shared material dedup or unreferenced material inclusion failed %d", materials)
	}
	target := catalogService(t)
	restored, e := target.RestoreEnvironmentBundle(ctx, archive)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = target.Artifacts().Verify(ctx, orphan.SHA256); e != nil {
		t.Fatal("unreferenced material lost", e)
	}
	families, e := target.Store.List(ctx, "family")
	if e != nil || len(families) != 2 {
		t.Fatalf("unreferenced catalog records lost %d %v", len(families), e)
	}
	instances, e := target.Store.List(ctx, "instance")
	if e != nil || len(instances) != 2 {
		t.Fatalf("%v %v", instances, e)
	}
	v, e := get[domain.AppInstance](ctx, target.Store, "instance", restored.IDMap["instance:"+second.ID])
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(v.PrivateData, "second.txt"))
	if e != nil || string(b) != "second private data" {
		t.Fatalf("%q %v", b, e)
	}
	failed := catalogService(t)
	if _, e = failed.Store.DB.Exec(`CREATE TRIGGER reject_second BEFORE INSERT ON records WHEN NEW.kind='instance' AND (SELECT count(*) FROM records WHERE kind='instance')>0 BEGIN SELECT RAISE(ABORT,'second instance failure'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e = failed.RestoreEnvironmentBundle(ctx, archive); e == nil {
		t.Fatal("ignored atomic failure")
	}
	rows, e := failed.Store.List(ctx, "instance")
	if e != nil || len(rows) != 0 {
		t.Fatal("partially imported workbench", e)
	}
	dirs, e := os.ReadDir(filepath.Join(failed.Paths.Data, "instances"))
	if e != nil || len(dirs) != 0 {
		t.Fatalf("partial private trees retained %v %v", dirs, e)
	}
}
