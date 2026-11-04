package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/host"
	"github.com/ZZH1124Prime/ZaH/internal/process"
)

func selectedExternal(t *testing.T, s *Service, instance string) (domain.FileBinding, string) {
	t.Helper()
	root := t.TempDir()
	file := filepath.Join(root, "shared.txt")
	if e := os.WriteFile(file, []byte("selected external bytes"), 0600); e != nil {
		t.Fatal(e)
	}
	ev, e := host.InspectBinding(root, true)
	if e != nil {
		t.Fatal(e)
	}
	b := domain.FileBinding{ID: domain.NewID(), InstanceID: instance, HostPath: ev.Path, TargetPath: "/mnt/zah/shared", Access: "read", Device: ev.Device, Inode: ev.Inode, State: "verified"}
	if e = s.save(context.Background(), "binding", b.ID, b, ref("binding", b.ID, "instance", instance, "binding")); e != nil {
		t.Fatal(e)
	}
	return b, file
}
func TestSelectedExternalDataRestoresToNewDirectoryWithoutAutoBinding(t *testing.T) {
	s, v := fullSource(t)
	ctx := context.Background()
	selected, file := selectedExternal(t, s, v.ID)
	omitted, _ := selectedExternal(t, s, v.ID)
	omitted.TargetPath = "/mnt/zah/omitted"
	if e := s.Store.Put(ctx, record("binding", omitted.ID, omitted)); e != nil {
		t.Fatal(e)
	}
	archive := filepath.Join(t.TempDir(), "selected.zahbundle")
	result, e := s.ExportEnvironmentBundle(ctx, EnvironmentBundleInput{ID: v.ID, Path: archive, ColdConfirmed: true, IncludeBindings: []string{selected.ID}})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(file); e != nil {
		t.Fatal(e)
	}
	if _, ok := result.Inspection.Checksums[bindingMember(v.ID, omitted.ID)+"tree.json"]; ok {
		t.Fatal("copied unselected external contents")
	}
	target := catalogService(t)
	restored, e := target.RestoreEnvironmentBundle(ctx, archive)
	if e != nil {
		t.Fatal(e)
	}
	b, e := get[domain.FileBinding](ctx, target.Store, "binding", restored.IDMap["binding:"+selected.ID])
	if e != nil {
		t.Fatal(e)
	}
	if b.HostPath != "" || b.State != "needs_binding" || !strings.HasPrefix(b.RestoredData, target.Paths.Data+string(os.PathSeparator)) {
		t.Fatalf("unsafe rebound path %+v", b)
	}
	contents, e := os.ReadFile(filepath.Join(b.RestoredData, "shared.txt"))
	if e != nil || string(contents) != "selected external bytes" {
		t.Fatalf("%q %v", contents, e)
	}
	other, _ := get[domain.FileBinding](ctx, target.Store, "binding", restored.IDMap["binding:"+omitted.ID])
	if other.RestoredData != "" {
		t.Fatal("invented restored external contents")
	}
}

type changingExternalRunner struct {
	delegate process.Runner
	path     string
}

func (r changingExternalRunner) Run(ctx context.Context, c process.Command) (process.Result, error) {
	if len(c.Args) > 1 && c.Args[0] == "container" && c.Args[1] == "commit" {
		if e := os.WriteFile(r.path, []byte("changed by external writer"), 0600); e != nil {
			return process.Result{}, e
		}
	}
	return r.delegate.Run(ctx, c)
}
func TestExternalWriterChangePreventsPublishingBundle(t *testing.T) {
	s, v := fullSource(t)
	b, file := selectedExternal(t, s, v.ID)
	s.Backend = backend.Adapter{Runner: changingExternalRunner{delegate: s.Backend.Runner, path: file}}
	archive := filepath.Join(t.TempDir(), "changed.zahbundle")
	if _, e := s.ExportEnvironmentBundle(context.Background(), EnvironmentBundleInput{ID: v.ID, Path: archive, ColdConfirmed: true, IncludeBindings: []string{b.ID}}); e == nil {
		t.Fatal("published changing external data")
	}
	if _, e := os.Stat(archive); !os.IsNotExist(e) {
		t.Fatal("published partial bundle", e)
	}
}
func TestExternalSelectionMustBelongToThisInstance(t *testing.T) {
	s, v := fullSource(t)
	b, _ := selectedExternal(t, s, v.ID)
	for _, ids := range [][]string{{b.ID, b.ID}, {"../escape"}, {domain.NewID()}} {
		if _, e := s.selectedBindings(context.Background(), v.ID, ids); e == nil {
			t.Fatal("accepted unsafe selection", ids)
		}
	}
	if _, e := s.selectedBindings(context.Background(), "different-instance", []string{b.ID}); e == nil {
		t.Fatal("selected another instance binding")
	}
}

func TestExternalRootRejectsEscapingLinksAndReplacement(t *testing.T) {
	s, v := fullSource(t)
	b, _ := selectedExternal(t, s, v.ID)
	outside := filepath.Join(t.TempDir(), "secret")
	if e := os.WriteFile(outside, []byte("must not archive"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(outside, filepath.Join(b.HostPath, "escape")); e != nil {
		t.Fatal(e)
	}
	if _, e := collectExternalTree(context.Background(), b, t.TempDir()); e == nil {
		t.Fatal("accepted link outside selected tree")
	}
	if e := os.Remove(filepath.Join(b.HostPath, "escape")); e != nil {
		t.Fatal(e)
	}
	if e := os.Rename(b.HostPath, b.HostPath+"-old"); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(b.HostPath, 0700); e != nil {
		t.Fatal(e)
	}
	if _, e := collectExternalTree(context.Background(), b, ""); e == nil {
		t.Fatal("accepted replaced directory inode")
	}
}
