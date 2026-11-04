package backend

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/process"
)

func fullBottleArchive(t *testing.T, in ManualBottlesInput, omit string) (string, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bottle.tar.gz")
	f, e := os.Create(p)
	if e != nil {
		t.Fatal(e)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	e = filepath.WalkDir(in.BottlePath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(in.BottlePath, path)
		if err != nil {
			return err
		}
		if rel == omit {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err = tw.WriteHeader(&tar.Header{Name: "bottle/" + filepath.ToSlash(rel), Mode: 0600, Size: int64(len(b))}); err != nil {
			return err
		}
		_, err = tw.Write(b)
		return err
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = tw.Close(); e != nil {
		t.Fatal(e)
	}
	if e = gz.Close(); e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	h := sha256.Sum256(b)
	return p, hex.EncodeToString(h[:])
}
func TestManualFullBottleBackupRequiresEveryFile(t *testing.T) {
	r, in, cfg, programs := windowsRequest(t)
	for _, name := range []string{"bottle.yml", "system.reg", "user.reg", "userdef.reg"} {
		if e := os.WriteFile(filepath.Join(in.BottlePath, name), []byte("required metadata"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	a := Adapter{&scriptRunner{replies: []process.Result{{Stdout: cfg}, {Stdout: programs}}}}
	ev, e := a.VerifyManualBottles(context.Background(), r, in)
	if e != nil {
		t.Fatal(e)
	}
	r.Windows = ev.Windows
	archive, sum := fullBottleArchive(t, in, "")
	a.Runner = &scriptRunner{replies: []process.Result{{Stdout: cfg}, {Stdout: programs}}}
	proof, e := a.VerifyManualBottlesBackup(context.Background(), r, archive, sum)
	if e != nil {
		t.Fatal(e)
	}
	if proof.State != "upstream_full_bottle_verified" || proof.Files != 5 {
		t.Fatalf("%+v", proof)
	}
	if _, e = InspectBottlesBackupArchive(context.Background(), archive, sum); e != nil {
		t.Fatal(e)
	}
	incomplete, sum := fullBottleArchive(t, in, "user.reg")
	a.Runner = &scriptRunner{replies: []process.Result{{Stdout: cfg}, {Stdout: programs}}}
	if _, e = a.VerifyManualBottlesBackup(context.Background(), r, incomplete, sum); e == nil {
		t.Fatal("accepted partial source backup")
	}
	if _, e = InspectBottlesBackupArchive(context.Background(), incomplete, sum); e == nil {
		t.Fatal("accepted missing registry")
	}
}
func TestBottlesArchiveRejectsUnsafeLink(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.tar.gz")
	f, e := os.Create(p)
	if e != nil {
		t.Fatal(e)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "bottle/drive_c/escape", Typeflag: tar.TypeSymlink, Linkname: "/etc"})
	_ = tw.Close()
	_ = gz.Close()
	_ = f.Close()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	h := sha256.Sum256(b)
	if _, e = InspectBottlesBackupArchive(context.Background(), p, hex.EncodeToString(h[:])); e == nil {
		t.Fatal("accepted archive symlink")
	}
}
