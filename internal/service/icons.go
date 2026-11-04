package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/ZZH1124Prime/ZaH/internal/catalog"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

func (s *Service) storeIcon(ctx context.Context, png []byte) (digest string, err error) {
	sum := sha256.Sum256(png)
	digest = hex.EncodeToString(sum[:])
	before, lookupErr := s.Store.Get(ctx, "artifact", digest)
	if lookupErr != nil && !errors.Is(lookupErr, domain.ErrNotFound) {
		return "", lookupErr
	}
	path, err := s.Artifacts().Path(digest)
	if err != nil {
		return "", err
	}
	_, statErr := os.Lstat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return "", statErr
	}
	defer func() {
		if err != nil && os.IsNotExist(statErr) {
			s.rollbackMaterialPublication(context.WithoutCancel(ctx), path, before)
		}
	}()
	f, err := os.CreateTemp(s.Paths.Data, ".icon-*.png")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(png); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	a, err := s.Artifacts().Import(ctx, f.Name())
	if err != nil {
		return "", err
	}
	a.Format = "icon-png"
	a.Source.OriginalName = "version-icon.png"
	a.Source.OriginalPath = ""
	a.Sources = nil
	if errors.Is(lookupErr, domain.ErrNotFound) {
		err = s.save(ctx, "artifact", a.SHA256, a)
	} else {
		var existing catalog.Artifact
		if err = json.Unmarshal(before.Data, &existing); err != nil {
			return "", err
		}
		if existing.SHA256 != a.SHA256 || existing.Size != a.Size || existing.Path != a.Path {
			return "", errors.New("existing icon material facts differ")
		}
		if existing.Availability == "removed" || existing.Availability == "removal_pending" {
			err = s.Store.ImportRecordsAndRecoveredMaterials(ctx, nil, nil, nil, map[string]json.RawMessage{digest: before.Data})
		} else if existing.Availability != "" && existing.Availability != "available" {
			return "", errors.New("icon material availability requires inspection")
		}
	}
	return digest, err
}

// On a failed publication, remove only a newly created byte object whose row is
// still absent or byte-for-byte the pre-import tombstone. Uncertain commits keep
// bytes for inspection; existing material/provenance is never discarded.
func (s *Service) rollbackMaterialPublication(ctx context.Context, path string, before domain.Record) {
	current, checkErr := s.Store.Get(ctx, "artifact", filepath.Base(path))
	if before.ID == "" && errors.Is(checkErr, domain.ErrNotFound) || before.ID != "" && checkErr == nil && bytes.Equal(current.Data, before.Data) {
		_ = os.Remove(path)
	}
}
func (s *Service) setReleaseIcon(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	v, err := get[domain.AppRelease](ctx, s.Store, "release", in.ID)
	if err != nil {
		return nil, err
	}
	png, err := catalog.ReadIcon(in.Path)
	if err != nil {
		return nil, err
	}
	digest, err := s.storeIcon(ctx, png)
	if err != nil {
		return nil, err
	}
	v.IconSHA256 = digest
	refs := []domain.ResourceRef{ref("release", v.ID, "family", v.FamilyID, "family"), ref("release", v.ID, "artifact", digest, "icon")}
	for _, id := range v.Artifacts {
		refs = append(refs, ref("release", v.ID, "artifact", id, "material"))
	}
	return v, s.Store.PutReplacingReferenceRole(ctx, record("release", v.ID, v), "icon", refs)
}
func (s *Service) releaseIcon(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	v, err := get[domain.AppRelease](ctx, s.Store, "release", in.ID)
	if err != nil {
		return nil, err
	}
	if v.IconSHA256 == "" {
		return "", nil
	}
	metadata, err := get[catalog.Artifact](ctx, s.Store, "artifact", v.IconSHA256)
	if err != nil {
		return nil, err
	}
	if metadata.Size > catalog.IconLimit {
		return nil, errors.New("oversized stored icon")
	}
	a, err := s.Artifacts().Verify(ctx, v.IconSHA256)
	if err != nil {
		return nil, err
	}
	if a.Size > catalog.IconLimit {
		return nil, errors.New("oversized stored icon")
	}
	f, err := os.Open(a.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rawPNG, err := io.ReadAll(io.LimitReader(f, catalog.IconLimit+1))
	if err != nil {
		return nil, err
	}
	normalized, err := catalog.NormalizeIcon(rawPNG)
	if err != nil || !bytes.Equal(normalized, rawPNG) {
		return nil, errors.New("stored icon is not normalized PNG")
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(rawPNG), nil
}
