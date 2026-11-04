// Package migration implements the unencrypted, versioned .zahbundle container.
// It verifies and stages bytes; backend restoration and catalog registration are
// separate service transactions and must not be inferred from a successful import.
package migration

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Resource describes inclusion explicitly; missing data never becomes offline ready.
type Resource struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason,omitempty"`
}
type Manifest struct {
	Format        string     `json:"format"`
	SchemaVersion int        `json:"schema_version"`
	Arch          string     `json:"arch"`
	CreatedAt     time.Time  `json:"created_at"`
	Resources     []Resource `json:"resources"`
	Omissions     []string   `json:"omissions"`
	Requirements  []string   `json:"requirements"`
	OfflineReady  bool       `json:"offline_ready"`
}
type Snapshot struct {
	Manifest Manifest
	Catalog  json.RawMessage
	Bindings json.RawMessage
}

// Entry identifies one explicitly selected regular file. No implicit directory walk occurs.
type Entry struct {
	Name string
	Path string
}
type Checksum struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Inspection struct {
	Manifest      Manifest            `json:"manifest"`
	Catalog       json.RawMessage     `json:"catalog"`
	Bindings      json.RawMessage     `json:"bindings"`
	Checksums     map[string]Checksum `json:"checksums"`
	ExpandedBytes int64               `json:"expanded_bytes"`
	Files         int                 `json:"files"`
}

// Limits apply to uncompressed bytes and members, not merely compressed input size.
type Limits struct {
	MaxBytes         int64
	MaxFileBytes     int64
	MaxMetadataBytes int64
	MaxFiles         int
	Arch             string
}

func (l Limits) defaults() Limits {
	if l.MaxBytes <= 0 {
		l.MaxBytes = 64 << 30
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = 32 << 30
	}
	if l.MaxMetadataBytes <= 0 {
		l.MaxMetadataBytes = 8 << 20
	}
	if l.MaxFiles <= 0 {
		l.MaxFiles = 100000
	}
	if l.Arch == "" {
		l.Arch = runtime.GOARCH
	}
	return l
}
func validName(n string) bool {
	if n == "" || len(n) > 4096 || strings.ContainsAny(n, "\\\x00\r\n") || strings.HasPrefix(n, "/") || path.Clean(n) != n || n == "." || n == ".." || strings.HasPrefix(n, "../") {
		return false
	}
	switch n {
	case "manifest.json", "catalog.json", "bindings.json", "checksums.json":
		return true
	}
	for _, p := range []string{"objects/sha256/", "environments/", "data/"} {
		if strings.HasPrefix(n, p) && len(n) > len(p) {
			return true
		}
	}
	return false
}
func payloadName(n string) bool { return validName(n) && strings.Contains(n, "/") }
func regular(p string) (*os.File, error) {
	fd, e := syscall.Open(p, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), p)
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("bundle source must be regular non-symlink file")
	}
	return f, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
func strictJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}
func validateManifest(m Manifest, l Limits) error {
	if m.Format != "zah.bundle" || m.SchemaVersion != 1 {
		return errors.New("unsupported bundle format or schema")
	}
	if m.Arch != l.Arch {
		return fmt.Errorf("bundle architecture %q differs from %q", m.Arch, l.Arch)
	}
	if m.CreatedAt.IsZero() {
		return errors.New("missing creation timestamp")
	}
	seen := map[string]bool{}
	for _, r := range m.Resources {
		if r.ID == "" || seen[r.ID] {
			return errors.New("missing or duplicate resource ID")
		}
		seen[r.ID] = true
		switch r.Status {
		case "packaged":
			if !payloadName(r.Path) {
				return errors.New("packaged resource has invalid path")
			}
		case "target_required", "rebind", "excluded":
			if m.OfflineReady {
				return errors.New("offline_ready conflicts with missing resources")
			}
		default:
			return errors.New("unknown resource status")
		}
	}
	if m.OfflineReady && (len(m.Omissions) > 0 || len(m.Requirements) > 0) {
		return errors.New("offline_ready conflicts with omissions or requirements")
	}
	return nil
}
func syncDir(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}

// Export writes a unique partial file, verifies it in full, and publishes without
// replacing a destination. Caller must stop writers and hold resource locks.
func Export(ctx context.Context, destination string, snapshot Snapshot, entries []Entry, limits Limits) error {
	l := limits.defaults()
	if !filepath.IsAbs(destination) {
		return errors.New("destination must be absolute")
	}
	if !strings.HasSuffix(destination, ".zahbundle") {
		return errors.New("destination must end in .zahbundle")
	}
	if _, e := os.Lstat(destination); e == nil {
		return os.ErrExist
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	m := snapshot.Manifest
	if m.Format == "" {
		m.Format = "zah.bundle"
	}
	if m.SchemaVersion == 0 {
		m.SchemaVersion = 1
	}
	if m.Arch == "" {
		m.Arch = runtime.GOARCH
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	if e := validateManifest(m, l); e != nil {
		return e
	}
	if !json.Valid(snapshot.Catalog) || !json.Valid(snapshot.Bindings) {
		return errors.New("catalog and bindings must be valid JSON")
	}
	mb, e := json.Marshal(m)
	if e != nil {
		return e
	}
	parent := filepath.Dir(destination)
	f, e := os.CreateTemp(parent, "."+filepath.Base(destination)+"-*.partial")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	checks := map[string]Checksum{}
	var total int64
	count := 0
	add := func(name string, r io.Reader, size int64, mode os.FileMode) error {
		if !validName(name) || size < 0 || size > l.MaxFileBytes {
			return errors.New("invalid or oversized bundle member")
		}
		if _, ok := checks[name]; ok {
			return errors.New("duplicate bundle member")
		}
		if count >= l.MaxFiles || size > l.MaxBytes-total {
			return errors.New("bundle limit exceeded")
		}
		if !payloadName(name) && size > l.MaxMetadataBytes {
			return errors.New("metadata limit exceeded")
		}
		if e := tw.WriteHeader(&tar.Header{Name: name, Mode: int64(mode.Perm() & 0700), Size: size, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); e != nil {
			return e
		}
		h := sha256.New()
		n, e := io.CopyN(io.MultiWriter(tw, h), contextReader{ctx, r}, size)
		if e != nil {
			return e
		}
		checks[name] = Checksum{hex.EncodeToString(h.Sum(nil)), n}
		total += n
		count++
		return nil
	}
	for _, v := range []struct {
		name string
		b    []byte
	}{{"manifest.json", mb}, {"catalog.json", snapshot.Catalog}, {"bindings.json", snapshot.Bindings}} {
		if e = add(v.name, bytes.NewReader(v.b), int64(len(v.b)), 0600); e != nil {
			return e
		}
	}
	for _, entry := range entries {
		if !payloadName(entry.Name) {
			return errors.New("invalid payload path")
		}
		src, e := regular(entry.Path)
		if e != nil {
			return e
		}
		before, e := src.Stat()
		if e != nil {
			src.Close()
			return e
		}
		e = add(entry.Name, src, before.Size(), before.Mode())
		after, statErr := src.Stat()
		src.Close()
		if e != nil {
			return e
		}
		if statErr != nil {
			return statErr
		}
		if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			return errors.New("source changed during export")
		}
	}
	cb, e := json.Marshal(checks)
	if e != nil {
		return e
	}
	if int64(len(cb)) > l.MaxMetadataBytes {
		return errors.New("checksums too large")
	}
	if e = add("checksums.json", bytes.NewReader(cb), int64(len(cb)), 0600); e != nil {
		return e
	}
	if e = tw.Close(); e != nil {
		return e
	}
	if e = gz.Close(); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if _, e = Inspect(ctx, f.Name(), l); e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	if e = os.Link(f.Name(), destination); e != nil {
		return e
	}
	return syncDir(parent)
}

// Inspect validates every member and digest without writing extracted content.
func Inspect(ctx context.Context, archive string, limits Limits) (Inspection, error) {
	return read(ctx, archive, "", limits.defaults())
}

// Import verifies into a private staging directory and commits a new directory.
// It does not execute archived code, load images, or register application instances.
func Import(ctx context.Context, archive, target string, limits Limits) (Inspection, error) {
	var empty Inspection
	if !filepath.IsAbs(target) || filepath.Clean(target) == "/" {
		return empty, errors.New("target must be an absolute new directory")
	}
	if _, e := os.Lstat(target); e == nil {
		return empty, os.ErrExist
	} else if !errors.Is(e, os.ErrNotExist) {
		return empty, e
	}
	parent := filepath.Dir(target)
	stage, e := os.MkdirTemp(parent, ".zah-import-")
	if e != nil {
		return empty, e
	}
	defer os.RemoveAll(stage)
	result, e := read(ctx, archive, stage, limits.defaults())
	if e != nil {
		return empty, e
	}
	if e = ctx.Err(); e != nil {
		return empty, e
	}
	if e = os.Mkdir(target, 0700); e != nil {
		return empty, e
	}
	if e = syscall.Rename(stage, target); e != nil {
		os.Remove(target)
		return empty, e
	}
	if e = syncDir(parent); e != nil {
		return result, e
	}
	return result, nil
}
func read(ctx context.Context, archive, stage string, l Limits) (Inspection, error) {
	result := Inspection{Checksums: map[string]Checksum{}}
	fail := func(e error) (Inspection, error) { return Inspection{}, e }
	f, e := regular(archive)
	if e != nil {
		return fail(e)
	}
	defer f.Close()
	br := bufio.NewReader(contextReader{ctx, f})
	gz, e := gzip.NewReader(br)
	if e != nil {
		return fail(e)
	}
	defer gz.Close()
	gz.Multistream(false)
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	metadata := map[string][]byte{}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return fail(e)
		}
		if !validName(h.Name) || seen[h.Name] {
			return fail(errors.New("invalid or duplicate archive path"))
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return fail(errors.New("archive links, directories and special files are forbidden"))
		}
		if h.Mode&07000 != 0 || h.Mode&^int64(0777) != 0 || h.Uid != 0 || h.Gid != 0 || h.Linkname != "" || len(h.Xattrs) > 0 {
			return fail(errors.New("privileged archive metadata forbidden"))
		}
		for k := range h.PAXRecords {
			if k != "path" && k != "size" && k != "mtime" && k != "atime" && k != "ctime" {
				return fail(errors.New("unsupported PAX metadata"))
			}
		}
		if h.Size < 0 || h.Size > l.MaxFileBytes || h.Size > l.MaxBytes-result.ExpandedBytes || result.Files >= l.MaxFiles {
			return fail(errors.New("archive expansion limit exceeded"))
		}
		if result.Files == 0 && h.Name != "manifest.json" {
			return fail(errors.New("manifest must be first member"))
		}
		if !payloadName(h.Name) && h.Size > l.MaxMetadataBytes {
			return fail(errors.New("metadata limit exceeded"))
		}
		seen[h.Name] = true
		result.Files++
		result.ExpandedBytes += h.Size
		hash := sha256.New()
		var meta bytes.Buffer
		writers := []io.Writer{hash}
		if !payloadName(h.Name) {
			writers = append(writers, &meta)
		}
		var out *os.File
		if stage != "" {
			var space syscall.Statfs_t
			if e = syscall.Statfs(stage, &space); e != nil {
				return fail(e)
			}
			if uint64(h.Size) > space.Bavail*uint64(space.Bsize) {
				return fail(errors.New("insufficient staging filesystem space"))
			}
			p := filepath.Join(stage, filepath.FromSlash(h.Name))
			if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
				return fail(e)
			}
			out, e = os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return fail(e)
			}
			writers = append(writers, out)
		}
		n, copyErr := io.CopyN(io.MultiWriter(writers...), contextReader{ctx, tr}, h.Size)
		if out != nil {
			if copyErr == nil {
				copyErr = out.Chmod(os.FileMode(h.Mode) & 0700)
			}
			if copyErr == nil {
				copyErr = out.Sync()
			}
			closeErr := out.Close()
			if copyErr == nil {
				copyErr = closeErr
			}
		}
		if copyErr != nil {
			return fail(copyErr)
		}
		digest := hex.EncodeToString(hash.Sum(nil))
		if strings.HasPrefix(h.Name, "objects/sha256/") {
			expected := strings.TrimPrefix(h.Name, "objects/sha256/")
			if expected != digest {
				return fail(errors.New("object name differs from content digest"))
			}
		}
		if h.Name != "checksums.json" {
			result.Checksums[h.Name] = Checksum{digest, n}
		}
		if !payloadName(h.Name) {
			metadata[h.Name] = meta.Bytes()
		}
		if h.Name == "manifest.json" {
			if e = strictJSON(meta.Bytes(), &result.Manifest); e != nil {
				return fail(e)
			}
			if e = validateManifest(result.Manifest, l); e != nil {
				return fail(e)
			}
		}
	}
	// Read to gzip EOF so truncation and checksum errors cannot hide after tar EOF.
	tail, e := io.ReadAll(io.LimitReader(gz, 4097))
	if e != nil {
		return fail(e)
	}
	if len(tail) > 4096 {
		return fail(errors.New("excess data after tar end"))
	}
	for _, b := range tail {
		if b != 0 {
			return fail(errors.New("data after tar end"))
		}
	}
	if _, e = br.Peek(1); e != io.EOF {
		if e == nil {
			e = errors.New("multiple gzip streams or trailing bytes forbidden")
		}
		return fail(e)
	}
	for _, name := range []string{"manifest.json", "catalog.json", "bindings.json", "checksums.json"} {
		if !seen[name] {
			return fail(fmt.Errorf("missing %s", name))
		}
	}
	if !json.Valid(metadata["catalog.json"]) || !json.Valid(metadata["bindings.json"]) {
		return fail(errors.New("invalid catalog or bindings JSON"))
	}
	var expected map[string]Checksum
	if e = strictJSON(metadata["checksums.json"], &expected); e != nil {
		return fail(e)
	}
	if len(expected) != len(result.Checksums) {
		return fail(errors.New("checksum set mismatch"))
	}
	for name, got := range result.Checksums {
		if want, ok := expected[name]; !ok || want != got {
			return fail(fmt.Errorf("checksum mismatch for %s", name))
		}
	}
	for _, r := range result.Manifest.Resources {
		if r.Status == "packaged" && !seen[r.Path] {
			return fail(fmt.Errorf("resource %s is not packaged", r.ID))
		}
	}
	result.Catalog = metadata["catalog.json"]
	result.Bindings = metadata["bindings.json"]
	if stage != "" {
		dirs := []string{}
		e = filepath.WalkDir(stage, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				dirs = append(dirs, p)
			}
			return nil
		})
		if e != nil {
			return fail(e)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
		for _, d := range dirs {
			if e = syncDir(d); e != nil {
				return fail(e)
			}
		}
	}
	return result, nil
}
