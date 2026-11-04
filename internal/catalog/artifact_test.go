package catalog

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestImportIndependentDedupAndTamper(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "中文 空格.AppImage")
	if e := os.WriteFile(src, []byte("installation bytes"), 0600); e != nil {
		t.Fatal(e)
	}
	s := Store{Root: filepath.Join(dir, "artifacts")}
	a, e := s.Import(ctx, src)
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.Import(ctx, src)
	if e != nil || a.SHA256 != b.SHA256 {
		t.Fatalf("dedup: %v %+v", e, b)
	}
	if a.Source.OriginalName != "中文 空格.AppImage" || a.Format != "unknown" || a.Source.Platform != "unknown" {
		t.Fatal(a)
	}
	if e = os.WriteFile(src, []byte("changed"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Verify(ctx, a.SHA256); e != nil {
		t.Fatal(e)
	}
	c, e := s.Import(ctx, src)
	if e != nil || c.SHA256 == a.SHA256 {
		t.Fatalf("same filename must not merge: %v", e)
	}
	if e = os.Chmod(a.Path, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(a.Path, []byte("tampered"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Verify(ctx, a.SHA256); e == nil {
		t.Fatal("accepted corruption")
	}
	if _, e = s.Import(ctx, src); e != nil {
		t.Fatal(e)
	}
}
func TestLimitsLinksCancellation(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	os.WriteFile(src, []byte("1234"), 0600)
	s := Store{Root: filepath.Join(dir, "objects"), MaxBytes: 3}
	if _, e := s.Import(context.Background(), src); e == nil {
		t.Fatal("limit bypass")
	}
	s.MaxBytes = 8
	link := filepath.Join(dir, "link")
	os.Symlink(src, link)
	if _, e := s.Import(context.Background(), link); e == nil {
		t.Fatal("symlink accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := s.Import(ctx, src); e == nil {
		t.Fatal("cancel ignored")
	}
	entries, _ := os.ReadDir(s.Root)
	if len(entries) != 0 {
		t.Fatalf("partial artifacts leaked: %v", entries)
	}
	if _, e := s.Path("../../outside"); e == nil {
		t.Fatal("unsafe digest accepted")
	}
}

func TestConcurrentImportsDoNotReplace(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	os.WriteFile(src, []byte("same bytes"), 0600)
	s := Store{Root: filepath.Join(dir, "objects")}
	results := make(chan Artifact, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		go func() { a, e := s.Import(context.Background(), src); results <- a; errs <- e }()
	}
	digest := ""
	for i := 0; i < 12; i++ {
		a := <-results
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
		if digest == "" {
			digest = a.SHA256
		}
		if digest != a.SHA256 {
			t.Fatal("concurrent digest mismatch")
		}
	}
	entries, _ := os.ReadDir(s.Root)
	if len(entries) != 1 {
		t.Fatal(entries)
	}
}
