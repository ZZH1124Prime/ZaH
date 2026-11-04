package migration

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func snapshot() Snapshot {
	return Snapshot{Manifest: Manifest{Format: "zah.bundle", SchemaVersion: 1, Arch: runtime.GOARCH, CreatedAt: time.Now().UTC(), Resources: []Resource{{ID: "private", Kind: "data", Status: "packaged", Path: "data/实例/file"}}, Omissions: []string{"backend runtime not included"}}, Catalog: json.RawMessage(`{"schema_version":1,"instances":[]}`), Bindings: json.RawMessage(`[]`)}
}
func makeBundle(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	if e := os.WriteFile(src, []byte("private bytes"), 0700); e != nil {
		t.Fatal(e)
	}
	dest := filepath.Join(dir, "test.zahbundle")
	if e := Export(context.Background(), dest, snapshot(), []Entry{{"data/实例/file", src}}, Limits{}); e != nil {
		t.Fatal(e)
	}
	return dir, dest
}
func TestRoundTripIndependent(t *testing.T) {
	dir, archive := makeBundle(t)
	os.Remove(filepath.Join(dir, "source"))
	in, e := Inspect(context.Background(), archive, Limits{})
	if e != nil {
		t.Fatal(e)
	}
	if in.Files != 5 || in.Manifest.OfflineReady {
		t.Fatal(in)
	}
	target := filepath.Join(dir, "restored")
	if _, e = Import(context.Background(), archive, target, Limits{}); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(target, "data/实例/file"))
	if e != nil || string(b) != "private bytes" {
		t.Fatalf("%s %v", b, e)
	}
	st, _ := os.Stat(filepath.Join(target, "data/实例/file"))
	if st.Mode().Perm() != 0700 {
		t.Fatalf("executable bit lost: %v", st.Mode())
	}
	if _, e = Import(context.Background(), archive, target, Limits{}); e == nil {
		t.Fatal("overwrote existing import")
	}
	if e = Export(context.Background(), archive, snapshot(), nil, Limits{}); e == nil {
		t.Fatal("overwrote existing bundle")
	}
}
func TestImportAtomicFailureAndLimits(t *testing.T) {
	dir, archive := makeBundle(t)
	target := filepath.Join(dir, "restore")
	for _, limits := range []Limits{{MaxBytes: 3}, {MaxFiles: 2}, {MaxMetadataBytes: 2}, {Arch: "other-architecture"}} {
		if _, e := Import(context.Background(), archive, target, limits); e == nil {
			t.Fatalf("accepted limit %+v", limits)
		}
		if _, e := os.Stat(target); !os.IsNotExist(e) {
			t.Fatal("failed import left target")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := Import(ctx, archive, target, Limits{}); e == nil {
		t.Fatal("cancel ignored")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".zah-import-") {
			t.Fatal("staging leaked")
		}
	}
}
func hostile(t *testing.T, headers []*tar.Header, bodies [][]byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hostile.zahbundle")
	f, _ := os.Create(p)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for i, h := range headers {
		if e := tw.WriteHeader(h); e != nil {
			t.Fatal(e)
		}
		if len(bodies) > i {
			if _, e := tw.Write(bodies[i]); e != nil {
				t.Fatal(e)
			}
		}
	}
	tw.Close()
	gz.Close()
	f.Close()
	return p
}
func TestRejectHostileMembers(t *testing.T) {
	manifest, _ := json.Marshal(snapshot().Manifest)
	mh := func() *tar.Header {
		return &tar.Header{Name: "manifest.json", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(manifest))}
	}
	tests := []*tar.Header{{Name: "../outside", Mode: 0600}, {Name: "/absolute", Mode: 0600}, {Name: "data/link", Typeflag: tar.TypeSymlink, Linkname: "../../outside"}, {Name: "data/hard", Typeflag: tar.TypeLink, Linkname: "data/file"}, {Name: "data/device", Typeflag: tar.TypeChar}, {Name: "data/privileged", Mode: 04755}, {Name: "data/owner", Mode: 0600, Uid: 1000}, {Name: "manifest.json", Mode: 0600}, {Name: "data/a/../b", Mode: 0600}, {Name: "data\\escape", Mode: 0600}}
	for _, h := range tests {
		t.Run(h.Name, func(t *testing.T) {
			p := hostile(t, []*tar.Header{mh(), h}, [][]byte{manifest})
			target := filepath.Join(t.TempDir(), "new")
			if _, e := Import(context.Background(), p, target, Limits{}); e == nil {
				t.Fatal("accepted hostile member")
			}
			if _, e := os.Stat(target); !os.IsNotExist(e) {
				t.Fatal("target created on failure")
			}
		})
	}
}
func TestTruncationTrailingAndDigestMismatch(t *testing.T) {
	_, p := makeBundle(t)
	original, _ := os.ReadFile(p)
	for name, data := range map[string][]byte{"truncated": original[:len(original)-5], "trailing": append(append([]byte{}, original...), 1)} {
		t.Run(name, func(t *testing.T) {
			q := filepath.Join(t.TempDir(), "bad")
			os.WriteFile(q, data, 0600)
			if _, e := Inspect(context.Background(), q, Limits{}); e == nil {
				t.Fatal("accepted invalid gzip")
			}
		})
	}
	manifest, _ := json.Marshal(snapshot().Manifest)
	headers := []*tar.Header{{Name: "manifest.json", Mode: 0600, Size: int64(len(manifest))}, {Name: "catalog.json", Mode: 0600, Size: 2}, {Name: "bindings.json", Mode: 0600, Size: 2}, {Name: "checksums.json", Mode: 0600, Size: 2}}
	q := hostile(t, headers, [][]byte{manifest, []byte(`{}`), []byte(`[]`), []byte(`{}`)})
	if _, e := Inspect(context.Background(), q, Limits{}); e == nil {
		t.Fatal("accepted invalid checksums")
	}
}

func TestObjectDigestAndMissingResource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	os.WriteFile(src, []byte("bytes"), 0600)
	snap := snapshot()
	snap.Manifest.Resources = nil
	dest := filepath.Join(dir, "bad.zahbundle")
	if e := Export(context.Background(), dest, snap, []Entry{{"objects/sha256/" + strings.Repeat("0", 64), src}}, Limits{}); e == nil {
		t.Fatal("wrong named object accepted")
	}
	if _, e := os.Stat(dest); !os.IsNotExist(e) {
		t.Fatal("failed export published")
	}
	if e := Export(context.Background(), dest, snapshot(), nil, Limits{}); e == nil {
		t.Fatal("missing declared resource accepted")
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".partial") {
			t.Fatal("partial export leaked")
		}
	}
}
