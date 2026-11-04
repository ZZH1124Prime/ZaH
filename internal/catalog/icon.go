package catalog

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"image"
	"image/png"
	"io"
	"path"
	"strings"
)

const IconInputLimit = 1 << 20
const IconLimit = 20 << 10

// NormalizeIcon only accepts small raster PNGs. Decode/re-encode strips ancillary
// metadata and produces a bounded 64px preview; no SVG, URLs or executables.
func NormalizeIcon(raw []byte) ([]byte, error) {
	if len(raw) > IconInputLimit {
		return nil, errors.New("icon exceeds 1 MiB")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 1024 || cfg.Height > 1024 {
		return nil, errors.New("icon dimensions must be 1–1024 pixels")
	}
	src, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	w, h := cfg.Width, cfg.Height
	if w > 64 || h > 64 {
		if w >= h {
			h = h * 64 / w
			w = 64
		} else {
			w = w * 64 / h
			h = 64
		}
		if w < 1 {
			w = 1
		}
		if h < 1 {
			h = 1
		}
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(x, y, src.At(x*cfg.Width/w, y*cfg.Height/h))
		}
	}
	var out bytes.Buffer
	if err = png.Encode(&out, dst); err != nil {
		return nil, err
	}
	if out.Len() > IconLimit {
		return nil, errors.New("normalized icon exceeds limit")
	}
	return out.Bytes(), nil
}

func ReadIcon(filename string) ([]byte, error) {
	f, err := openRegular(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, IconInputLimit+1))
	if err != nil {
		return nil, err
	}
	return NormalizeIcon(raw)
}

// ArchiveIcon reads bounded zip/tar/tar.gz members without extracting anything.
// Only one desktop entry and its unambiguous PNG basename are accepted. Other
// formats or ambiguous layouts simply leave the release's fallback icon intact.
func ArchiveIcon(filename string) ([]byte, error) {
	f, err := openRegular(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > 32<<20 {
		return nil, errors.New("automatic icon scan exceeds 32 MiB")
	}
	files := map[string][]byte{}
	total := int64(0)
	count := 0
	add := func(name string, size int64, r io.Reader) error {
		count++
		if count > 512 || size < 0 {
			return errors.New("archive icon scan limit")
		}
		total += size
		if total > 32<<20 {
			return errors.New("archive expanded scan limit")
		}
		if strings.Contains(name, "\\") || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
			return errors.New("unsafe icon archive path")
		}
		if !strings.HasSuffix(name, ".desktop") && !strings.HasSuffix(strings.ToLower(name), ".png") {
			return nil
		}
		if size > IconInputLimit {
			return nil
		}
		if _, ok := files[name]; ok {
			return errors.New("duplicate icon member")
		}
		b, e := io.ReadAll(io.LimitReader(r, IconInputLimit+1))
		if e != nil {
			return e
		}
		if int64(len(b)) != size {
			return errors.New("invalid member size")
		}
		files[name] = b
		return nil
	}
	if zr, e := zip.NewReader(f, st.Size()); e == nil {
		for _, z := range zr.File {
			if z.FileInfo().IsDir() {
				continue
			}
			if !z.Mode().IsRegular() {
				return nil, errors.New("nonregular icon archive member")
			}
			if z.UncompressedSize64 > 32<<20 {
				return nil, errors.New("oversized member")
			}
			r, e := z.Open()
			if e != nil {
				return nil, e
			}
			e = add(z.Name, int64(z.UncompressedSize64), r)
			r.Close()
			if e != nil {
				return nil, e
			}
		}
	} else {
		if _, err = f.Seek(0, 0); err != nil {
			return nil, err
		}
		var reader io.Reader = f
		gz, e := gzip.NewReader(f)
		if e == nil {
			defer gz.Close()
			reader = gz
		} else {
			if _, err = f.Seek(0, 0); err != nil {
				return nil, err
			}
		}
		tr := tar.NewReader(io.LimitReader(reader, (32<<20)+1))
		for {
			h, e := tr.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				return nil, e
			}
			if h.Typeflag == tar.TypeDir {
				continue
			}
			if h.Typeflag != tar.TypeReg {
				return nil, errors.New("nonregular icon archive member")
			}
			if e = add(h.Name, h.Size, tr); e != nil {
				return nil, e
			}
		}
	}
	var icon string
	desktops := 0
	for name, b := range files {
		if !strings.HasSuffix(name, ".desktop") {
			continue
		}
		desktops++
		section := ""
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "[") {
				section = line
			}
			if section == "[Desktop Entry]" && strings.HasPrefix(line, "Icon=") {
				if icon != "" {
					return nil, errors.New("ambiguous desktop icon")
				}
				icon = strings.TrimSpace(strings.TrimPrefix(line, "Icon="))
			}
		}
	}
	if desktops != 1 || icon == "" || strings.ContainsAny(icon, "/\\\x00") || icon == "." || icon == ".." {
		return nil, errors.New("no unambiguous desktop PNG icon")
	}
	if !strings.HasSuffix(strings.ToLower(icon), ".png") {
		icon += ".png"
	}
	var selected []byte
	for name, b := range files {
		if path.Base(name) == icon {
			if selected != nil {
				return nil, errors.New("ambiguous PNG icon")
			}
			selected = b
		}
	}
	return NormalizeIcon(selected)
}
