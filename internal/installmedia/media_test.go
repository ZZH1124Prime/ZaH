package installmedia

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func testPE() []byte {
	b := make([]byte, 128)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[60:], 64)
	copy(b[64:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(b[68:], 0x8664)
	return b
}
func zipFixture(t *testing.T, files map[string][]byte, symlink bool) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "material.zip")
	f, e := os.Create(p)
	if e != nil {
		t.Fatal(e)
	}
	z := zip.NewWriter(f)
	for n, b := range files {
		h := &zip.FileHeader{Name: n, Method: zip.Deflate}
		h.SetMode(0600)
		if symlink && n != "app.exe" {
			h.SetMode(os.ModeSymlink | 0777)
		}
		w, e := z.CreateHeader(h)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write(b); e != nil {
			t.Fatal(e)
		}
	}
	if e = z.Close(); e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestZIPPreservesWholeTreeAndDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	p := zipFixture(t, map[string][]byte{"folder/app.exe": testPE(), "folder/config/data.dat": []byte("kept"), "folder/lib.dll": []byte("dependency")}, false)
	info, e := Inspect(ctx, p)
	if e != nil || len(info.Entries) != 1 || info.Entries[0].Path != "folder/app.exe" {
		t.Fatalf("%+v %v", info, e)
	}
	dest := filepath.Join(t.TempDir(), "portable")
	if e = ExtractZIP(ctx, p, dest); e != nil {
		t.Fatal(e)
	}
	for _, n := range []string{"folder/config/data.dat", "folder/lib.dll"} {
		if _, e = os.Stat(filepath.Join(dest, n)); e != nil {
			t.Fatal(e)
		}
	}
	if e = ExtractZIP(ctx, p, dest); e == nil {
		t.Fatal("overwrote existing directory")
	}
}
func TestZIPRejectsAmbiguousAndUnsafeMembers(t *testing.T) {
	for _, name := range []string{"../escape", "/escape", "C:/escape", "dir\\escape", "CON.txt", "file.", "a/../b", "APP.EXE", "app.exe/child"} {
		t.Run(name, func(t *testing.T) {
			p := zipFixture(t, map[string][]byte{"app.exe": testPE(), name: []byte("bad")}, false)
			dest := filepath.Join(t.TempDir(), "target")
			if e := ExtractZIP(context.Background(), p, dest); e == nil {
				t.Fatal("accepted unsafe archive")
			}
			if _, e := os.Stat(dest); !os.IsNotExist(e) {
				t.Fatal("published rejected archive")
			}
		})
	}
	p := zipFixture(t, map[string][]byte{"app.exe": testPE(), "link": []byte("/etc/passwd")}, true)
	if _, e := Inspect(context.Background(), p); e == nil {
		t.Fatal("accepted link")
	}
}
func TestZIPCancellationAndSymlinkDestination(t *testing.T) {
	p := zipFixture(t, map[string][]byte{"app.exe": testPE()}, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := ExtractZIP(ctx, p, filepath.Join(t.TempDir(), "cancelled")); e == nil {
		t.Fatal("ignored cancellation")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if e := os.Symlink(t.TempDir(), link); e != nil {
		t.Fatal(e)
	}
	if e := ExtractZIP(context.Background(), p, filepath.Join(link, "target")); e == nil {
		t.Fatal("followed destination link")
	}
}
func TestDebSimpleDesktopDiscovery(t *testing.T) {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, name := range []string{"./", "./usr/", "./usr/bin/", "./usr/share/applications/"} {
		if e := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0755}); e != nil {
			t.Fatal(e)
		}
	}
	for _, f := range []struct {
		name, data string
		mode       int64
	}{{"./usr/bin/player", "fixture", 0755}, {"./usr/share/applications/player.desktop", "[Desktop Entry]\nType=Application\nName=Player\nExec=player %U\n", 0644}, {"./usr/share/applications/complex.desktop", "[Desktop Entry]\nType=Application\nName=Unsupported arguments\nExec=player --special\n", 0644}} {
		if e := tw.WriteHeader(&tar.Header{Name: f.name, Typeflag: tar.TypeReg, Mode: f.mode, Size: int64(len(f.data))}); e != nil {
			t.Fatal(e)
		}
		_, _ = tw.Write([]byte(f.data))
	}
	if e := tw.Close(); e != nil {
		t.Fatal(e)
	}
	entries, e := debEntries(&b)
	if e != nil || len(entries) != 1 || entries[0].Path != "/usr/bin/player" {
		t.Fatalf("%+v %v", entries, e)
	}
}
func TestBoundedMetadataAndPortableEXE(t *testing.T) {
	var b boundedOutput
	n, e := b.Write(bytes.Repeat([]byte("x"), 50000))
	if e != nil || n != 50000 || b.Len() != 4096 || !b.exceeded {
		t.Fatal("unbounded metadata")
	}
	p := filepath.Join(t.TempDir(), "app.exe")
	if e = os.WriteFile(p, testPE(), 0600); e != nil {
		t.Fatal(e)
	}
	info, e := Inspect(context.Background(), p)
	if e != nil || info.Format != "exe" {
		t.Fatalf("%+v %v", info, e)
	}
}

func TestWindowsX86AndUnsupportedMachine(t *testing.T) {
	for _, machine := range []uint16{0x014c, 0x8664, 0xaa64} {
		data := testPE()
		binary.LittleEndian.PutUint16(data[68:], machine)
		path := filepath.Join(t.TempDir(), "Original installer.exe")
		if e := os.WriteFile(path, data, 0600); e != nil {
			t.Fatal(e)
		}
		info, e := Inspect(context.Background(), path)
		if machine == 0xaa64 {
			if e == nil {
				t.Fatal("ARM accepted")
			}
			continue
		}
		if e != nil || info.Platform != "windows" {
			t.Fatal(info, e)
		}
		if machine == 0x014c && info.Architecture != "386" {
			t.Fatal(info)
		}
	}
}
