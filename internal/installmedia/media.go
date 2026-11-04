// Package installmedia inspects installation materials without executing them.
package installmedia

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const MaxExpanded uint64 = 8 << 30
const MaxEntries = 20000

type Entry struct {
	Path string `json:"path"`
	Name string `json:"name"`
}
type Info struct {
	Format        string   `json:"format"`
	Platform      string   `json:"platform"`
	Architecture  string   `json:"architecture"`
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Entries       []Entry  `json:"entries"`
	ExpandedBytes uint64   `json:"expanded_bytes"`
	Notes         []string `json:"notes"`
}

func openFile(p string) (*os.File, error) {
	fd, e := syscall.Open(p, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), p)
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() > 32<<30 {
		f.Close()
		return nil, errors.New("bounded regular material required")
	}
	return f, nil
}

// PEArchitecture accepts Windows x86 and AMD64 binaries; ARM is not supported.
func PEArchitecture(r io.ReaderAt, size int64) string {
	var h [64]byte
	if _, e := r.ReadAt(h[:], 0); e != nil || string(h[:2]) != "MZ" {
		return ""
	}
	off := int64(binary.LittleEndian.Uint32(h[60:]))
	if off < 64 || off > 16<<20 || off+6 > size {
		return ""
	}
	var p [6]byte
	_, e := r.ReadAt(p[:], off)
	if e != nil || string(p[:4]) != "PE\x00\x00" {
		return ""
	}
	switch binary.LittleEndian.Uint16(p[4:]) {
	case 0x8664:
		return "amd64"
	case 0x014c:
		return "386"
	}
	return ""
}
func pe(r io.ReaderAt, size int64) bool { return PEArchitecture(r, size) != "" }

// EXEName validates the original basename without changing or executing it.
func EXEName(name string) error {
	if name == "" || path.Base(name) != name || !strings.EqualFold(path.Ext(name), ".exe") {
		return errors.New("invalid original executable filename")
	}
	_, err := zipName(name)
	return err
}

func Inspect(ctx context.Context, p string) (Info, error) {
	f, e := openFile(p)
	if e != nil {
		return Info{}, e
	}
	defer f.Close()
	st, _ := f.Stat()
	h := make([]byte, 16)
	n, _ := f.ReadAt(h, 0)
	h = h[:n]
	info := Info{Architecture: "amd64", Entries: []Entry{}, Notes: []string{}}
	switch {
	case len(h) >= 8 && string(h[:8]) == "!<arch>\n":
		return inspectDeb(ctx, p)
	case len(h) >= 12 && string(h[:4]) == "\x7fELF" && string(h[8:11]) == "AI\x02":
		var machine [2]byte
		_, e = f.ReadAt(machine[:], 18)
		if e != nil || h[4] != 2 || h[5] != 1 || binary.LittleEndian.Uint16(machine[:]) != 62 {
			return info, errors.New("only x86_64 AppImage type 2 supported")
		}
		info.Format = "appimage"
		info.Platform = "linux"
		info.Entries = []Entry{{"squashfs-root/AppRun", "AppImage application"}}
		return info, nil
	case len(h) >= 4 && string(h[:2]) == "PK":
		z, e := zip.NewReader(f, st.Size())
		if e != nil {
			return info, e
		}
		entries, total, e := validateZip(ctx, z)
		if e != nil {
			return info, e
		}
		info.Format = "zip"
		info.Platform = "windows"
		info.Entries = entries
		info.ExpandedBytes = total
		info.Notes = []string{"ZIP may contain an installer or a portable application; choose explicitly. All regular files are preserved."}
		return info, nil
	case pe(f, st.Size()):
		info.Architecture = PEArchitecture(f, st.Size())
		info.Format = "exe"
		info.Platform = "windows"
		info.Entries = []Entry{{"program.exe", "Selected executable"}}
		info.Notes = []string{"EXE alone does not establish installer versus portable mode."}
		return info, nil
	default:
		return info, errors.New("supported materials: amd64 deb, x86_64 type-2 AppImage, x86 or AMD64 PE EXE, or safe Windows ZIP")
	}
}
func zipName(name string) (string, error) {
	if len(name) > 4096 || strings.Count(name, "/") > 64 || strings.ContainsAny(name, `<>"|?*`) {
		return "", errors.New("unsupported Windows ZIP name")
	}
	for _, c := range name {
		if c < 32 {
			return "", errors.New("control character in ZIP path")
		}
	}
	if strings.ContainsAny(name, "\\:\x00\r\n") || strings.HasPrefix(name, "/") {
		return "", errors.New("unsafe ZIP path")
	}
	s := strings.TrimSuffix(name, "/")
	if s == "" || path.Clean(s) != s || !filepath.IsLocal(s) {
		return "", errors.New("unsafe ZIP path")
	}
	for _, c := range strings.Split(s, "/") {
		b := strings.ToUpper(strings.SplitN(c, ".", 2)[0])
		if strings.TrimRight(c, " .") != c || b == "CON" || b == "PRN" || b == "AUX" || b == "NUL" || (len(b) == 4 && (strings.HasPrefix(b, "COM") || strings.HasPrefix(b, "LPT")) && b[3] >= '0' && b[3] <= '9') {
			return "", errors.New("ambiguous Windows ZIP path")
		}
	}
	return s, nil
}
func validateZip(ctx context.Context, z *zip.Reader) ([]Entry, uint64, error) {
	if len(z.File) > MaxEntries {
		return nil, 0, errors.New("ZIP entry limit exceeded")
	}
	seen := map[string]bool{}
	files := map[string]bool{}
	var total uint64
	entries := []Entry{}
	for _, f := range z.File {
		if e := ctx.Err(); e != nil {
			return nil, 0, e
		}
		s, e := zipName(f.Name)
		if e != nil {
			return nil, 0, e
		}
		key := strings.ToLower(s)
		if seen[key] || f.Flags&1 != 0 || (!f.Mode().IsRegular() && !f.Mode().IsDir()) {
			return nil, 0, errors.New("ZIP duplicate, encrypted member or special/link entry rejected")
		}
		seen[key] = true
		if f.UncompressedSize64 > 2<<30 || total > MaxExpanded-f.UncompressedSize64 {
			return nil, 0, errors.New("ZIP expanded size limit exceeded")
		}
		total += f.UncompressedSize64
		if f.Mode().IsDir() {
			continue
		}
		files[key] = true
		if strings.EqualFold(path.Ext(s), ".exe") {
			rd, e := f.Open()
			if e != nil {
				return nil, 0, e
			}
			data, e := io.ReadAll(io.LimitReader(rd, 16<<20+64))
			rd.Close()
			if e != nil {
				return nil, 0, e
			}
			if pe(bytes.NewReader(data), int64(len(data))) {
				entries = append(entries, Entry{s, path.Base(s)})
			}
			if len(entries) > 256 {
				return nil, 0, errors.New("too many executable candidates")
			}
		}
	}
	for s := range seen {
		for p := path.Dir(s); p != "."; p = path.Dir(p) {
			if files[p] {
				return nil, 0, errors.New("ZIP file/directory collision")
			}
		}
	}
	if len(entries) == 0 {
		return nil, 0, errors.New("ZIP has no supported x86 or AMD64 executable")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, total, nil
}

// ExtractZIP publishes the entire regular-file tree into a new destination.
// It never follows archive links, overwrites a destination, or executes a member.
func ExtractZIP(ctx context.Context, source, destination string) error {
	f, e := openFile(source)
	if e != nil {
		return e
	}
	defer f.Close()
	st, _ := f.Stat()
	z, e := zip.NewReader(f, st.Size())
	if e != nil {
		return e
	}
	_, _, e = validateZip(ctx, z)
	if e != nil {
		return e
	}
	parent := filepath.Dir(destination)
	if p, e := filepath.EvalSymlinks(parent); e != nil || p != parent {
		return errors.New("destination parent must be a real directory")
	}
	if _, e = os.Lstat(destination); !os.IsNotExist(e) {
		return errors.New("destination already exists or unavailable")
	}
	stage, e := os.MkdirTemp(parent, ".zah-extract-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	for _, member := range z.File {
		if e = ctx.Err(); e != nil {
			return e
		}
		name, _ := zipName(member.Name)
		target := filepath.Join(stage, filepath.FromSlash(name))
		if member.Mode().IsDir() {
			if e = os.MkdirAll(target, 0700); e != nil {
				return e
			}
			continue
		}
		if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
			return e
		}
		in, e := member.Open()
		if e != nil {
			return e
		}
		out, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			in.Close()
			return e
		}
		n, copyErr := io.Copy(out, &contextReader{ctx: ctx, r: io.LimitReader(in, int64(member.UncompressedSize64)+1)})
		in.Close()
		syncErr := out.Sync()
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if uint64(n) != member.UncompressedSize64 {
			return errors.New("ZIP member size mismatch")
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if _, e = os.Lstat(destination); !os.IsNotExist(e) {
		return errors.New("destination appeared during extraction")
	}
	return unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
func inspectDeb(ctx context.Context, p string) (Info, error) {
	info := Info{Format: "deb", Platform: "linux", Architecture: "amd64", Entries: []Entry{}, Notes: []string{}}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "dpkg-deb", "--field", p, "Package", "Version", "Architecture")
	cmd.Stderr = io.Discard
	var output boundedOutput
	cmd.Stdout = &output
	e := cmd.Run()
	out := output.Bytes()
	if e != nil || output.exceeded {
		return info, errors.New("cannot inspect deb metadata; dpkg-deb and a valid package are required")
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, ": ")
		if ok {
			values[k] = v
		}
	}
	if values["Architecture"] != "amd64" && values["Architecture"] != "all" {
		return info, errors.New("deb architecture is not amd64/all")
	}
	info.Name = values["Package"]
	info.Version = values["Version"]
	cmd = exec.CommandContext(ctx, "dpkg-deb", "--fsys-tarfile", p)
	cmd.Stderr = io.Discard
	pipe, e := cmd.StdoutPipe()
	if e != nil {
		return info, e
	}
	if e = cmd.Start(); e != nil {
		return info, e
	}
	limit := &io.LimitedReader{R: pipe, N: (512 << 20) + 1}
	entries, err := debEntries(&contextReader{ctx: ctx, r: limit})
	if err != nil {
		cancel()
	}
	if err == nil {
		_, err = io.Copy(io.Discard, limit)
		if limit.N == 0 {
			err = errors.New("deb payload expanded limit exceeded")
		}
	}
	if err != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if err != nil {
		return info, err
	}
	if waitErr != nil {
		return info, errors.New("deb payload inspection failed")
	}
	info.Entries = entries
	if len(entries) == 0 {
		info.Notes = append(info.Notes, "No simple desktop executable was discovered; choose the installed /usr entry in advanced settings.")
	}
	return info, nil
}
func debEntries(reader io.Reader) ([]Entry, error) {
	tr := tar.NewReader(reader)
	executables := map[string]bool{}
	desktop := map[string]string{}
	var total int64
	for n := 0; ; n++ {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if n >= MaxEntries {
			return nil, errors.New("deb entry count exceeded")
		}
		total += h.Size
		if h.Size < 0 || total > 512<<20 {
			return nil, errors.New("deb inspection expanded limit exceeded")
		}
		name := strings.TrimPrefix(h.Name, "./")
		if h.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
			if name == "" || name == "." {
				continue
			}
		}
		if !filepath.IsLocal(name) || path.Clean(name) != name {
			return nil, errors.New("unsafe deb payload path")
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if strings.HasPrefix(name, "usr/") && h.Mode&0111 != 0 {
			executables["/"+name] = true
		}
		if strings.HasPrefix(name, "usr/share/applications/") && strings.HasSuffix(name, ".desktop") && h.Size <= 256<<10 {
			b, e := io.ReadAll(tr)
			if e != nil {
				return nil, e
			}
			section := false
			fields := map[string]string{}
			for _, l := range strings.Split(string(b), "\n") {
				l = strings.TrimSpace(l)
				if strings.HasPrefix(l, "[") {
					section = l == "[Desktop Entry]"
				}
				if section {
					k, v, ok := strings.Cut(l, "=")
					if ok {
						fields[k] = v
					}
				}
			}
			if fields["Type"] == "Application" && fields["Hidden"] != "true" {
				if exe := simpleExec(fields["Exec"]); exe != "" {
					desktop[exe] = fields["Name"]
				}
			}
		}
	}
	entries := []Entry{}
	for exe, label := range desktop {
		if !strings.HasPrefix(exe, "/") {
			exe = "/usr/bin/" + exe
		}
		if executables[exe] {
			entries = append(entries, Entry{exe, label})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	if len(entries) > 256 {
		return nil, errors.New("too many deb desktop entries")
	}
	return entries, nil
}
func simpleExec(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	entry := strings.Trim(fields[0], "\"")
	if strings.ContainsAny(entry, "\"'$`\\;") {
		return ""
	}
	for _, arg := range fields[1:] {
		switch arg {
		case "%f", "%F", "%u", "%U", "%i", "%c", "%k":
		default:
			return ""
		}
	}
	if strings.HasPrefix(entry, "-") {
		return ""
	}
	return entry
}

// Consume output while retaining a strict memory bound. Caller rejects overflow.
type boundedOutput struct {
	bytes.Buffer
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 4096 - b.Len()
	if n > remaining {
		b.exceeded = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
