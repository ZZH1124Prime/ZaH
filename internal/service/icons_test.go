package service

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ZZH1124Prime/ZaH/internal/catalog"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestReleaseIconCopiesProtectsAndMigrates(t *testing.T) {
	s, v, _, _ := instanceCleanupFixture(t)
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "icon.png")
	im := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	im.Set(4, 5, color.NRGBA{R: 250, A: 255})
	var b bytes.Buffer
	png.Encode(&b, im)
	if e := os.WriteFile(p, b.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	rel := rpcValue[domain.AppRelease](t, s, "release.icon.set", map[string]string{"id": v.ReleaseID, "path": p})
	if rel.IconSHA256 == "" {
		t.Fatal("no icon")
	}
	os.Remove(p)
	url := rpcValue[string](t, s, "release.icon.get", map[string]string{"id": rel.ID})
	if len(url) < 30 {
		t.Fatal("no copied icon")
	}
	if _, e := s.Handle(ctx, "plan.create", json.RawMessage(`{"operation":"artifact.remove","input":{"id":"`+rel.IconSHA256+`"}}`)); e == nil {
		t.Fatal("referenced icon removable")
	}
	dst := filepath.Join(t.TempDir(), "icons.zahbundle")
	if _, e := s.ExportCatalogBundle(ctx, dst); e != nil {
		t.Fatal(e)
	}
	other := catalogService(t)
	result, e := other.RestoreCatalogBundle(ctx, dst)
	if e != nil {
		t.Fatal(e)
	}
	id := result.IDMap["release:"+rel.ID]
	if id == "" {
		t.Fatalf("%+v", result.IDMap)
	}
	got := rpcValue[string](t, other, "release.icon.get", map[string]string{"id": id})
	if got != url {
		t.Fatal("icon lost in migration")
	}
}

func iconFixturePNG(t *testing.T, red uint8) (string, []byte) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "icon.png")
	im := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	im.Set(4, 5, color.NRGBA{R: red, A: 255})
	var out bytes.Buffer
	if err := png.Encode(&out, im); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return p, out.Bytes()
}
func TestReleaseIconReplacementReleasesOnlyOldIcon(t *testing.T) {
	s, v, _, _ := instanceCleanupFixture(t)
	ctx := context.Background()
	a, _ := iconFixturePNG(t, 20)
	b, _ := iconFixturePNG(t, 21)
	old := rpcValue[domain.AppRelease](t, s, "release.icon.set", map[string]string{"id": v.ReleaseID, "path": a})
	latest := rpcValue[domain.AppRelease](t, s, "release.icon.set", map[string]string{"id": v.ReleaseID, "path": b})
	if old.IconSHA256 == latest.IconSHA256 {
		t.Fatal("fixture icons equal")
	}
	refs, err := s.Store.References(ctx, "artifact", old.IconSHA256)
	if err != nil || len(refs) != 0 {
		t.Fatalf("old icon reference leaked: %+v %v", refs, err)
	}
	plan := cleanupPlan(t, s, old.IconSHA256)
	if _, err = s.ExecuteCleanup(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if err = s.Store.Delete(ctx, "artifact", v.OriginalArtifact); err == nil {
		t.Fatal("material dependency lost")
	}
	restored := rpcValue[domain.AppRelease](t, s, "release.icon.set", map[string]string{"id": v.ReleaseID, "path": a})
	if restored.IconSHA256 != old.IconSHA256 {
		t.Fatal("icon digest changed")
	}
	material, err := get[catalog.Artifact](ctx, s.Store, "artifact", old.IconSHA256)
	if err != nil || material.Availability != "available" || material.Source.OriginalName != "version-icon.png" || material.Source.OriginalPath != "" {
		t.Fatalf("icon tombstone not restored correctly: %+v %v", material, err)
	}
	if _, err = s.ExportCatalogBundle(ctx, filepath.Join(t.TempDir(), "icons-restored.zahbundle")); err != nil {
		t.Fatal(err)
	}
}
func TestIconRecoveryDatabaseFailureRemovesOnlyNewBytes(t *testing.T) {
	s := catalogService(t)
	ctx := context.Background()
	_, png := iconFixturePNG(t, 40)
	id, err := s.storeIcon(ctx, png)
	if err != nil {
		t.Fatal(err)
	}
	p := cleanupPlan(t, s, id)
	if _, err = s.ExecuteCleanup(ctx, p); err != nil {
		t.Fatal(err)
	}
	before, err := s.Store.Get(ctx, "artifact", id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec(`CREATE TRIGGER reject_icon_recovery BEFORE UPDATE ON artifacts BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.storeIcon(ctx, png); err == nil {
		t.Fatal("database failure ignored")
	}
	after, err := s.Store.Get(ctx, "artifact", id)
	if err != nil || !bytes.Equal(before.Data, after.Data) {
		t.Fatal("failed recovery changed original metadata", err)
	}
	path, err := s.Artifacts().Path(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("failed recovery leaked newly published bytes", err)
	}
}
