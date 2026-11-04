// Package infra provides private paths and cooperative Linux process locks.
package infra

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Paths separates persistent data, configuration, state, cache and IPC files.
type Paths struct{ Data, Config, State, Cache, Runtime string }

var ErrWritableAncestor = errors.New("writable ancestor directory rejected")

// Resolve resolves user paths without creating files. An insecure or unavailable
// XDG runtime directory is replaced by a private directory below user state.
func Resolve() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	if !filepath.IsAbs(home) {
		return Paths{}, errors.New("home must be absolute")
	}
	base := func(key, fallback string) (string, error) {
		v := os.Getenv(key)
		if v == "" {
			v = filepath.Join(home, fallback)
		}
		if !filepath.IsAbs(v) {
			return "", fmt.Errorf("%s must be absolute", key)
		}
		return filepath.Join(filepath.Clean(v), "zah"), nil
	}
	var p Paths
	for _, field := range []struct {
		key, fallback string
		target        *string
	}{{"XDG_DATA_HOME", ".local/share", &p.Data}, {"XDG_CONFIG_HOME", ".config", &p.Config}, {"XDG_STATE_HOME", ".local/state", &p.State}, {"XDG_CACHE_HOME", ".cache", &p.Cache}} {
		v, e := base(field.key, field.fallback)
		if e != nil {
			return p, e
		}
		*field.target = v
	}
	// A default XDG state parent may have a group-writable umask (e.g. 0775).
	// Never chmod a shared user directory or weaken ancestor checks. For a new
	// state location only, keep ZaH's state inside its existing private data tree.
	if os.Getenv("XDG_STATE_HOME") == "" {
		fallback := filepath.Join(p.Data, "state", "zah")
		_, originalErr := os.Lstat(p.State)
		_, fallbackErr := os.Lstat(fallback)
		if fallbackErr == nil {
			if !os.IsNotExist(originalErr) {
				return Paths{}, errors.New("both default and fallback ZaH state paths exist; explicitly select XDG_STATE_HOME after inspection")
			}
			if e := ValidatePrivateDir(fallback); e != nil {
				return Paths{}, e
			}
			p.State = fallback
		} else if !os.IsNotExist(fallbackErr) {
			return Paths{}, fallbackErr
		} else if os.IsNotExist(originalErr) && errors.Is(noSymlinkComponents(p.State), ErrWritableAncestor) {
			if e := noSymlinkComponents(p.Data); e != nil {
				return Paths{}, e
			}
			p.State = fallback
		}
	}
	p.Runtime = filepath.Join(p.State, "runtime")
	if v := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(v) && ValidatePrivateDir(v) == nil {
		p.Runtime = filepath.Join(v, "zah")
	}
	return p, nil
}

// Ensure creates directories with user-only access. Existing insecure paths are
// rejected rather than silently changing ownership or permissions.
func (p Paths) Ensure() error {
	for _, v := range []string{p.Config, p.Data, p.State, p.Cache, p.Runtime} {
		if err := EnsurePrivateDir(v); err != nil {
			return err
		}
	}
	return nil
}

// ValidatePrivateDir rejects links, foreign ownership and group/world access.
func ValidatePrivateDir(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("private directory must be absolute")
	}
	if err := noSymlinkComponents(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || st.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("directory %q must be owned by this user and mode 0700", path)
	}
	return nil
}

// EnsurePrivateDir does not follow symlink path components. A same-user malicious
// process is outside the threat model, as specified by the product contract.
func EnsurePrivateDir(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) == "/" {
		return errors.New("invalid private directory")
	}
	if err := noSymlinkComponents(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	return ValidatePrivateDir(path)
}

func noSymlinkComponents(path string) error {
	path = filepath.Clean(path)
	current := string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		i, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if i.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink path component rejected: %q", current)
		}
		if !i.IsDir() {
			return fmt.Errorf("non-directory path component: %q", current)
		}
		st, ok := i.Sys().(*syscall.Stat_t)
		if !ok || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) {
			return fmt.Errorf("untrusted directory owner: %q", current)
		}
		if i.Mode().Perm()&0022 != 0 && i.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf("%w: %q", ErrWritableAncestor, current)
		}
	}
	return nil
}

// ValidateOwnedDirectory checks a selected parent without changing its mode.
// New private children can safely live beneath an owner-controlled 0755 parent;
// parent traversal still rejects links and writable/untrusted ancestors.
func ValidateOwnedDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("selected directory must be an absolute clean path")
	}
	if err := noSymlinkComponents(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || st.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 {
		return errors.New("selected directory must be owned by this user and not writable by other users")
	}
	return nil
}
