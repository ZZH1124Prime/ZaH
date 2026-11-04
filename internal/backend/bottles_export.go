package backend

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
)

// ExportBottlesSnapshot creates the same bounded regular-file envelope accepted
// by the existing full-backup verifier. Unsupported links/special files fail
// visibly; no host target is followed or implicitly added to the archive.
func (a Adapter) ExportBottlesSnapshot(ctx context.Context, r InstallRequest, identity, destination string) (ev BottlesBackupEvidence, err error) {
	if r.Windows == nil || r.Windows.ResourceIdentity() != identity {
		return ev, errors.New("verified Bottle identity required")
	}
	if _, err = a.ConfirmStopped(ctx, r, identity); err != nil {
		return ev, err
	}
	if !filepath.IsAbs(destination) {
		return ev, errors.New("absolute snapshot destination required")
	}
	if _, e := os.Lstat(destination); !os.IsNotExist(e) {
		return ev, errors.New("snapshot destination exists or is inaccessible")
	}
	expected, err := bottleInventory(ctx, r.Windows.Bottle.Path)
	if err != nil {
		return ev, err
	}
	directories, err := bottleDirectories(ctx, r.Windows.Bottle.Path)
	if err != nil {
		return ev, err
	}
	if len(expected)+len(directories) > maxSnapshotEntries {
		return ev, errors.New("too many Bottle snapshot entries")
	}
	f, err := os.CreateTemp(filepath.Dir(destination), ".zah-bottle-snapshot-")
	if err != nil {
		return ev, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	source, err := os.OpenRoot(r.Windows.Bottle.Path)
	if err != nil {
		return ev, err
	}
	defer source.Close()
	dirKeys := make([]string, 0, len(directories))
	for key := range directories {
		dirKeys = append(dirKeys, key)
	}
	sort.Strings(dirKeys)
	for _, key := range dirKeys {
		if err = ctx.Err(); err != nil {
			return ev, err
		}
		st, e := source.Stat(filepath.FromSlash(key))
		if e != nil || !st.IsDir() || st.Mode().Perm() != directories[key] {
			return ev, errors.New("Bottle directory changed during snapshot")
		}
		if e = tw.WriteHeader(&tar.Header{Name: r.Windows.BottleName + "/" + key, Typeflag: tar.TypeDir, Mode: int64(directories[key])}); e != nil {
			return ev, e
		}
	}
	keys := make([]string, 0, len(expected))
	for key := range expected {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err = ctx.Err(); err != nil {
			return ev, err
		}
		if link := expected[key].link; link != "" {
			actual, e := source.Readlink(filepath.FromSlash(key))
			if e != nil || actual != link || !bottleUserAlias(key, link) {
				return ev, errors.New("Bottle alias changed during snapshot")
			}
			// The real alias destination was already emitted as a directory.
			dir := filepath.Join(filepath.Dir(key), link)
			st, e := source.Stat(dir)
			if e != nil || !st.IsDir() {
				return ev, errors.New("Bottle alias target changed during snapshot")
			}
			if e = tw.WriteHeader(&tar.Header{Name: r.Windows.BottleName + "/" + key, Typeflag: tar.TypeSymlink, Mode: 0777, Linkname: link}); e != nil {
				return ev, e
			}
			continue
		}
		full, e := regularInside(r.Windows.Bottle.Path, filepath.Join(r.Windows.Bottle.Path, filepath.FromSlash(key)))
		if e != nil {
			return ev, e
		}
		_ = full // regularInside verifies the pre-inspected path; os.Root confines the subsequent open.
		input, e := source.Open(filepath.FromSlash(key))
		if e != nil {
			return ev, e
		}
		st, e := input.Stat()
		if e != nil {
			input.Close()
			return ev, e
		}
		want := expected[key]
		if !st.Mode().IsRegular() || st.Size() != want.size {
			input.Close()
			return ev, errors.New("Bottle changed during snapshot")
		}
		h := &tar.Header{Name: r.Windows.BottleName + "/" + key, Mode: int64(st.Mode().Perm()), Size: st.Size(), Typeflag: tar.TypeReg}
		if e = tw.WriteHeader(h); e != nil {
			input.Close()
			return ev, e
		}
		hash := sha256.New()
		n, e := io.Copy(io.MultiWriter(tw, hash), io.LimitReader(contextReader{ctx, input}, want.size+1))
		closeErr := input.Close()
		if e != nil {
			return ev, e
		}
		if closeErr != nil {
			return ev, closeErr
		}
		if n != want.size || hex.EncodeToString(hash.Sum(nil)) != want.sum {
			return ev, errors.New("Bottle bytes changed during snapshot")
		}
	}
	if err = tw.Close(); err != nil {
		return ev, err
	}
	if err = gz.Close(); err != nil {
		return ev, err
	}
	if err = f.Sync(); err != nil {
		return ev, err
	}
	if err = f.Close(); err != nil {
		return ev, err
	}
	digest, err := hashRegular(ctx, f.Name(), MaxSnapshotBytes)
	if err != nil {
		return ev, err
	}
	ev, err = a.VerifyManualBottlesBackup(ctx, r, f.Name(), digest)
	if err != nil {
		return ev, err
	}
	if _, err = a.ConfirmStopped(ctx, r, identity); err != nil {
		return ev, err
	}
	after, err := bottleDirectories(ctx, r.Windows.Bottle.Path)
	if err != nil {
		return ev, err
	}
	if !reflect.DeepEqual(directories, after) {
		return ev, errors.New("Bottle directories changed during snapshot")
	}
	if err = os.Link(f.Name(), destination); err != nil {
		return ev, err
	}
	parent, e := os.Open(filepath.Dir(destination))
	if e != nil {
		return ev, e
	}
	err = parent.Sync()
	parent.Close()
	if err != nil {
		return ev, err
	}
	ev.Path = destination
	ev.State = "zah_full_bottle_verified"
	return ev, nil
}
