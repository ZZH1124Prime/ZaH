// Package catalog preserves installation materials as independently copied,
// content-addressed files. Database ownership and reference checks belong to service.
package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Source preserves provenance without inferring an origin or signature.
type Source struct {
	OriginalName    string    `json:"original_name"`
	OriginalPath    string    `json:"original_path"`
	ImportedAt      time.Time `json:"imported_at"`
	Platform        string    `json:"platform"`
	Arch            string    `json:"arch"`
	DeclaredVersion string    `json:"declared_version,omitempty"`
	Origin          string    `json:"origin,omitempty"`
	Signature       string    `json:"signature,omitempty"`
}

// Artifact identifies immutable archived bytes, not the version currently installed.
type Artifact struct {
	Availability string   `json:"availability,omitempty"`
	SHA256       string   `json:"sha256"`
	Size         int64    `json:"size"`
	Path         string   `json:"path"`
	Format       string   `json:"format"`
	Source       Source   `json:"source"`
	Sources      []Source `json:"sources,omitempty"`
}

// Store.Root is the artifacts/sha256 directory. Zero MaxBytes permits 32 GiB.
type Store struct {
	Root     string
	MaxBytes int64
}

func ValidDigest(s string) bool {
	if len(s) != 64 || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func (s Store) Path(digest string) (string, error) {
	if !ValidDigest(digest) {
		return "", errors.New("invalid SHA256 digest")
	}
	return filepath.Join(s.Root, digest), nil
}
func openRegular(path string) (*os.File, error) {
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("source must be a regular non-symlink file")
	}
	return f, nil
}
func (s Store) prepare() error {
	if s.Root == "" || !filepath.IsAbs(s.Root) {
		return errors.New("artifact root must be absolute")
	}
	if e := os.MkdirAll(s.Root, 0700); e != nil {
		return e
	}
	st, e := os.Lstat(s.Root)
	if e != nil {
		return e
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
		return errors.New("artifact root must be a private directory")
	}
	return nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (r ctxReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
func digestFile(ctx context.Context, f *os.File, max int64) (string, int64, error) {
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(ctxReader{ctx, f}, max+1))
	if e == nil && n > max {
		e = errors.New("artifact exceeds size limit")
	}
	return hex.EncodeToString(h.Sum(nil)), n, e
}
func syncDir(path string) error {
	d, e := os.Open(path)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

// Import copies without executing the source, verifies the copy and atomically
// publishes it without replacing existing content. Return Source must be persisted
// for every import, including a deduplicated import.
func (s Store) Import(ctx context.Context, source string) (Artifact, error) {
	var a Artifact
	if e := s.prepare(); e != nil {
		return a, e
	}
	p, e := filepath.Abs(source)
	if e != nil {
		return a, e
	}
	f, e := openRegular(p)
	if e != nil {
		return a, e
	}
	defer f.Close()
	before, e := f.Stat()
	if e != nil {
		return a, e
	}
	max := s.MaxBytes
	if max <= 0 {
		max = 32 << 30
	}
	if max > 1<<50 {
		return a, errors.New("artifact limit exceeds implementation bound")
	}
	var space syscall.Statfs_t
	if e = syscall.Statfs(s.Root, &space); e != nil {
		return a, e
	}
	if uint64(before.Size()) > space.Bavail*uint64(space.Bsize) {
		return a, errors.New("insufficient artifact filesystem space")
	}
	if before.Size() > max {
		return a, errors.New("artifact exceeds size limit")
	}
	t, e := os.CreateTemp(s.Root, ".import-")
	if e != nil {
		return a, e
	}
	defer os.Remove(t.Name())
	defer t.Close()
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(t, h), io.LimitReader(ctxReader{ctx, f}, max+1))
	if e != nil {
		return a, e
	}
	if n > max {
		return a, errors.New("artifact exceeds size limit")
	}
	after, e := f.Stat()
	if e != nil {
		return a, e
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || n != before.Size() {
		return a, errors.New("source changed during import")
	}
	if e = t.Sync(); e != nil {
		return a, e
	}
	if _, e = t.Seek(0, io.SeekStart); e != nil {
		return a, e
	}
	digest, n2, e := digestFile(ctx, t, max)
	if e != nil {
		return a, e
	}
	if n != n2 || digest != hex.EncodeToString(h.Sum(nil)) {
		return a, errors.New("copied artifact verification failed")
	}
	a = Artifact{Availability: "available", SHA256: digest, Size: n, Source: Source{OriginalName: filepath.Base(p), OriginalPath: p, ImportedAt: time.Now().UTC(), Platform: "unknown", Arch: "unknown"}}
	a.Path, _ = s.Path(digest)
	if e = t.Chmod(0400); e != nil {
		return Artifact{}, e
	}
	if e = t.Sync(); e != nil {
		return Artifact{}, e
	}
	if e = os.Link(t.Name(), a.Path); e != nil {
		if !errors.Is(e, os.ErrExist) {
			return Artifact{}, e
		}
		existing, e := s.Verify(ctx, digest)
		if e != nil {
			return Artifact{}, e
		}
		if existing.Size != n {
			return Artifact{}, errors.New("existing object size mismatch")
		}
	}
	if e = syncDir(s.Root); e != nil {
		return Artifact{}, e
	}
	a.Format, a.Source.Platform, a.Source.Arch = detect(t)
	return a, nil
}

// Verify hashes the complete archived object. It never trusts the filename alone.
func (s Store) Verify(ctx context.Context, digest string) (Artifact, error) {
	p, e := s.Path(digest)
	if e != nil {
		return Artifact{}, e
	}
	f, e := openRegular(p)
	if e != nil {
		return Artifact{}, e
	}
	defer f.Close()
	max := s.MaxBytes
	if max <= 0 {
		max = 32 << 30
	}
	if max > 1<<50 {
		return Artifact{}, errors.New("artifact limit exceeds implementation bound")
	}
	got, n, e := digestFile(ctx, f, max)
	if e != nil {
		return Artifact{}, e
	}
	if got != digest {
		return Artifact{}, fmt.Errorf("artifact integrity mismatch: %s", digest)
	}
	format, platform, arch := detect(f)
	return Artifact{Availability: "available", SHA256: digest, Size: n, Path: p, Format: format, Source: Source{Platform: platform, Arch: arch}}, nil
}
func detect(f *os.File) (string, string, string) {
	b := make([]byte, 4096)
	n, _ := f.ReadAt(b, 0)
	b = b[:n]
	format, platform, arch := "unknown", "unknown", "unknown"
	if len(b) >= 20 && string(b[:4]) == "\x7fELF" {
		format, platform = "elf", "linux"
		if len(b) > 10 && string(b[8:10]) == "AI" {
			format = "appimage"
		}
		var machine uint16
		if b[5] == 1 {
			machine = binary.LittleEndian.Uint16(b[18:20])
		} else if b[5] == 2 {
			machine = binary.BigEndian.Uint16(b[18:20])
		}
		switch machine {
		case 62:
			arch = "amd64"
		case 183:
			arch = "arm64"
		case 3:
			arch = "386"
		}
	} else if len(b) >= 8 && string(b[:8]) == "!<arch>\n" {
		format = "ar"
		if strings.Contains(string(b), "debian-binary") {
			format, platform = "deb", "linux"
		}
	} else if len(b) >= 64 && string(b[:2]) == "MZ" {
		off := int64(binary.LittleEndian.Uint32(b[60:64]))
		h := make([]byte, 6)
		if off >= 64 && off < 1<<20 {
			if n, _ := f.ReadAt(h, off); n == 6 && string(h[:4]) == "PE\x00\x00" {
				format, platform = "exe", "windows"
				switch binary.LittleEndian.Uint16(h[4:]) {
				case 0x8664:
					arch = "amd64"
				case 0x14c:
					arch = "386"
				case 0xaa64:
					arch = "arm64"
				}
			}
		}
	} else if len(b) >= 8 && string(b[:8]) == "\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1" {
		format = "ole"
	}
	return format, platform, arch
}
