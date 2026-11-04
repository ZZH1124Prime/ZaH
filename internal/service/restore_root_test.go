package service

import (
	"context"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectedRestoreRootIdentityAndCleanupBoundary(t *testing.T) {
	ctx := context.Background()
	src, v := fullSource(t)
	archive := filepath.Join(t.TempDir(), "copy.zahbundle")
	if _, e := src.ExportEnvironmentBundle(ctx, EnvironmentBundleInput{ID: v.ID, Path: archive, ColdConfirmed: true}); e != nil {
		t.Fatal(e)
	}
	dst := catalogService(t)
	parent := t.TempDir()
	os.Chmod(parent, 0755)
	sentinel := filepath.Join(parent, "keep")
	os.WriteFile(sentinel, []byte("unrelated"), 0600)
	proof, e := inspectRestoreParent(parent)
	if e != nil {
		t.Fatal(e)
	}
	result, e := dst.RestoreEnvironmentBundle(ctx, archive, RestoreOptions{Merge: true, Directory: parent, Parent: proof})
	if e != nil {
		t.Fatal(e)
	}
	inst, e := get[domain.AppInstance](ctx, dst.Store, "instance", result.IDMap["instance:"+v.ID])
	if e != nil {
		t.Fatal(e)
	}
	if inst.RestoreRoot == nil || !strings.HasPrefix(inst.PrivateData, parent+"/zah-import-") {
		t.Fatal("selected root ignored")
	}
	if _, e = dst.inspectInstanceCleanupTree(ctx, inst); e != nil {
		t.Fatal(e)
	}
	helper, e := dst.supervisorCopy(ctx, inst)
	if e != nil || filepath.Dir(helper) != filepath.Join(inst.PrivateData, ".zah-runtime") {
		t.Fatal("restored instance cannot prepare its verified supervisor", helper, e)
	}
	if b, e := os.ReadFile(sentinel); e != nil || string(b) != "unrelated" {
		t.Fatal("parent data changed")
	}
	root := inst.RestoreRoot.Path
	if e = os.Rename(root, root+"-old"); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(root, 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = dst.inspectInstanceCleanupTree(ctx, inst); e == nil {
		t.Fatal("replacement root accepted for deletion")
	}
	if _, _, e = dst.request(ctx, inst); e == nil {
		t.Fatal("replacement root accepted for running")
	}
	if _, e = dst.supervisorCopy(ctx, inst); e == nil {
		t.Fatal("replacement root accepted for supervisor publication")
	}
}
func TestRestoreParentChangedRejectsBeforePublication(t *testing.T) {
	ctx := context.Background()
	src, v := fullSource(t)
	archive := filepath.Join(t.TempDir(), "copy.zahbundle")
	if _, e := src.ExportEnvironmentBundle(ctx, EnvironmentBundleInput{ID: v.ID, Path: archive, ColdConfirmed: true}); e != nil {
		t.Fatal(e)
	}
	dst := catalogService(t)
	parent := t.TempDir()
	os.Chmod(parent, 0700)
	proof, _ := inspectRestoreParent(parent)
	wrong := *proof
	wrong.Inode++
	if _, e := dst.RestoreEnvironmentBundle(ctx, archive, RestoreOptions{Merge: true, Directory: parent, Parent: &wrong}); e == nil {
		t.Fatal("changed selection accepted")
	}
	rows, _ := dst.Store.List(ctx, "instance")
	children, _ := os.ReadDir(parent)
	if len(rows) != 0 || len(children) != 0 {
		t.Fatal("failure published records or data")
	}
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(parent, link)
	if _, e := inspectRestoreParent(link); e == nil {
		t.Fatal("symlink parent accepted")
	}
}
