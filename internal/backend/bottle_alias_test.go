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

func TestDefaultSodaAliasExportVerifyAndRestore(t *testing.T) {
	t.Run("populated", func(t *testing.T) { testDefaultSodaAlias(t, false) })
	t.Run("empty", func(t *testing.T) { testDefaultSodaAlias(t, true) })
}
func testDefaultSodaAlias(t *testing.T, empty bool) {
	r, in, cfg, programs, proof := wineControlFixture(t)
	user := filepath.Join(in.BottlePath, "drive_c", "users", "fixture")
	if e := os.MkdirAll(filepath.Join(in.BottlePath, "drive_c", "empty-application-data", "nested"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.MkdirAll(user, 0700); e != nil {
		t.Fatal(e)
	}
	if !empty {
		if e := os.WriteFile(filepath.Join(user, "saved.txt"), []byte("persisted"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.Symlink("fixture", filepath.Join(filepath.Dir(user), "steamuser")); e != nil {
		t.Fatal(e)
	}
	for _, n := range []string{"bottle.yml", "system.reg", "user.reg", "userdef.reg"} {
		if e := os.WriteFile(filepath.Join(in.BottlePath, n), []byte("metadata"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	reg := []process.Result{{Stdout: cfg}, {Stdout: programs}, {Stdout: "/test/bottles/bottles"}, {Stdout: proof}}
	f := &scriptRunner{replies: append([]process.Result{}, reg...)}
	a := Adapter{f}
	ev, e := a.VerifyManualBottles(context.Background(), r, in)
	if e != nil {
		t.Fatal(e)
	}
	r.Windows = ev.Windows
	// ConfirmStopped, verify backup registration, final ConfirmStopped.
	f.calls = nil
	f.replies = append(append(append(append(append([]process.Result{}, reg...), process.Result{Stdout: proof}), reg...), reg...), process.Result{Stdout: proof})
	archive := filepath.Join(t.TempDir(), "bottle.tar.gz")
	result, e := a.ExportBottlesSnapshot(context.Background(), r, ev.ResourceID, archive)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = InspectBottlesBackupArchive(context.Background(), archive, result.SHA256); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if e = extractBottlePayload(context.Background(), archive, target, result.SHA256); e != nil {
		t.Fatal(e)
	}
	if st, e := os.Stat(filepath.Join(target, "drive_c", "empty-application-data", "nested")); e != nil || !st.IsDir() {
		t.Fatal("ordinary empty application directory was lost", e)
	}
	link, e := os.Readlink(filepath.Join(target, "drive_c/users/steamuser"))
	if e != nil || link != "fixture" {
		t.Fatal(link, e)
	}
	if empty {
		st, e := os.Stat(filepath.Join(target, "drive_c/users/steamuser"))
		if e != nil || !st.IsDir() {
			t.Fatal("empty alias target missing", e)
		}
		return
	}
	b, e := os.ReadFile(filepath.Join(target, "drive_c/users/steamuser/saved.txt"))
	if e != nil || string(b) != "persisted" {
		t.Fatal(string(b), e)
	}
}

func TestBottleAliasRejectsEscapeChainAndWritesThroughAlias(t *testing.T) {
	for _, tc := range []struct{ name, target, extra string }{
		{"absolute", "/tmp", ""}, {"parent", "../fixture", ""}, {"self", "steamuser", ""},
		{"missing", "missing", ""}, {"through", "fixture", "drive_c/users/steamuser/escape"},
		{"target_file", "fixture", "drive_c/users/fixture"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "bad.tar.gz")
			f, e := os.Create(archive)
			if e != nil {
				t.Fatal(e)
			}
			gz := gzip.NewWriter(f)
			tw := tar.NewWriter(gz)
			names := []string{"bottle.yml", "system.reg", "user.reg", "userdef.reg", "drive_c/users/fixture/saved.txt"}
			if tc.extra != "" {
				names = append(names, tc.extra)
			}
			for _, n := range names {
				if e = tw.WriteHeader(&tar.Header{Name: "b/" + n, Typeflag: tar.TypeReg, Mode: 0600, Size: 1}); e != nil {
					t.Fatal(e)
				}
				if _, e = tw.Write([]byte("x")); e != nil {
					t.Fatal(e)
				}
			}
			if e = tw.WriteHeader(&tar.Header{Name: "b/drive_c/users/steamuser", Typeflag: tar.TypeSymlink, Mode: 0777, Linkname: tc.target}); e != nil {
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
			b, e := os.ReadFile(archive)
			if e != nil {
				t.Fatal(e)
			}
			h := sha256.Sum256(b)
			digest := hex.EncodeToString(h[:])
			if _, e = InspectBottlesBackupArchive(context.Background(), archive, digest); e == nil {
				t.Fatal("unsafe alias archive accepted")
			}
			target := filepath.Join(t.TempDir(), "restored")
			if e = extractBottlePayload(context.Background(), archive, target, digest); e == nil {
				t.Fatal("unsafe alias extracted")
			}
			if _, e = os.Stat(target); !os.IsNotExist(e) {
				t.Fatal("failed stage retained", e)
			}
		})
	}
}

func TestBottleInventoryDoesNotFollowUserAliasChain(t *testing.T) {
	root := t.TempDir()
	users := filepath.Join(root, "drive_c/users")
	if e := os.MkdirAll(users, 0700); e != nil {
		t.Fatal(e)
	}
	external := t.TempDir()
	if e := os.WriteFile(filepath.Join(external, "private.txt"), []byte("never archive"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(external, filepath.Join(users, "fixture")); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink("fixture", filepath.Join(users, "steamuser")); e != nil {
		t.Fatal(e)
	}
	if _, e := bottleInventory(context.Background(), root); e == nil {
		t.Fatal("external target chain accepted")
	}
	if e := validateBottleAliases(map[string]string{"drive_c/users/steamuser": "fixture"}, map[string]byte{"drive_c/users/fixture": tar.TypeSymlink, "drive_c/users/fixture/secret": tar.TypeReg}); e == nil {
		t.Fatal("archive link chain accepted")
	}
}
