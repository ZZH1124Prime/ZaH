package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/infra"
	"golang.org/x/sys/unix"
)

type cleanupTreeEntry struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Device   uint64 `json:"device"`
	Inode    uint64 `json:"inode"`
	Mode     uint32 `json:"mode"`
	Modified int64  `json:"modified"`
	Size     int64  `json:"size"`
	Link     string `json:"link,omitempty"`
}

func cleanupTreeDigest(entries []cleanupTreeEntry) (string, error) {
	b, e := json.Marshal(entries)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// inspectCleanupTree never follows symbolic links. It accepts only the exact
// managed HOME; an external target is retained even when its link is removed.
func inspectCleanupTree(ctx context.Context, data, id, home string) ([]cleanupTreeEntry, error) {
	if !safeInstanceID(id) || home != filepath.Join(data, "instances", id, "home") {
		return nil, errors.New("private cleanup path is not the exact managed instance HOME")
	}
	if e := infra.ValidatePrivateDir(data); e != nil {
		return nil, e
	}
	for _, parent := range []string{filepath.Join(data, "instances"), filepath.Join(data, "instances", id)} {
		if _, e := os.Lstat(parent); errors.Is(e, os.ErrNotExist) {
			return []cleanupTreeEntry{}, nil
		} else if e != nil {
			return nil, e
		}
		if e := infra.ValidatePrivateDir(parent); e != nil {
			return nil, e
		}
	}
	if _, e := os.Lstat(home); errors.Is(e, os.ErrNotExist) {
		return []cleanupTreeEntry{}, nil
	} else if e != nil {
		return nil, e
	}
	if e := infra.ValidatePrivateDir(home); e != nil {
		return nil, e
	}
	entries := []cleanupTreeEntry{}
	var device uint64
	e := filepath.WalkDir(home, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if len(entries) >= 100000 {
			return errors.New("private cleanup tree exceeds entry limit")
		}
		rel, e := filepath.Rel(home, path)
		if e != nil {
			return e
		}
		if !filepath.IsLocal(rel) {
			return domain.ErrInvalid
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) {
			return errors.New("private cleanup encountered a foreign-owned entry")
		}
		if rel == "." {
			device = uint64(stat.Dev)
		}
		if uint64(stat.Dev) != device {
			return errors.New("private cleanup refuses filesystem boundaries")
		}
		entry := cleanupTreeEntry{Path: rel, Device: uint64(stat.Dev), Inode: stat.Ino, Mode: uint32(info.Mode().Perm()), Modified: info.ModTime().UnixNano(), Size: info.Size()}
		switch {
		case info.IsDir():
			entry.Kind = "directory"
		case info.Mode()&os.ModeSymlink != 0:
			entry.Kind = "symlink"
			entry.Link, e = os.Readlink(path)
			if e != nil {
				return e
			}
		case info.Mode().IsRegular():
			entry.Kind = "file"
		default:
			return errors.New("private cleanup refuses devices, sockets and FIFOs")
		}
		entries = append(entries, entry)
		return nil
	})
	if e != nil {
		return nil, e
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func cleanupPathWithin(root, path string) bool {
	relative, e := filepath.Rel(root, strings.TrimSuffix(path, " (deleted)"))
	return e == nil && filepath.IsLocal(relative)
}
func cleanupProcError(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

// ensureNoCleanupWriters rejects mountpoints and observable same-user process
// references. It cannot prevent a new external process from opening the tree;
// explicit cold confirmation and cooperative locks remain required.
func ensureNoCleanupWriters(ctx context.Context, home string, tree []cleanupTreeEntry, procRoot string) error {
	if len(tree) == 0 {
		return nil
	}
	if procRoot == "" {
		return errors.New("process evidence source unavailable")
	}
	mountData, e := os.ReadFile(filepath.Join(procRoot, "self", "mountinfo"))
	if e != nil {
		return fmt.Errorf("cannot verify mount boundaries: %w", e)
	}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(string(mountData), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			return errors.New("invalid mount evidence")
		}
		mount := unescape.Replace(fields[4])
		if cleanupPathWithin(home, mount) {
			return errors.New("private HOME contains a mountpoint; unmount it before cleanup")
		}
	}
	identities := map[[2]uint64]bool{}
	for _, entry := range tree {
		identities[[2]uint64{entry.Device, entry.Inode}] = true
	}
	procs, e := os.ReadDir(procRoot)
	if e != nil {
		return e
	}
	for _, proc := range procs {
		if e = ctx.Err(); e != nil {
			return e
		}
		if _, e = strconv.Atoi(proc.Name()); e != nil || !proc.IsDir() {
			continue
		}
		base := filepath.Join(procRoot, proc.Name())
		info, e := os.Stat(base)
		if cleanupProcError(e) {
			continue
		}
		if e != nil {
			return e
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("process owner evidence unavailable")
		}
		if st.Uid != uint32(os.Geteuid()) {
			continue
		}
		for _, field := range []string{"cwd", "root"} {
			target, e := os.Readlink(filepath.Join(base, field))
			if cleanupProcError(e) {
				continue
			}
			if e != nil {
				return fmt.Errorf("cannot inspect process %s %s: %w", proc.Name(), field, e)
			}
			if cleanupPathWithin(home, target) {
				return fmt.Errorf("private data is in use by process %s", proc.Name())
			}
		}
		fds, e := os.ReadDir(filepath.Join(base, "fd"))
		if cleanupProcError(e) {
			continue
		}
		if e != nil {
			return fmt.Errorf("cannot inspect process %s descriptors: %w", proc.Name(), e)
		}
		for _, fd := range fds {
			info, e := os.Stat(filepath.Join(base, "fd", fd.Name()))
			if cleanupProcError(e) {
				continue
			}
			if e != nil {
				return fmt.Errorf("cannot inspect process %s descriptor: %w", proc.Name(), e)
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if ok && identities[[2]uint64{uint64(stat.Dev), stat.Ino}] {
				return fmt.Errorf("private data has an open descriptor in process %s", proc.Name())
			}
		}
		maps, e := os.ReadFile(filepath.Join(base, "maps"))
		if cleanupProcError(e) {
			continue
		}
		if e != nil {
			return fmt.Errorf("cannot inspect process %s mappings: %w", proc.Name(), e)
		}
		for _, line := range strings.Split(string(maps), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 5 {
				continue
			}
			dev := strings.Split(fields[3], ":")
			if len(dev) != 2 {
				return errors.New("invalid process mapping device")
			}
			major, e1 := strconv.ParseUint(dev[0], 16, 32)
			minor, e2 := strconv.ParseUint(dev[1], 16, 32)
			inode, e3 := strconv.ParseUint(fields[4], 10, 64)
			if e1 != nil || e2 != nil || e3 != nil {
				return errors.New("invalid process mapping identity")
			}
			if identities[[2]uint64{unix.Mkdev(uint32(major), uint32(minor)), inode}] {
				return fmt.Errorf("private data is mapped by process %s", proc.Name())
			}
		}
	}
	return nil
}

func matchesCleanupEntry(st unix.Stat_t, entry cleanupTreeEntry, checkContent bool) bool {
	kind := ""
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		kind = "directory"
	case unix.S_IFREG:
		kind = "file"
	case unix.S_IFLNK:
		kind = "symlink"
	}
	if kind != entry.Kind || uint64(st.Dev) != entry.Device || st.Ino != entry.Inode || st.Uid != uint32(os.Geteuid()) {
		return false
	}
	return !checkContent || (st.Size == entry.Size && st.Mtim.Sec*1e9+st.Mtim.Nsec == entry.Modified)
}

// removeCleanupTree walks directory file descriptors, never link targets. Only
// preflighted identities can be unlinked, and only their own directory modes may
// be relaxed to permit the explicitly approved deletion.
func removeCleanupTree(ctx context.Context, home string, entries []cleanupTreeEntry) (int64, error) {
	if len(entries) == 0 {
		if _, e := os.Lstat(home); !errors.Is(e, os.ErrNotExist) {
			return 0, errors.New("private HOME appeared after planning")
		}
		return 0, nil
	}
	expected := map[string]cleanupTreeEntry{}
	for _, entry := range entries {
		expected[entry.Path] = entry
	}
	root, ok := expected["."]
	if !ok || root.Kind != "directory" {
		return 0, domain.ErrInvalid
	}
	parent, e := unix.Open(filepath.Dir(home), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return 0, e
	}
	defer unix.Close(parent)
	var removed int64
	var removeDir func(int, string, string) error
	removeDir = func(parent int, name, rel string) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		want, ok := expected[rel]
		if !ok || want.Kind != "directory" {
			return domain.ErrConflict
		}
		fd, e := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			return e
		}
		f := os.NewFile(uintptr(fd), name)
		defer f.Close()
		var stat unix.Stat_t
		if e = unix.Fstat(fd, &stat); e != nil {
			return e
		}
		if !matchesCleanupEntry(stat, want, false) {
			return errors.New("private directory identity changed")
		}
		if e = unix.Fchmod(fd, uint32(stat.Mode&0777)|0700); e != nil {
			return e
		}
		names, e := f.Readdirnames(-1)
		if e != nil {
			return e
		}
		sort.Strings(names)
		for _, child := range names {
			if e = ctx.Err(); e != nil {
				return e
			}
			path := filepath.Join(rel, child)
			entry, ok := expected[path]
			if !ok {
				return errors.New("new private entry appeared during deletion")
			}
			if e = unix.Fstatat(fd, child, &stat, unix.AT_SYMLINK_NOFOLLOW); e != nil {
				return e
			}
			if !matchesCleanupEntry(stat, entry, entry.Kind != "directory") {
				return errors.New("private entry changed during deletion")
			}
			if entry.Kind == "directory" {
				if e = removeDir(fd, child, path); e != nil {
					return e
				}
			} else {
				if e = unix.Unlinkat(fd, child, 0); e != nil {
					return e
				}
				if entry.Kind == "file" {
					removed += entry.Size
				}
			}
		}
		if e = unix.Fsync(fd); e != nil {
			return e
		}
		if e = unix.Fstatat(parent, name, &stat, unix.AT_SYMLINK_NOFOLLOW); e != nil {
			return e
		}
		if !matchesCleanupEntry(stat, want, false) {
			return errors.New("private directory replaced before final unlink")
		}
		if e = unix.Unlinkat(parent, name, unix.AT_REMOVEDIR); e != nil {
			return e
		}
		return unix.Fsync(parent)
	}
	e = removeDir(parent, filepath.Base(home), ".")
	return removed, e
}
