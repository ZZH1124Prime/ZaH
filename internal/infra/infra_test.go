package infra

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPrivatePathsAndFallback(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	for _, key := range []string{"XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(key, "")
	}
	p, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if p.Runtime != filepath.Join(root, ".local/state/zah/runtime") {
		t.Fatal(p)
	}
	if err = p.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err = p.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{p.Data, p.Config, p.Cache, p.State, p.Runtime} {
		if err = ValidatePrivateDir(path); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_DATA_HOME", "relative")
	if _, err = Resolve(); err == nil {
		t.Fatal("relative XDG accepted")
	}
}
func TestRejectLinksAndPermissiveDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(filepath.Join(link, "child")); err == nil {
		t.Fatal("followed symlink")
	}
	if _, err := os.Stat(filepath.Join(other, "child")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created through link")
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(root); err == nil {
		t.Fatal("accepted permissive directory")
	}
}
func TestLocksConflictAndRelease(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "global.lock")
	l, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = AcquireLock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("want locked: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()
	if _, err = AcquireLocks(ctx, dir, "environment-z", "global", "environment-a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	set, err := AcquireLocks(context.Background(), dir, "global", "environment-a", "environment-a")
	if err != nil {
		t.Fatal(err)
	}
	if err = set.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = AcquireLocks(context.Background(), dir, "../escape"); err == nil {
		t.Fatal("invalid lock accepted")
	}
}
func TestRejectLockSymlinkAndHardlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if l, err := AcquireLock(link); err == nil {
		l.Close()
		t.Fatal("symlink accepted")
	}
	hard := filepath.Join(dir, "hard")
	if err := os.Link(target, hard); err != nil {
		t.Fatal(err)
	}
	if l, err := AcquireLock(hard); err == nil {
		l.Close()
		t.Fatal("hardlink accepted")
	}
}

func TestRejectWritableAncestor(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(filepath.Join(dir, "child")); err == nil {
		t.Fatal("accepted replaceable private directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "child")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created inside unsafe parent")
	}
}

func TestDefaultGroupWritableStateUsesPrivateDataWithoutChangingParent(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	t.Setenv("HOME", root)
	for _, k := range []string{"XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(k, "")
	}
	parent := filepath.Join(root, ".local", "state")
	if e := os.MkdirAll(parent, 0755); e != nil {
		t.Fatal(e)
	}
	os.Chmod(parent, 0775)
	p, e := Resolve()
	if e != nil {
		t.Fatal(e)
	}
	want := filepath.Join(root, ".local/share/zah/state/zah")
	if p.State != want || p.Runtime != filepath.Join(want, "runtime") {
		t.Fatal(p)
	}
	if e = p.Ensure(); e != nil {
		t.Fatal(e)
	}
	st, _ := os.Stat(parent)
	if st.Mode().Perm() != 0775 {
		t.Fatal("changed shared state parent")
	}
	if _, e = os.Stat(filepath.Join(parent, "zah")); !os.IsNotExist(e) {
		t.Fatal("wrote inside unsafe parent")
	}
	// A later chmod by the user must not silently switch existing state locations.
	os.Chmod(parent, 0755)
	again, e := Resolve()
	if e != nil || again.State != want {
		t.Fatal(again, e)
	}
	os.Mkdir(filepath.Join(parent, "zah"), 0700)
	if _, e = Resolve(); e == nil {
		t.Fatal("ambiguous state accepted")
	}
}
func TestExistingOrExplicitStateIsNeverSilentlyRelocated(t *testing.T) {
	for _, mode := range []string{"existing", "explicit"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			os.Chmod(root, 0700)
			t.Setenv("HOME", root)
			for _, k := range []string{"XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
				t.Setenv(k, "")
			}
			parent := filepath.Join(root, ".local/state")
			os.MkdirAll(parent, 0755)
			os.Chmod(parent, 0775)
			if mode == "explicit" {
				t.Setenv("XDG_STATE_HOME", parent)
			} else {
				os.Mkdir(filepath.Join(parent, "zah"), 0700)
			}
			p, e := Resolve()
			if e != nil {
				t.Fatal(e)
			}
			if p.State != filepath.Join(parent, "zah") {
				t.Fatal("existing state relocated")
			}
			if e = p.Ensure(); e == nil {
				t.Fatal("weakened writable ancestor validation")
			}
		})
	}
}
