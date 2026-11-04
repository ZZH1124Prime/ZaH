package backend

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// BottlesBackupEvidence describes a verified full upstream bottle archive. Its
// runtime components and excluded host mappings still need target preparation.
type BottlesBackupEvidence struct {
	Path           string   `json:"path"`
	SHA256         string   `json:"sha256"`
	Size           int64    `json:"size"`
	ExpandedBytes  int64    `json:"expanded_bytes"`
	Files          int      `json:"files"`
	BottleIdentity string   `json:"bottle_identity"`
	EntrySHA256    string   `json:"entry_sha256"`
	State          string   `json:"state"`
	Excluded       []string `json:"excluded"`
}

type bottleFile struct {
	sum  string
	size int64
	link string
}

// Soda creates this one relative alias by default. No general-purpose links,
// parent traversal, host paths or link chains are accepted by this adapter.
func bottleUserAlias(name, target string) bool {
	return name == "drive_c/users/steamuser" && target != "" && target != "." && target != ".." && !strings.EqualFold(target, "steamuser") && !strings.ContainsAny(target, "/\\:\x00\r\n")
}
func validateBottleAliases(links map[string]string, members map[string]byte) error {
	for name, target := range links {
		if !bottleUserAlias(name, target) {
			return errors.New("unsupported Bottle alias")
		}
		dest := path.Join(path.Dir(name), target)
		found := members[dest] == tar.TypeDir
		for member, kind := range members {
			if strings.HasPrefix(member, name+"/") {
				return errors.New("archive writes through a Bottle alias")
			}
			if strings.HasPrefix(member, dest+"/") && kind == tar.TypeReg {
				found = true
			}
		}
		if !found || members[dest] == tar.TypeSymlink || members[dest] == tar.TypeReg {
			return errors.New("Bottle alias target is not an archived directory")
		}
	}
	return nil
}

// VerifyManualBottlesBackup validates the upstream GUI's full tar+gzip export
// against the selected bottle's current regular files, without host extraction.
// Caller must hold the environment lock and obtain explicit confirmation that
// all bottle writers have stopped. Presence of registry files alone is not
// treated as proof of a full export. External links are rejected, not followed.
func (a Adapter) VerifyManualBottlesBackup(ctx context.Context, r InstallRequest, archivePath, expectedSHA256 string) (BottlesBackupEvidence, error) {
	var ev BottlesBackupEvidence
	if r.Windows == nil || !digestPattern.MatchString(expectedSHA256) {
		return ev, errors.New("verified bottle registration and expected archive digest required")
	}
	registration, err := a.InspectBottles(ctx, r, r.Windows.ResourceIdentity())
	if err != nil {
		return ev, err
	}
	if err = noLinks(archivePath, false); err != nil {
		return ev, err
	}
	st, err := os.Stat(archivePath)
	if err != nil {
		return ev, err
	}
	if !st.Mode().IsRegular() || st.Size() > MaxSnapshotBytes {
		return ev, errors.New("bottle backup type or size invalid")
	}
	digest, err := hashRegular(ctx, archivePath, MaxSnapshotBytes)
	if err != nil {
		return ev, err
	}
	if digest != expectedSHA256 {
		return ev, errors.New("bottle backup digest mismatch")
	}
	root := r.Windows.Bottle.Path
	expected, err := bottleInventory(ctx, root)
	if err != nil {
		return ev, err
	}
	for _, required := range []string{"bottle.yml", "system.reg", "user.reg", "userdef.reg"} {
		if item, ok := expected[required]; !ok || item.size == 0 {
			return ev, errors.New("bottle is missing required full-backup metadata: " + required)
		}
	}
	relativeEntry, err := filepath.Rel(root, r.Windows.ExecutablePath)
	if err != nil {
		return ev, err
	}
	entryKey := filepath.ToSlash(relativeEntry)
	if expected[entryKey].sum != r.Windows.EntrySHA256 {
		return ev, errors.New("installed entry changed before backup verification")
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return ev, err
	}
	defer f.Close()
	br := bufio.NewReader(contextReader{ctx, f})
	gz, err := gzip.NewReader(br)
	if err != nil {
		return ev, errors.New("Bottles full backup must be tar+gzip")
	}
	defer gz.Close()
	gz.Multistream(false)
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	found := map[string]bottleFile{}
	aliases := map[string]string{}
	members := map[string]byte{}
	top := ""
	var total int64
	for count := 0; ; count++ {
		if count > maxSnapshotEntries {
			return ev, errors.New("bottle backup entry count exceeds limit")
		}
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return ev, e
		}
		key := strings.TrimSuffix(h.Name, "/")
		if key == "" || path.IsAbs(key) || path.Clean(key) != key || key == ".." || strings.HasPrefix(key, "../") || strings.ContainsAny(key, "\\\x00") || seen[key] {
			return ev, errors.New("unsafe or duplicate bottle backup path")
		}
		seen[key] = true
		parts := strings.SplitN(key, "/", 2)
		if top == "" {
			top = parts[0]
		}
		if parts[0] != top {
			return ev, errors.New("bottle backup contains multiple root directories")
		}
		if len(parts) == 2 {
			members[parts[1]] = h.Typeflag
		}
		if h.Typeflag == tar.TypeDir {
			if h.Size != 0 {
				return ev, errors.New("invalid backup directory")
			}
			continue
		}
		if len(parts) != 2 {
			return ev, errors.New("full bottle files must be under one root directory")
		}
		rel := parts[1]
		if h.Typeflag == tar.TypeSymlink && h.Size == 0 && bottleUserAlias(rel, h.Linkname) {
			aliases[rel] = h.Linkname
			found[rel] = bottleFile{link: h.Linkname}
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return ev, errors.New("bottle backup contains an unsupported link or special file: " + rel)
		}
		if h.Mode&07000 != 0 || h.Size < 0 || h.Size > MaxSnapshotBytes-total {
			return ev, errors.New("unsafe or oversized bottle backup entry")
		}
		total += h.Size
		hash := sha256.New()
		if _, e = io.Copy(hash, tr); e != nil {
			return ev, e
		}
		found[rel] = bottleFile{sum: hex.EncodeToString(hash.Sum(nil)), size: h.Size}
	}
	if err = validateBottleAliases(aliases, members); err != nil {
		return ev, err
	}
	// Finish the gzip checksum and reject concatenated members/trailing payload.
	padding := make([]byte, 32<<10)
	for {
		n, e := gz.Read(padding)
		for _, b := range padding[:n] {
			if b != 0 {
				return ev, errors.New("trailing bottle tar content")
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return ev, e
		}
	}
	if _, e := br.Peek(1); e != io.EOF {
		return ev, errors.New("trailing gzip member or data")
	}
	if len(found) != len(expected) {
		return ev, errors.New("backup file set differs from full selected bottle; configuration-only or partial exports are insufficient")
	}
	for key, want := range expected {
		got, ok := found[key]
		if !ok || got != want {
			return ev, errors.New("backup content differs from selected bottle: " + key)
		}
	}
	after, err := bottleInventory(ctx, root)
	if err != nil {
		return ev, err
	}
	if !sameInventory(expected, after) {
		return ev, errors.New("bottle changed during verification; stop writers and export again")
	}
	now, err := os.Stat(archivePath)
	if err != nil || now.Size() != st.Size() || !now.ModTime().Equal(st.ModTime()) {
		return ev, errors.New("backup changed during verification")
	}
	ev = BottlesBackupEvidence{Path: archivePath, SHA256: digest, Size: st.Size(), ExpandedBytes: total, Files: len(found), BottleIdentity: registration.ResourceID, EntrySHA256: r.Windows.EntrySHA256, State: "upstream_full_bottle_verified", Excluded: []string{"dosdevices mappings (rebind on target)", "lsfg-vk files excluded by upstream export", "runner, DXVK/VKD3D and Flatpak runtime outside bottle", "external shared data, host credentials and live session"}}
	return ev, nil
}
func bottleInventory(ctx context.Context, root string) (map[string]bottleFile, error) {
	out := map[string]bottleFile{}
	var total int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if p == root {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		key := filepath.ToSlash(rel)
		// Match the documented upstream full-export exclusions, recording them in
		// evidence instead of pretending the corresponding mappings are portable.
		if excludedBottleMember(key) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, e := os.Readlink(p)
			if e != nil || !bottleUserAlias(key, target) {
				return errors.New("unsupported Bottle link: " + key)
			}
			dest := filepath.Join(filepath.Dir(p), target)
			canonical, e := filepath.EvalSymlinks(dest)
			if e != nil || canonical != dest {
				return errors.New("Bottle alias target traverses links")
			}
			st, e := os.Stat(dest)
			if e != nil || !st.IsDir() {
				return errors.New("Bottle alias target must be an internal directory")
			}
			out[key] = bottleFile{link: target}
			return nil
		}
		if !d.Type().IsRegular() {
			return errors.New("bottle contains unsupported external/internal link or special file: " + key)
		}
		if len(out) >= maxSnapshotEntries {
			return errors.New("bottle file count exceeds limit")
		}
		st, e := d.Info()
		if e != nil {
			return e
		}
		if st.Size() > MaxSnapshotBytes-total {
			return errors.New("bottle data exceeds size limit")
		}
		total += st.Size()
		sum, e := hashRegular(ctx, p, MaxSnapshotBytes)
		if e != nil {
			return e
		}
		after, e := os.Stat(p)
		if e != nil || st.Size() != after.Size() || !st.ModTime().Equal(after.ModTime()) {
			return errors.New("bottle file changed during inspection")
		}
		out[key] = bottleFile{sum: sum, size: st.Size()}
		return nil
	})
	return out, err
}

func excludedBottleMember(key string) bool {
	if strings.Contains(key, "dosdevices") {
		return true
	}
	for _, part := range strings.Split(key, "/") {
		if part == "lsfg-vk" {
			return true
		}
	}
	return false
}

// Preserve ordinary empty directories too. WalkDir never follows the Wine
// profile alias or host mappings; only real directories enter this inventory.
func bottleDirectories(ctx context.Context, root string) (map[string]fs.FileMode, error) {
	directories := map[string]fs.FileMode{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if excludedBottleMember(key) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if len(directories) >= maxSnapshotEntries {
			return errors.New("too many Bottle directories")
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		if !st.IsDir() {
			return errors.New("Bottle directory changed during inventory")
		}
		directories[key] = st.Mode().Perm()
		return nil
	})
	return directories, err
}
func sameInventory(a, b map[string]bottleFile) bool {
	if len(a) != len(b) {
		return false
	}
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		x, y := a[k], b[k]
		if x != y {
			return false
		}
	}
	return true
}

// InspectBottlesBackupArchive validates a portable full-backup envelope without
// a source bottle. It proves byte integrity and safe container structure, not
// application completeness or target runtime compatibility. GUI import remains
// explicit and is followed by VerifyManualBottles on the target.
func InspectBottlesBackupArchive(ctx context.Context, p, expectedSHA256 string) (BottlesBackupEvidence, error) {
	var ev BottlesBackupEvidence
	if !digestPattern.MatchString(expectedSHA256) {
		return ev, errors.New("expected backup digest required")
	}
	if err := noLinks(p, false); err != nil {
		return ev, err
	}
	sum, err := hashRegular(ctx, p, MaxSnapshotBytes)
	if err != nil {
		return ev, err
	}
	if sum != expectedSHA256 {
		return ev, errors.New("backup digest mismatch")
	}
	st, err := os.Stat(p)
	if err != nil {
		return ev, err
	}
	f, err := os.Open(p)
	if err != nil {
		return ev, err
	}
	defer f.Close()
	br := bufio.NewReader(contextReader{ctx, f})
	gz, err := gzip.NewReader(br)
	if err != nil {
		return ev, errors.New("full bottle backup must be gzip")
	}
	defer gz.Close()
	gz.Multistream(false)
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	required := map[string]bool{"bottle.yml": false, "system.reg": false, "user.reg": false, "userdef.reg": false}
	top := ""
	var total int64
	files := 0
	driveFiles := 0
	aliases := map[string]string{}
	members := map[string]byte{}
	for n := 0; ; n++ {
		if n > maxSnapshotEntries {
			return ev, errors.New("backup file count exceeds limit")
		}
		hdr, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return ev, e
		}
		key := strings.TrimSuffix(hdr.Name, "/")
		if key == "" || path.IsAbs(key) || path.Clean(key) != key || key == ".." || strings.HasPrefix(key, "../") || strings.ContainsAny(key, "\\\x00") || seen[key] {
			return ev, errors.New("unsafe or duplicate bottle archive path")
		}
		seen[key] = true
		parts := strings.SplitN(key, "/", 2)
		if top == "" {
			top = parts[0]
		}
		if top != parts[0] {
			return ev, errors.New("multiple bottle archive roots")
		}
		if len(parts) == 2 {
			members[parts[1]] = hdr.Typeflag
		}
		if hdr.Typeflag == tar.TypeDir {
			if hdr.Size != 0 {
				return ev, errors.New("invalid backup directory")
			}
			continue
		}
		if len(parts) == 2 && hdr.Typeflag == tar.TypeSymlink && hdr.Size == 0 && bottleUserAlias(parts[1], hdr.Linkname) {
			aliases[parts[1]] = hdr.Linkname
			files++
			continue
		}
		if len(parts) != 2 || hdr.Typeflag != tar.TypeReg || hdr.Mode&07000 != 0 || hdr.Size < 0 || hdr.Size > MaxSnapshotBytes-total {
			return ev, errors.New("bottle archive contains unsupported link, special file or excessive data")
		}
		rel := parts[1]
		if _, ok := required[rel]; ok {
			required[rel] = hdr.Size > 0
		}
		if strings.HasPrefix(rel, "drive_c/") {
			driveFiles++
		}
		total += hdr.Size
		files++
		if _, e = io.Copy(io.Discard, tr); e != nil {
			return ev, e
		}
	}
	if err = validateBottleAliases(aliases, members); err != nil {
		return ev, err
	}
	padding := make([]byte, 32<<10)
	for {
		n, e := gz.Read(padding)
		for _, b := range padding[:n] {
			if b != 0 {
				return ev, errors.New("trailing tar content")
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return ev, e
		}
	}
	if _, e := br.Peek(1); e != io.EOF {
		return ev, errors.New("trailing gzip data")
	}
	for name, ok := range required {
		if !ok {
			return ev, errors.New("full bottle metadata missing: " + name)
		}
	}
	if driveFiles == 0 {
		return ev, errors.New("configuration-only backup is insufficient")
	}
	now, err := os.Stat(p)
	if err != nil || now.Size() != st.Size() || !now.ModTime().Equal(st.ModTime()) {
		return ev, errors.New("backup changed while inspecting")
	}
	return BottlesBackupEvidence{Path: p, SHA256: sum, Size: st.Size(), ExpandedBytes: total, Files: files, State: "archive_structure_verified_target_validation_required", Excluded: []string{"target runner/components/permissions and installed entry are unverified"}}, nil
}
