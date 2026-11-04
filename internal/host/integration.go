package host

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// BindingEvidence pins a selected directory's identity, detecting an absent or
// replaced mount point. It is evidence, not a guarantee against concurrent edits.
type BindingEvidence struct {
	Path     string `json:"path"`
	Device   uint64 `json:"device"`
	Inode    uint64 `json:"inode"`
	ReadOnly bool   `json:"read_only"`
}

// InspectBinding resolves an existing directory, never creating an empty mount point.
func InspectBinding(path string, readOnly bool) (BindingEvidence, error) {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\n\r") {
		return BindingEvidence{}, errors.New("binding path must be absolute")
	}
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return BindingEvidence{}, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return BindingEvidence{}, err
	}
	if !st.IsDir() {
		return BindingEvidence{}, errors.New("binding must be a directory")
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return BindingEvidence{}, errors.New("directory identity unavailable")
	}
	mode := uint32(4 | 1)
	if !readOnly {
		mode |= 2
	}
	if err = syscall.Access(p, mode); err != nil {
		return BindingEvidence{}, err
	}
	return BindingEvidence{p, uint64(s.Dev), s.Ino, readOnly}, nil
}

// VerifyBinding fails closed if a directory or mount was replaced since selection.
func VerifyBinding(old BindingEvidence) error {
	now, err := InspectBinding(old.Path, old.ReadOnly)
	if err != nil {
		return err
	}
	if now.Path != old.Path || now.Device != old.Device || now.Inode != old.Inode {
		return errors.New("binding identity changed; select the directory again")
	}
	return nil
}

// AvailableBytes reports free space for the actual filesystem containing path.
func AvailableBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

var desktopID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{7,63}$`)

// DesktopEntry returns a deterministic desktop entry for one stable instance ID.
// Installation is separate so callers can review the exact file before writing.
func DesktopEntry(executable, instanceID, displayName string) ([]byte, error) {
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable || strings.ContainsAny(executable, "\x00\r\n") || !desktopID.MatchString(instanceID) || strings.ContainsAny(displayName, "\x00\r\n") || displayName == "" {
		return nil, errors.New("invalid desktop entry arguments")
	}
	// Exec quoting is distinct from shell quoting. Backslashes are escaped twice:
	// once for Exec parsing and once for the desktop entry string value.
	q := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "`", "\\`", "$", "\\$", "%", "%%").Replace(executable)
	q = strings.ReplaceAll(q, "\\", "\\\\")
	label := strings.ReplaceAll(displayName, "\\", "\\\\")
	return []byte(fmt.Sprintf("[Desktop Entry]\nType=Application\nName=%s\nExec=\"%s\" launch %s\nIcon=application-x-executable\nTerminal=false\nStartupNotify=false\nCategories=Utility;\n", label, q, instanceID)), nil
}

// InstallDesktopEntry creates only a new ZaH-owned name; it never overwrites an
// existing shortcut, follows directory symlinks or launches a desktop command.
func InstallDesktopEntry(applicationsDir, executable, instanceID, displayName string) (string, error) {
	b, err := DesktopEntry(executable, instanceID, displayName)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(applicationsDir) || filepath.Clean(applicationsDir) != applicationsDir {
		return "", errors.New("applications directory must be absolute")
	}
	for p := applicationsDir; p != "/"; p = filepath.Dir(p) {
		s, e := os.Lstat(p)
		if e != nil {
			return "", e
		}
		if !s.IsDir() || s.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("unsafe applications directory")
		}
	}
	p := filepath.Join(applicationsDir, "zah-"+instanceID+".desktop")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(p)
		}
	}()
	if _, err = f.Write(b); err != nil {
		return "", err
	}
	if err = f.Sync(); err != nil {
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	ok = true
	return p, nil
}
