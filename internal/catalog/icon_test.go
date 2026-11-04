package catalog

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func iconPNG(t *testing.T) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 128, 80))
	im.Set(0, 0, color.NRGBA{R: 200, A: 255})
	var b bytes.Buffer
	if e := png.Encode(&b, im); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func TestNormalizeIcon(t *testing.T) {
	raw := iconPNG(t)
	out, e := NormalizeIcon(raw)
	if e != nil {
		t.Fatal(e)
	}
	c, e := png.DecodeConfig(bytes.NewReader(out))
	if e != nil || c.Width != 64 || c.Height != 40 {
		t.Fatalf("%+v %v", c, e)
	}
	again, e := NormalizeIcon(out)
	if e != nil || !bytes.Equal(out, again) {
		t.Fatal("not canonical")
	}
	for _, bad := range [][]byte{[]byte(`<svg onload="bad"/>`), make([]byte, IconInputLimit+1), raw[:12]} {
		if _, e := NormalizeIcon(bad); e == nil {
			t.Fatal("accepted invalid icon")
		}
	}
}
func TestReadIconRejectsSymlink(t *testing.T) {
	p := filepath.Join(t.TempDir(), "icon.png")
	if e := os.WriteFile(p, iconPNG(t), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(p, p+".link"); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadIcon(p + ".link"); e == nil {
		t.Fatal("accepted symlink")
	}
}
func TestArchiveDesktopIcon(t *testing.T) {
	for _, format := range []string{"zip", "tar"} {
		t.Run(format, func(t *testing.T) {
			for _, mode := range []string{"ok", "traversal", "ambiguous", "missing", "symlink"} {
				t.Run(mode, func(t *testing.T) {
					p := filepath.Join(t.TempDir(), "material")
					f, e := os.Create(p)
					if e != nil {
						t.Fatal(e)
					}
					files := map[string][]byte{"app.desktop": []byte("[Desktop Entry]\nName=Demo\nExec=NEVER_EXECUTE\nIcon=app\n"), "icons/app.png": iconPNG(t)}
					switch mode {
					case "traversal":
						files["../bad.png"] = iconPNG(t)
					case "ambiguous":
						files["other/app.png"] = iconPNG(t)
					case "missing":
						delete(files, "icons/app.png")
					}
					if format == "zip" {
						w := zip.NewWriter(f)
						for n, b := range files {
							h := &zip.FileHeader{Name: n}
							h.SetMode(0600)
							if mode == "symlink" && n == "icons/app.png" {
								h.SetMode(os.ModeSymlink | 0777)
							}
							r, e := w.CreateHeader(h)
							if e != nil {
								t.Fatal(e)
							}
							r.Write(b)
						}
						w.Close()
					} else {
						w := tar.NewWriter(f)
						for n, b := range files {
							h := &tar.Header{Name: n, Mode: 0600, Size: int64(len(b))}
							if mode == "symlink" && n == "icons/app.png" {
								h.Typeflag = tar.TypeSymlink
								h.Linkname = "/etc/passwd"
								h.Size = 0
							}
							w.WriteHeader(h)
							if h.Typeflag != tar.TypeSymlink {
								w.Write(b)
							}
						}
						w.Close()
					}
					f.Close()
					out, e := ArchiveIcon(p)
					if mode == "ok" {
						if e != nil || len(out) == 0 {
							t.Fatal(e)
						}
					} else if e == nil {
						t.Fatal("accepted unsafe archive")
					}
				})
			}
		})
	}
}
