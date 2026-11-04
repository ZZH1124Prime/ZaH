package backend

import (
	"context"
	"github.com/ZZH1124Prime/ZaH/internal/host"
	"os"
	"path/filepath"
	"testing"
)

func TestOwnedBottleRemovalKeepsOtherBottlesAndSymlinkTargets(t *testing.T) {
	root := filepath.Join(t.TempDir(), "bottles")
	target := filepath.Join(root, "zah-test")
	other := filepath.Join(root, "unrelated")
	outside := filepath.Join(t.TempDir(), "keep")
	for _, p := range []string{target, other} {
		if e := os.MkdirAll(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(outside, []byte("private outside data"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(filepath.Dir(outside), filepath.Join(target, "z:")); e != nil {
		t.Fatal(e)
	}
	b, e := host.InspectBinding(target, false)
	if e != nil {
		t.Fatal(e)
	}
	if e = removeOwnedBottleTree(context.Background(), b, "zah-test"); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Lstat(target); !os.IsNotExist(e) {
		t.Fatal("target remains", e)
	}
	if _, e = os.Stat(other); e != nil {
		t.Fatal("other Bottle affected", e)
	}
	if contents, e := os.ReadFile(outside); e != nil || string(contents) != "private outside data" {
		t.Fatal("symlink target affected", e)
	}
}
func TestOwnedBottleRemovalRejectsReplacedRootAndCustomLocation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "bottles")
	p := filepath.Join(root, "zah-test")
	if e := os.MkdirAll(p, 0700); e != nil {
		t.Fatal(e)
	}
	b, e := host.InspectBinding(p, false)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(p, p+"-original"); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(p, 0700); e != nil {
		t.Fatal(e)
	}
	if e = removeOwnedBottleTree(context.Background(), b, "zah-test"); e == nil {
		t.Fatal("replacement removed")
	}
	custom := filepath.Join(t.TempDir(), "zah-custom")
	if e = os.Mkdir(custom, 0700); e != nil {
		t.Fatal(e)
	}
	b, e = host.InspectBinding(custom, false)
	if e != nil {
		t.Fatal(e)
	}
	if e = removeOwnedBottleTree(context.Background(), b, "zah-custom"); e == nil {
		t.Fatal("custom location removed")
	}
}
func TestGuidedBottleRemovalRequiresExactStoppedPrefix(t *testing.T) {
	r, f := guidedBottleFixture(t, "exe")
	a := Adapter{Runner: f}
	ctx := context.Background()
	installed, e := a.prepareBottles(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	r.Windows = installed.Windows
	other := filepath.Join(f.root, "unrelated-bottle")
	if e = os.Mkdir(other, 0700); e != nil {
		t.Fatal(e)
	}
	plan, e := a.PlanRemove(r, installed.ResourceID)
	if e != nil || plan.Steps[0].Mode != "automatic" {
		t.Fatal(plan, e)
	}
	f.failWait = true
	if _, e = a.ExecuteStep(ctx, plan.Steps[0]); e == nil {
		t.Fatal("active prefix removed")
	}
	if _, e = os.Stat(r.Windows.Bottle.Path); e != nil {
		t.Fatal("failed stopped proof changed directory", e)
	}
	f.failWait = false
	removed, e := a.ExecuteStep(ctx, plan.Steps[0])
	if e != nil || removed.State != "removed" {
		t.Fatal(removed, e)
	}
	if _, e = os.Stat(other); e != nil {
		t.Fatal("other Bottle changed", e)
	}
	if _, e = a.VerifyBottleAbsent(ctx, r, installed.ResourceID); e != nil {
		t.Fatal(e)
	}
}
func TestAutomaticBottleSnapshotIsCompleteAndDoesNotOverwrite(t *testing.T) {
	r, f := guidedBottleFixture(t, "exe")
	a := Adapter{Runner: f}
	ctx := context.Background()
	installed, e := a.prepareBottles(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	r.Windows = installed.Windows
	for _, name := range []string{"bottle.yml", "system.reg", "user.reg", "userdef.reg"} {
		if e = os.WriteFile(filepath.Join(r.Windows.Bottle.Path, name), []byte("fixture metadata\n"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	destination := filepath.Join(t.TempDir(), "bottle.tar.gz")
	proof, e := a.ExportBottlesSnapshot(ctx, r, installed.ResourceID, destination)
	if e != nil || proof.State != "zah_full_bottle_verified" {
		t.Fatal(proof, e)
	}
	if _, e = a.VerifyManualBottlesBackup(ctx, r, destination, proof.SHA256); e != nil {
		t.Fatal(e)
	}
	if _, e = a.ExportBottlesSnapshot(ctx, r, installed.ResourceID, destination); e == nil {
		t.Fatal("snapshot overwritten")
	}
	if e = os.Symlink("/", filepath.Join(r.Windows.Bottle.Path, "outside")); e != nil {
		t.Fatal(e)
	}
	if _, e = a.ExportBottlesSnapshot(ctx, r, installed.ResourceID, filepath.Join(t.TempDir(), "reject.tar.gz")); e == nil {
		t.Fatal("external link followed")
	}
}
