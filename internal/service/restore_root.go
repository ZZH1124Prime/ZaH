package service

import (
	"context"
	"errors"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/infra"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Selection is an existing owner-controlled parent. A new random child is created; no
// archive can name a destination or overwrite an existing directory.
func inspectRestoreParent(path string) (*domain.ManagedDataRoot, error) {
	if strings.ContainsAny(path, "\x00\n\r\t:\"'`$\\;&|<>(){}[]*?!") {
		return nil, errors.New("selected path contains characters unsupported by the runtime backend")
	}
	if path == "" {
		return nil, nil
	}
	if err := infra.ValidateOwnedDirectory(path); err != nil {
		return nil, err
	}
	st, e := os.Stat(path)
	if e != nil {
		return nil, e
	}
	v, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, errors.New("restore directory identity unavailable")
	}
	return &domain.ManagedDataRoot{Path: path, Device: uint64(v.Dev), Inode: v.Ino}, nil
}
func (s *Service) instanceDataRoot(v domain.AppInstance) (string, error) {
	if v.RestoreRoot == nil {
		return s.Paths.Data, nil
	}
	root := v.RestoreRoot
	if !strings.HasPrefix(filepath.Base(root.Path), "zah-import-") || !safeInstanceID(strings.TrimPrefix(filepath.Base(root.Path), "zah-import-")) {
		return "", errors.New("invalid managed restoration root")
	}
	if e := infra.ValidatePrivateDir(root.Path); e != nil {
		return "", e
	}
	current, e := inspectRestoreParent(root.Path)
	if e != nil {
		return "", e
	}
	if *current != *root {
		return "", errors.New("restored data directory identity changed; refusing access")
	}
	if v.PrivateData != filepath.Join(root.Path, "instances", v.ID, "home") {
		return "", errors.New("restored private HOME differs from registered root")
	}
	return root.Path, nil
}
func (s *Service) inspectInstanceCleanupTree(ctx context.Context, v domain.AppInstance) ([]cleanupTreeEntry, error) {
	root, e := s.instanceDataRoot(v)
	if e != nil {
		return nil, e
	}
	return inspectCleanupTree(ctx, root, v.ID, v.PrivateData)
}
