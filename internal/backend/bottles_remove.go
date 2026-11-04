package backend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ZZH1124Prime/ZaH/internal/host"
	"golang.org/x/sys/unix"
)

// removeBottles only removes a dedicated, default-location ZaH Bottle after
// matching its registration and exact Wine server namespace. It never invokes
// upstream's global cleanup, desktop-entry glob, or a shell rm command.
func (a Adapter) removeBottles(ctx context.Context, r InstallRequest, id string) (Evidence, error) {
	ev := Evidence{ResourceID: id, State: "unknown"}
	if r.Windows == nil || r.Windows.Control == nil || r.Windows.ResourceIdentity() != id {
		return ev, errors.New("exact Bottle registration required")
	}
	if _, e := a.ConfirmStopped(ctx, r, id); e != nil {
		return ev, e
	}
	current, e := a.newBottleRoot(ctx, r)
	if e != nil {
		return ev, e
	}
	if current.Path != r.Windows.Bottle.Path || current.Device != r.Windows.Bottle.Device || current.Inode != r.Windows.Bottle.Inode {
		return ev, errors.New("Bottle directory differs from registered identity")
	}
	if e = removeOwnedBottleTree(ctx, current, name(r.InstanceID)); e != nil {
		return ev, e
	}
	if _, e = a.VerifyBottleAbsent(ctx, r, id); e != nil {
		return ev, e
	}
	ev.State = "removed"
	ev.Windows = r.Windows
	return ev, nil
}

func (a Adapter) VerifyBottleAbsent(ctx context.Context, r InstallRequest, id string) (Evidence, error) {
	ev := Evidence{ResourceID: id, State: "unknown"}
	if r.Windows == nil || r.Windows.InstanceID != r.InstanceID || r.Windows.ResourceIdentity() != id || r.Windows.BottleName != name(r.InstanceID) {
		return ev, errors.New("matching Bottle removal registration required")
	}
	parent := filepath.Dir(r.Windows.Bottle.Path)
	if filepath.Base(parent) != "bottles" || filepath.Base(r.Windows.Bottle.Path) != name(r.InstanceID) {
		return ev, errors.New("only default-location dedicated Bottles can be removed")
	}
	canonical, e := filepath.EvalSymlinks(parent)
	if e != nil || canonical != parent {
		return ev, errors.New("Bottle parent directory changed")
	}
	if _, e = os.Lstat(r.Windows.Bottle.Path); !errors.Is(e, os.ErrNotExist) {
		return ev, errors.New("Bottle directory absence not proved")
	}
	inventory, e := a.bottleCatalog(ctx)
	if e != nil {
		return ev, e
	}
	if _, ok := inventory[name(r.InstanceID)]; ok {
		return ev, errors.New("Bottle still appears in upstream inventory")
	}
	ev.State = "absent"
	return ev, nil
}

// The caller has proved no Wine writers and holds the environment lock. Move
// the pinned directory to a random, non-overwriting name before deleting through
// an os.Root. Symlinks are unlinked, never traversed; mountpoints are refused.
// A failed removal retains its quarantine directory for inspection, not replay.
func removeOwnedBottleTree(ctx context.Context, b host.BindingEvidence, expectedName string) error {
	if filepath.Base(b.Path) != expectedName || !strings.HasPrefix(expectedName, "zah-") || filepath.Base(filepath.Dir(b.Path)) != "bottles" {
		return errors.New("not an exact default-location ZaH Bottle")
	}
	canonical, e := filepath.EvalSymlinks(b.Path)
	if e != nil || canonical != b.Path {
		return errors.New("Bottle path traverses a symbolic link")
	}
	if e = host.VerifyBinding(b); e != nil {
		return e
	}
	mounts, e := os.ReadFile("/proc/self/mountinfo")
	if e != nil {
		return e
	}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(string(mounts), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			return errors.New("invalid mount evidence")
		}
		p := unescape.Replace(fields[4])
		if p == b.Path || strings.HasPrefix(p, b.Path+string(os.PathSeparator)) {
			return errors.New("Bottle contains a mountpoint; removal refused")
		}
	}
	count := 0
	e = filepath.WalkDir(b.Path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		count++
		if count > 1000000 {
			return errors.New("Bottle tree exceeds removal inspection limit")
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || uint64(st.Dev) != b.Device {
			return errors.New("Bottle filesystem boundary changed")
		}
		if !(info.Mode().IsRegular() || info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return errors.New("Bottle contains unsupported special files")
		}
		return nil
	})
	if e != nil {
		return e
	}
	parent, e := os.OpenRoot(filepath.Dir(b.Path))
	if e != nil {
		return e
	}
	defer parent.Close()
	dir, e := parent.Open(".")
	if e != nil {
		return e
	}
	defer dir.Close()
	if e = host.VerifyBinding(b); e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	token := make([]byte, 16)
	if _, e = rand.Read(token); e != nil {
		return e
	}
	quarantine := ".zah-delete-" + hex.EncodeToString(token)
	if e = unix.Renameat2(int(dir.Fd()), expectedName, int(dir.Fd()), quarantine, unix.RENAME_NOREPLACE); e != nil {
		return e
	}
	info, e := parent.Lstat(quarantine)
	if e != nil {
		return e
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || uint64(st.Dev) != b.Device || st.Ino != b.Inode {
		return errors.New("Bottle changed during removal; quarantine preserved for inspection")
	}
	if e = parent.RemoveAll(quarantine); e != nil {
		return fmt.Errorf("Bottle removal incomplete; quarantine %s retained: %w", quarantine, e)
	}
	return dir.Sync()
}
