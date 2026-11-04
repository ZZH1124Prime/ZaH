package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/catalog"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/migration"
	"github.com/ZZH1124Prime/ZaH/internal/recipe"
)

const catalogBundleFormat = "zah.catalog-materials"

type catalogSnapshot struct {
	Format        string               `json:"format"`
	SchemaVersion int                  `json:"schema_version"`
	Records       []domain.Record      `json:"records"`
	References    []domain.ResourceRef `json:"references"`
}

// CatalogRestoreResult reports registration only; it never asserts runtime recovery.
type CatalogRestoreResult struct {
	IDMap           map[string]string `json:"id_map"`
	Records         int               `json:"records"`
	SkippedVersions int               `json:"skipped_versions"`
	State           string            `json:"state"`
	Omissions       []string          `json:"omissions"`
}

// ExportCatalogBundle exports catalog records and archived installation material.
// Caller holds the global modification lock. This is NOT a runtime/data backup.
func (s *Service) ExportCatalogBundle(ctx context.Context, path string) (migration.Inspection, error) {
	records, refs, err := s.Store.Snapshot(ctx)
	if err != nil {
		return migration.Inspection{}, err
	}
	snap := catalogSnapshot{Format: catalogBundleFormat, SchemaVersion: 1, Records: records, References: refs}
	if err = validateCatalogSnapshot(snap); err != nil {
		return migration.Inspection{}, err
	}
	manifest := migration.Manifest{Omissions: []string{"runtime environments, container images and bottle backups are NOT included", "private application data and host shared directories are NOT included", "running sessions, credentials, keyrings and login state are NOT included", "execution plans and jobs are not portable"}, Requirements: []string{"reinstall each application and rebind directories on target; this archive does not restore working applications"}, OfflineReady: false}
	entries := []migration.Entry{}
	for _, r := range records {
		switch r.Kind {
		case "artifact":
			var material catalog.Artifact
			if e := json.Unmarshal(r.Data, &material); e != nil {
				return migration.Inspection{}, e
			}
			if material.Availability == "removed" || material.Availability == "removal_pending" {
				return migration.Inspection{}, fmt.Errorf("material %s is unavailable; reimport it before a complete catalog/materials export", r.ID)
			}
			a, e := s.Artifacts().Verify(ctx, r.ID)
			if e != nil {
				return migration.Inspection{}, e
			}
			name := "objects/sha256/" + r.ID
			entries = append(entries, migration.Entry{Name: name, Path: a.Path})
			manifest.Resources = append(manifest.Resources, migration.Resource{ID: "artifact:" + r.ID, Kind: "artifact", Status: "packaged", Path: name})
		case "instance":
			manifest.Resources = append(manifest.Resources, migration.Resource{ID: "instance:" + r.ID, Kind: "instance", Status: "target_required", Reason: "catalog only; private data and installed runtime excluded"})
		case "binding":
			manifest.Resources = append(manifest.Resources, migration.Resource{ID: "binding:" + r.ID, Kind: "binding", Status: "rebind", Reason: "host directory content excluded"})
		}
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return migration.Inspection{}, err
	}
	err = migration.Export(ctx, path, migration.Snapshot{Manifest: manifest, Catalog: data, Bindings: json.RawMessage(`{"state":"rebind_all"}`)}, entries, migration.Limits{})
	if err != nil {
		return migration.Inspection{}, err
	}
	return migration.Inspect(ctx, path, migration.Limits{})
}

// RestoreCatalogBundle imports semantic identities and materials only. It never
// invokes a backend, trusts old readiness, writes an old host path or replays a job.
// Caller holds the global modification lock, including artifact publication/rollback.
func (s *Service) RestoreCatalogBundle(ctx context.Context, path string, options ...RestoreOptions) (CatalogRestoreResult, error) {
	return s.restoreBundle(ctx, path, catalogBundleFormat, nil, options...)
}

// restoreBundle is shared by explicit bundle formats. A backend preparation
// callback may write only new target resources and must return rollback cleanup.
// It runs after validation/remapping, before the catalog commit, under the worker lock.
func (s *Service) restoreBundle(ctx context.Context, path, format string, prepare func(stage string, inspection migration.Inspection, records []domain.Record, idMap map[string]string) (cleanup func(), err error), options ...RestoreOptions) (result CatalogRestoreResult, err error) {
	result = CatalogRestoreResult{IDMap: map[string]string{}, State: "needs_reinstall_and_rebind"}
	parent := filepath.Join(s.Paths.Data, "imports")
	if err = os.MkdirAll(parent, 0700); err != nil {
		return result, err
	}
	stage := filepath.Join(parent, "catalog-"+domain.NewID())
	inspection, err := migration.Import(ctx, path, stage, migration.Limits{})
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(stage)
	if err = checkedBundleIdentity(ctx, inspection); err != nil {
		return result, err
	}
	var snap catalogSnapshot
	if err = decode(inspection.Catalog, &snap); err != nil {
		return result, err
	}
	if snap.Format != format || snap.SchemaVersion != 1 {
		return result, errors.New("bundle is not a supported catalog/materials snapshot")
	}
	semantic := snap
	semantic.Format = catalogBundleFormat // exact wire format was checked above; reuse entity validation
	if err = validateCatalogSnapshot(semantic); err != nil {
		return result, err
	}
	aliases := map[string]string{}
	if len(options) > 1 {
		return result, domain.ErrInvalid
	}
	if len(options) == 1 && options[0].Merge {
		snap, aliases, result.SkippedVersions, err = s.mergeSelection(ctx, snap)
		if err != nil {
			return result, err
		}
	}
	result.Omissions = inspection.Manifest.Omissions
	for name := range inspection.Checksums {
		if prepare == nil && strings.Contains(name, "/") && !strings.HasPrefix(name, "objects/sha256/") {
			return result, errors.New("catalog/materials restore rejects environment and user-data payloads")
		}
	}
	old := map[string]domain.Record{}
	for _, r := range snap.Records {
		key := r.Kind + ":" + r.ID
		if r.ID == "" || old[key].ID != "" {
			return result, errors.New("missing or duplicate catalog identity")
		}
		switch r.Kind {
		case "family", "release", "instance", "environment", "binding", "capability":
			result.IDMap[key] = domain.NewID()
			if r.Kind == "family" && aliases[r.ID] != "" {
				result.IDMap[key] = aliases[r.ID]
			}
		case "recipe", "artifact":
			if !catalog.ValidDigest(r.ID) {
				return result, errors.New("invalid material/recipe digest")
			}
			result.IDMap[key] = r.ID
		default:
			return result, fmt.Errorf("nonportable record kind %q", r.Kind)
		}
		old[key] = r
	}
	mapped := func(kind, id string) (string, error) {
		if id == "" {
			return "", domain.ErrInvalid
		}
		v, ok := result.IDMap[kind+":"+id]
		if !ok {
			return "", fmt.Errorf("missing %s dependency", kind)
		}
		return v, nil
	}
	records := []domain.Record{}
	refs := []domain.ResourceRef{}
	created := map[string]domain.Record{}
	committed := false
	defer func() {
		if !committed {
			for path, before := range created {
				s.rollbackMaterialPublication(context.WithoutCancel(ctx), path, before)
			}
		}
	}()
	reused := map[string]bool{}
	mergedSources := map[string][]json.RawMessage{}
	recovered := map[string]json.RawMessage{}
	for _, r := range snap.Records {
		if r.Kind == "family" && aliases[r.ID] != "" {
			reused["family:"+aliases[r.ID]] = true
			continue
		}
		originalID := r.ID
		r.ID = result.IDMap[r.Kind+":"+r.ID]
		r.CreatedAt = time.Now().UTC()
		r.UpdatedAt = r.CreatedAt
		var value any
		switch r.Kind {
		case "artifact":
			var a catalog.Artifact
			if err = decode(r.Data, &a); err != nil {
				return result, err
			}
			if a.SHA256 != originalID {
				return result, errors.New("artifact identity differs from digest")
			}
			member := "objects/sha256/" + originalID
			sum, ok := inspection.Checksums[member]
			if !ok || sum.SHA256 != originalID || sum.Size != a.Size {
				return result, errors.New("missing or inconsistent archived material")
			}
			if existingRecord, e := s.Store.Get(ctx, "artifact", originalID); e == nil {
				var existing catalog.Artifact
				if e = json.Unmarshal(existingRecord.Data, &existing); e != nil {
					return result, e
				}
				expectedPath, e := s.Artifacts().Path(originalID)
				if e != nil {
					return result, e
				}
				if existing.SHA256 != originalID || existing.Size != a.Size || existing.Path != expectedPath {
					return result, errors.New("existing material facts differ from archived object")
				}
				if existing.Availability == "removed" || existing.Availability == "removal_pending" {
					_, statErr := os.Lstat(expectedPath)
					if statErr != nil && !os.IsNotExist(statErr) {
						return result, statErr
					}
					if os.IsNotExist(statErr) {
						created[expectedPath] = existingRecord
					}
					copied, copyErr := s.Artifacts().Import(ctx, filepath.Join(stage, filepath.FromSlash(member)))
					if copyErr != nil {
						return result, copyErr
					}
					if copied.SHA256 != originalID || copied.Size != a.Size {
						return result, errors.New("recovered material differs from archived object")
					}
					recovered[originalID] = append(json.RawMessage(nil), existingRecord.Data...)
				} else {
					if existing.Availability != "" && existing.Availability != "available" {
						return result, errors.New("existing material availability requires inspection")
					}
					if _, e = s.Artifacts().Verify(ctx, originalID); e != nil {
						return result, e
					}
				}
				sourceList := append([]catalog.Source{a.Source}, a.Sources...)
				for _, source := range sourceList {
					raw, e := json.Marshal(source)
					if e != nil {
						return result, e
					}
					mergedSources[r.ID] = append(mergedSources[r.ID], raw)
				}
				reused[r.Kind+":"+r.ID] = true
				continue
			} else if !errors.Is(e, domain.ErrNotFound) {
				return result, e
			}
			dest, e := s.Artifacts().Path(originalID)
			if e != nil {
				return result, e
			}
			_, statErr := os.Lstat(dest)
			if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
				return result, statErr
			}
			if errors.Is(statErr, os.ErrNotExist) {
				created[dest] = domain.Record{}
			}
			copied, e := s.Artifacts().Import(ctx, filepath.Join(stage, filepath.FromSlash(member)))
			if e != nil {
				return result, e
			}
			a.Path = copied.Path
			a.Format = copied.Format
			value = a
		case "recipe":
			v, e := recipe.Decode(bytes.NewReader(r.Data))
			if e != nil || v.Digest() != originalID {
				return result, errors.New("invalid recipe or recipe digest")
			}
			if existing, e := s.Store.Get(ctx, "recipe", originalID); e == nil {
				var local recipe.Recipe
				if e = json.Unmarshal(existing.Data, &local); e != nil || local.Digest() != originalID {
					return result, errors.New("existing recipe integrity mismatch")
				}
				reused[r.Kind+":"+r.ID] = true
				continue
			} else if !errors.Is(e, domain.ErrNotFound) {
				return result, e
			}
			value = v
		case "family":
			var v domain.AppFamily
			if err = decode(r.Data, &v); err != nil {
				return result, err
			}
			if v.StableID == "" {
				v.StableID = originalID
			}
			v.ID = r.ID
			if v.DefaultInstanceID != "" {
				v.DefaultInstanceID, err = mapped("instance", v.DefaultInstanceID)
				if err != nil {
					return result, err
				}
				refs = append(refs, ref("family", v.ID, "instance", v.DefaultInstanceID, "default"))
			}
			value = v
		case "release":
			var v domain.AppRelease
			if err = decode(r.Data, &v); err != nil {
				return result, err
			}
			v.ID = r.ID
			if v.IconSHA256 != "" {
				if v.IconSHA256, err = mapped("artifact", v.IconSHA256); err != nil {
					return result, err
				}
				refs = append(refs, ref("release", v.ID, "artifact", v.IconSHA256, "icon"))
			}
			if v.FamilyID, err = mapped("family", v.FamilyID); err != nil {
				return result, err
			}
			for i, a := range v.Artifacts {
				if v.Artifacts[i], err = mapped("artifact", a); err != nil {
					return result, err
				}
				refs = append(refs, ref("release", v.ID, "artifact", v.Artifacts[i], "material"))
			}
			refs = append(refs, ref("release", v.ID, "family", v.FamilyID, "family"))
			value = v
		case "instance":
			var v domain.AppInstance
			if err = decode(r.Data, &v); err != nil {
				return result, err
			}
			v.ID = r.ID
			for _, pair := range []struct {
				k string
				p *string
			}{{"release", &v.ReleaseID}, {"environment", &v.EnvironmentID}, {"recipe", &v.RecipeRevision}, {"artifact", &v.OriginalArtifact}} {
				if *pair.p, err = mapped(pair.k, *pair.p); err != nil {
					return result, err
				}
				refs = append(refs, ref("instance", v.ID, pair.k, *pair.p, pair.k))
			}
			v.State = "unprepared"
			v.RestoreRoot = nil
			v.Entry = nil
			v.PrivateData = filepath.Join(s.Paths.Data, "instances", v.ID, "home")
			v.LastUsed = nil
			v.Drift = "unknown"
			v.ObservedInstalledVersion = ""
			var trustedRecipe recipe.Recipe
			if e := json.Unmarshal(old["recipe:"+v.RecipeRevision].Data, &trustedRecipe); e != nil {
				return result, e
			}
			v.ConflictGroups = append([]string(nil), trustedRecipe.ConflictGroups...)
			value = v
		case "environment":
			var v domain.Environment
			if err = decode(r.Data, &v); err != nil {
				return result, err
			}
			v.ID = r.ID
			v.Name = "zah-" + domain.NewID()
			for _, candidate := range snap.Records {
				if candidate.Kind == "instance" {
					var inst domain.AppInstance
					if e := json.Unmarshal(candidate.Data, &inst); e != nil {
						return result, e
					}
					if inst.EnvironmentID == originalID {
						v.Name = "zah-" + result.IDMap["instance:"+candidate.ID]
						break
					}
				}
			}
			v.State = "unprepared"
			v.Ownership = "managed"
			v.Identity = ""
			v.Runtime = ""
			v.CreationEvidence = nil
			v.Registration = nil
			v.PendingRestore = nil
			v.LaunchLease = false
			v.LaunchToken = ""
			v.StopRequested = false
			value = v
		case "binding":
			var v domain.FileBinding
			if err = decode(r.Data, &v); err != nil {
				return result, err
			}
			v.ID = r.ID
			if v.InstanceID, err = mapped("instance", v.InstanceID); err != nil {
				return result, err
			}
			v.RestoredData = ""
			v.HostPath = ""
			v.State = "needs_binding"
			v.Device = 0
			v.Inode = 0
			refs = append(refs, ref("binding", v.ID, "instance", v.InstanceID, "binding"))
			value = v
		case "capability":
			var v domain.CapabilityResult
			if err = decode(r.Data, &v); err != nil {
				return result, err
			}
			v.ID = r.ID
			if v.InstanceID, err = mapped("instance", v.InstanceID); err != nil {
				return result, err
			}
			v.State = "historical_unverified"
			v.Evidence = "Imported historical evidence; requires validation on this host. " + v.Evidence
			refs = append(refs, ref("capability", v.ID, "instance", v.InstanceID, "capability"))
			value = v
		}
		r.Data, err = json.Marshal(value)
		if err != nil {
			return result, err
		}
		records = append(records, r)
	}
	// Preserve extra references only when both endpoints belong to this snapshot.
	for _, r := range snap.References {
		from, e := mapped(r.FromKind, r.FromID)
		if e != nil {
			return result, e
		}
		to, e := mapped(r.ToKind, r.ToID)
		if e != nil {
			return result, e
		}
		if reused[r.FromKind+":"+from] {
			continue
		}
		r.FromID = from
		r.ToID = to
		if len(r.Role) > 128 {
			return result, domain.ErrInvalid
		}
		refs = append(refs, r)
	}
	if prepare != nil {
		cleanup, e := prepare(stage, inspection, records, result.IDMap)
		if cleanup != nil {
			defer func() {
				if !committed {
					// Preserve prepared paths if commit outcome cannot be disproved.
					for _, r := range records {
						_, checkErr := s.Store.Get(context.WithoutCancel(ctx), r.Kind, r.ID)
						if !errors.Is(checkErr, domain.ErrNotFound) {
							return
						}
					}
					cleanup()
				}
			}()
		}
		if e != nil {
			return result, e
		}
	}
	if err = s.Store.ImportRecordsAndRecoveredMaterials(ctx, records, refs, mergedSources, recovered); err != nil {
		return result, err
	}
	committed = true
	result.Records = len(records)
	return result, nil
}
