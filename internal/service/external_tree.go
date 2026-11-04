package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

// collectExternalTree reads through a directory descriptor. Shared directories
// need not be owner-private, unlike managed HOME. Root-bound open operations
// prevent a concurrent symlink replacement from escaping the selected root.
// Optional stage receives independent copies, so the archive never rereads a
// mutable external pathname after this traversal.
func collectExternalTree(ctx context.Context, b domain.FileBinding, stage string) (privateTree, error) {
	tree := privateTree{SchemaVersion: 1}
	root, e := os.OpenRoot(b.HostPath)
	if e != nil {
		return tree, e
	}
	defer root.Close()
	st, e := root.Stat(".")
	if e != nil {
		return tree, e
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || uint64(sys.Dev) != b.Device || sys.Ino != b.Inode {
		return tree, errors.New("selected external directory identity changed")
	}
	total := int64(0)
	e = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if !treePath(rel) || len(tree.Entries) >= 90000 {
			return errors.New("external tree path or entry limit exceeded")
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || uint64(stat.Dev) != b.Device {
			return errors.New("external tree crosses a filesystem boundary")
		}
		item := privateTreeEntry{Path: rel, Mode: uint32(info.Mode().Perm() & 0700)}
		switch {
		case d.IsDir():
			item.Kind = "directory"
		case d.Type()&os.ModeSymlink != 0:
			target, e := root.Readlink(rel)
			if e != nil {
				return e
			}
			if filepath.IsAbs(target) || strings.ContainsAny(target, "\\\x00\r\n") || !treePath(filepath.ToSlash(filepath.Join(filepath.Dir(rel), target))) {
				return errors.New("external link escapes selected directory or uses an unsupported absolute target")
			}
			resolved, e := root.Stat(rel)
			if e != nil || (!resolved.IsDir() && !resolved.Mode().IsRegular()) {
				return errors.New("external link is dangling, cyclic or targets a special file")
			}
			item.Kind = "symlink"
			item.Mode = 0
			item.Target = filepath.ToSlash(target)
		case d.Type().IsRegular():
			f, e := root.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if e != nil {
				return e
			}
			defer f.Close()
			before, e := f.Stat()
			if e != nil || !before.Mode().IsRegular() || !os.SameFile(info, before) || before.Size() > 32<<30 {
				return errors.New("external file type or identity changed")
			}
			hash := sha256.New()
			var writer io.Writer = hash
			var out *os.File
			if stage != "" {
				path := filepath.Join(stage, filepath.FromSlash(rel))
				if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
					return e
				}
				out, e = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if e != nil {
					return e
				}
				defer out.Close()
				writer = io.MultiWriter(out, hash)
			}
			n, e := io.Copy(writer, io.LimitReader(treeReader{ctx, f}, (32<<30)+1))
			if e != nil {
				return e
			}
			after, e := f.Stat()
			if e != nil || n != before.Size() || n != after.Size() || !before.ModTime().Equal(after.ModTime()) || n > (64<<30)-total {
				return errors.New("external file changed or tree exceeds size limit")
			}
			if out != nil {
				if e = out.Sync(); e != nil {
					return e
				}
			}
			total += n
			item.Kind = "file"
			item.Size = n
			item.SHA256 = hex.EncodeToString(hash.Sum(nil))
		default:
			return errors.New("external tree contains a socket, device or FIFO")
		}
		tree.Entries = append(tree.Entries, item)
		return nil
	})
	return tree, e
}
