package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ZZH1124Prime/ZaH/internal/catalog"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/installmedia"
	"github.com/ZZH1124Prime/ZaH/internal/recipe"
)

type installConfiguration struct {
	HideWineTray bool   `json:"hide_wine_tray,omitempty"`
	RequestID    string `json:"request_id"`
	ArtifactID   string `json:"artifact_id"`
	FamilyID     string `json:"family_id,omitempty"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	Base         string `json:"base"`
	Entry        string `json:"entry"`
	Mode         string `json:"mode"`
	DPI          int    `json:"dpi,omitempty"`
}

func (s *Service) inspectMaterial(ctx context.Context, id string) (installmedia.Info, error) {
	metadata, e := get[catalog.Artifact](ctx, s.Store, "artifact", id)
	if e != nil {
		return installmedia.Info{}, e
	}
	if metadata.Availability != "" && metadata.Availability != "available" {
		return installmedia.Info{}, errors.New("reimport unavailable installation material before setup")
	}
	a := s.Artifacts()
	if _, e := a.Verify(ctx, id); e != nil {
		return installmedia.Info{}, e
	}
	p, e := a.Path(id)
	if e != nil {
		return installmedia.Info{}, e
	}
	info, e := installmedia.Inspect(ctx, p)
	if e == nil && info.Format == "exe" && metadata.Source.OriginalName != "" {
		if e = installmedia.EXEName(metadata.Source.OriginalName); e != nil {
			return info, e
		}
		info.Entries[0] = installmedia.Entry{Path: metadata.Source.OriginalName, Name: metadata.Source.OriginalName}
	}
	return info, e
}

// configureInstallation registers one complete, internally consistent version
// in one SQLite transaction. The request ID makes an uncertain response safely
// retryable. No container, Wine process, or installation command is started.
func (s *Service) configureInstallation(ctx context.Context, raw json.RawMessage) (result any, err error) {
	newRequest, committing := false, false
	defer func() {
		if err != nil && newRequest && !committing && ctx.Err() == nil {
			err = fmt.Errorf("%w: %w", domain.ErrConfigurationRejected, err)
		}
	}()
	var in installConfiguration
	if e := decode(raw, &in); e != nil {
		return nil, e
	}
	if !safeInstanceID(in.RequestID) {
		return nil, domain.ErrInvalid
	}
	canonical, _ := json.Marshal(in)
	sum := sha256.Sum256(canonical)
	fingerprint := hex.EncodeToString(sum[:])
	if previous, e := get[domain.AppInstance](ctx, s.Store, "instance", in.RequestID); e == nil {
		if previous.Installation == nil || previous.Installation.RequestSHA256 != fingerprint {
			return nil, domain.ErrConflict
		}
		return previous, nil
	} else if !errors.Is(e, domain.ErrNotFound) {
		return nil, e
	}
	newRequest = true
	if in.DPI != 0 && (in.DPI < 96 || in.DPI > 384 || in.Mode == "native") {
		return nil, errors.New("Windows DPI must be between 96 and 384")
	}
	if required(in.Name) != nil || required(in.Version) != nil || len(in.Base) > 512 || len(in.Entry) > 4096 {
		return nil, domain.ErrInvalid
	}
	info, e := s.inspectMaterial(ctx, in.ArtifactID)
	if e != nil {
		return nil, e
	}
	if in.HideWineTray && info.Platform != "windows" {
		return nil, errors.New("tray preference requires Windows material")
	}
	if info.Platform == "linux" && in.Mode != "native" || info.Platform == "windows" && in.Mode != "installer" && in.Mode != "portable" {
		return nil, errors.New("select installer or portable mode explicitly")
	}
	// For Linux deb, advanced manual /usr entries remain possible when desktop
	// discovery cannot describe complex launch arguments. Execution verifies it.
	if info.Platform == "windows" {
		found := false
		for _, v := range info.Entries {
			if v.Path == in.Entry {
				found = true
			}
		}
		if !found {
			return nil, errors.New("select one inspected executable")
		}
	}
	r := recipe.Recipe{SchemaVersion: 1, ID: "guided-" + in.RequestID, Revision: "1", Platform: info.Platform, Architecture: "amd64", Strategy: info.Format, Entry: in.Entry}
	if info.Platform == "linux" {
		r.Image = in.Base
		r.PendingEntry = true
		// Native installation/file tools and desktop Exec handling live in the
		// dedicated container, never installed on the host by this workflow.
		r.Dependencies = []string{"python3", "libglib2.0-bin", "gdebi", "synaptic", "thunar"}
		if info.Format == "appimage" {
			r.Dependencies = append(r.Dependencies, "libopengl0")
			r.Entry = "squashfs-root/AppRun"
		}
	} else {
		r.Runner = in.Base
		r.Strategy = "exe"
		r.Entry = "ZaH application"
		if in.Mode == "installer" {
			r.Entry = "ZaH installer"
		}
	}
	if e = r.Validate(); e != nil {
		return nil, e
	}
	family := domain.AppFamily{ID: in.FamilyID, Name: in.Name}
	records := []domain.Record{}
	if in.FamilyID == "" {
		family.ID = domain.NewID()
		family.StableID = family.ID
		family.Platform = info.Platform
		records = append(records, record("family", family.ID, family))
	} else {
		if family, e = get[domain.AppFamily](ctx, s.Store, "family", in.FamilyID); e != nil {
			return nil, e
		}
	}
	if family.Platform != "" && family.Platform != info.Platform {
		return nil, errors.New("安装包平台与应用平台不同")
	}
	releases, e := s.Store.List(ctx, "release")
	if e != nil {
		return nil, e
	}
	for _, row := range releases {
		var existing domain.AppRelease
		if e := json.Unmarshal(row.Data, &existing); e != nil {
			return nil, e
		}
		if existing.FamilyID == family.ID && existing.Platform == info.Platform && existing.Arch == "amd64" && existing.Version == in.Version {
			return nil, errors.New("这个应用版本已存在，不会覆盖已有数据")
		}
	}
	rel := domain.AppRelease{ID: domain.NewID(), FamilyID: family.ID, Platform: info.Platform, Version: in.Version, Arch: "amd64", Artifacts: []string{in.ArtifactID}}
	env := domain.Environment{ID: domain.NewID(), Backend: "distrobox-docker", Name: "zah-" + in.RequestID, Ownership: "managed", State: "unprepared"}
	if info.Platform == "windows" {
		env.Backend = "bottles-flatpak"
	}
	v := domain.AppInstance{ID: in.RequestID, ReleaseID: rel.ID, EnvironmentID: env.ID, Name: family.Name + " " + in.Version, RecipeRevision: r.Digest(), PrivateData: filepath.Join(s.Paths.Data, "instances", in.RequestID, "home"), State: "unprepared", OriginalArtifact: in.ArtifactID, Drift: "unknown", Installation: &domain.Installation{RequestSHA256: fingerprint, Format: info.Format, Mode: in.Mode, SourceEntry: in.Entry, Stage: "prepared", DPI: in.DPI, HideWineTray: in.HideWineTray}}
	records = append(records, record("release", rel.ID, rel), record("recipe", r.Digest(), r), record("environment", env.ID, env), record("instance", v.ID, v))
	refs := []domain.ResourceRef{ref("release", rel.ID, "family", family.ID, "family"), ref("release", rel.ID, "artifact", in.ArtifactID, "material"), ref("instance", v.ID, "release", rel.ID, "release"), ref("instance", v.ID, "recipe", r.Digest(), "recipe"), ref("instance", v.ID, "environment", env.ID, "environment"), ref("instance", v.ID, "artifact", in.ArtifactID, "original_artifact")}
	committing = true
	return v, s.Store.ImportRecords(ctx, records, refs)
}

func validateInstallation(v *domain.Installation, platform string) error {
	if v.HideWineTray && platform != "windows" {
		return errors.New("invalid non-Windows tray preference")
	}
	if v.DPI != 0 && (platform != "windows" || v.DPI < 96 || v.DPI > 384) {
		return errors.New("invalid per-environment DPI")
	}
	if len(v.RequestSHA256) != 64 {
		return errors.New("invalid installation request digest")
	}
	if _, e := hex.DecodeString(v.RequestSHA256); e != nil {
		return e
	}
	switch v.Stage {
	case "prepared", "installer_ready", "awaiting_completion", "complete":
	default:
		return errors.New("invalid installation stage")
	}
	if platform == "linux" {
		if v.Mode != "native" || (v.Format != "deb" && v.Format != "appimage") {
			return errors.New("invalid Linux installation metadata")
		}
	} else {
		if (v.Mode != "installer" && v.Mode != "portable") || (v.Format != "exe" && v.Format != "zip") || !filepath.IsLocal(v.SourceEntry) || strings.ContainsAny(v.SourceEntry, "\\:\x00\r\n") {
			return errors.New("invalid Windows installation metadata")
		}
	}
	if len(v.SourceEntry) > 4096 {
		return errors.New("installation source entry too long")
	}
	return nil
}
