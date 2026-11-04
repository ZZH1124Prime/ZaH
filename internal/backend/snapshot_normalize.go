package backend

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// normalizeSavedDockerImage handles the hybrid OCI/classic output of recent
// Docker image save. It is used only for our freshly saved, owned snapshot;
// imported archives still pass the strict classic archive validator unchanged.
func normalizeSavedDockerImage(ctx context.Context, filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(contextReader{ctx, f})
	var manifest []dockerManifest
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if h.Name != "manifest.json" {
			continue
		}
		if manifest != nil || h.Typeflag != tar.TypeReg || h.Size > maxSnapshotMetadata {
			return errors.New("invalid saved image manifest")
		}
		b, e := io.ReadAll(tr)
		if e != nil {
			return e
		}
		if e = json.Unmarshal(b, &manifest); e != nil {
			return e
		}
	}
	if len(manifest) != 1 || len(manifest[0].RepoTags) != 0 {
		return errors.New("single tagless saved image required")
	}
	m := manifest[0]
	if !strings.HasPrefix(m.Config, "blobs/sha256/") {
		return nil
	}
	digest := strings.TrimPrefix(m.Config, "blobs/sha256/")
	if !digestPattern.MatchString(digest) || len(m.Layers) == 0 || len(m.Layers) > 4096 {
		return errors.New("invalid saved image references")
	}
	selected := map[string]string{m.Config: digest + ".json"}
	m.Config = digest + ".json"
	for i, source := range m.Layers {
		sum := strings.TrimPrefix(source, "blobs/sha256/")
		if source == sum || !digestPattern.MatchString(sum) || selected[source] != "" {
			return errors.New("invalid saved layer reference")
		}
		target := path.Join(sum, "layer.tar")
		selected[source] = target
		m.Layers[i] = target
	}
	out, err := os.CreateTemp(filepath.Dir(filename), ".zah-classic-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	tw := tar.NewWriter(out)
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	tr = tar.NewReader(contextReader{ctx, f})
	seen := map[string]bool{}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		target, keep := selected[h.Name]
		if !keep {
			continue
		}
		if seen[h.Name] || h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > MaxSnapshotBytes {
			return errors.New("invalid saved image payload")
		}
		seen[h.Name] = true
		if e = tw.WriteHeader(&tar.Header{Name: target, Mode: 0600, Size: h.Size}); e != nil {
			return e
		}
		if _, e = io.Copy(tw, tr); e != nil {
			return e
		}
	}
	if len(seen) != len(selected) {
		return errors.New("saved image payload missing")
	}
	b, err := json.Marshal([]dockerManifest{m})
	if err != nil {
		return err
	}
	if err = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(b))}); err != nil {
		return err
	}
	if _, err = tw.Write(b); err != nil {
		return err
	}
	if err = tw.Close(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	// Validate all retained bytes, config identity and uncompressed layer digests
	// before replacing our private staging file. No layer is host-extracted.
	if _, err = InspectDockerArchive(ctx, out.Name(), MaxSnapshotBytes); err != nil {
		return err
	}
	return os.Rename(out.Name(), filename)
}
