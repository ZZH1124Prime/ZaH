package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

// RestoreOptions enables conservative identity-based merging. Existing callers
// keep their original isolated-copy behavior unless they explicitly opt in.
type RestoreOptions struct {
	Merge     bool
	Directory string
	Parent    *domain.ManagedDataRoot
}

func familyIdentity(v domain.AppFamily) string {
	if v.StableID != "" {
		return v.StableID
	}
	return v.ID
}
func releaseIdentity(v domain.AppRelease) string {
	return v.FamilyID + "\x00" + v.Platform + "\x00" + v.Arch + "\x00" + v.Version
}

// mergeSelection never matches names or updates destination records. A legacy
// family ID is its portable identity; imported copies persist that identity.
func (s *Service) mergeSelection(ctx context.Context, snap catalogSnapshot) (catalogSnapshot, map[string]string, int, error) {
	existing, err := s.Store.List(ctx, "family")
	if err != nil {
		return snap, nil, 0, err
	}
	identities := map[string]domain.AppFamily{}
	for _, row := range existing {
		var f domain.AppFamily
		if err = json.Unmarshal(row.Data, &f); err != nil {
			return snap, nil, 0, err
		}
		key := familyIdentity(f)
		if _, ok := identities[key]; ok {
			return snap, nil, 0, errors.New("ambiguous destination application identity")
		}
		identities[key] = f
	}
	aliases := map[string]string{}
	incoming := map[string]bool{}
	for _, row := range snap.Records {
		if row.Kind != "family" {
			continue
		}
		var f domain.AppFamily
		if err = json.Unmarshal(row.Data, &f); err != nil {
			return snap, nil, 0, err
		}
		key := familyIdentity(f)
		if incoming[key] {
			return snap, nil, 0, errors.New("duplicate application identity in archive")
		}
		incoming[key] = true
		if local, ok := identities[key]; ok {
			aliases[f.ID] = local.ID
		}
	}
	localReleases, err := s.Store.List(ctx, "release")
	if err != nil {
		return snap, nil, 0, err
	}
	versions := map[string]bool{}
	for _, row := range localReleases {
		var r domain.AppRelease
		if err = json.Unmarshal(row.Data, &r); err != nil {
			return snap, nil, 0, err
		}
		versions[releaseIdentity(r)] = true
	}
	skip := map[string]bool{}
	count := 0
	seenVersions := map[string]bool{}
	for _, row := range snap.Records {
		if row.Kind != "release" {
			continue
		}
		var r domain.AppRelease
		if err = json.Unmarshal(row.Data, &r); err != nil {
			return snap, nil, 0, err
		}
		if id, ok := aliases[r.FamilyID]; ok {
			r.FamilyID = id
		}
		key := releaseIdentity(r)
		if seenVersions[key] {
			return snap, nil, 0, errors.New("archive contains duplicate platform/architecture/version identities")
		}
		seenVersions[key] = true
		if versions[key] {
			skip["release:"+row.ID] = true
			count++
		}
	}
	for _, row := range snap.Records {
		if row.Kind != "instance" {
			continue
		}
		var v domain.AppInstance
		if err = json.Unmarshal(row.Data, &v); err != nil {
			return snap, nil, 0, err
		}
		if skip["release:"+v.ReleaseID] {
			skip["instance:"+v.ID] = true
			skip["environment:"+v.EnvironmentID] = true
		}
	}
	for _, row := range snap.Records {
		switch row.Kind {
		case "binding", "capability":
			var fields struct {
				InstanceID string `json:"instance_id"`
			}
			if err = json.Unmarshal(row.Data, &fields); err != nil {
				return snap, nil, 0, err
			}
			if skip["instance:"+fields.InstanceID] {
				skip[row.Kind+":"+row.ID] = true
			}
		}
	}
	needed := map[string]bool{}
	for _, row := range snap.Records {
		if skip[row.Kind+":"+row.ID] {
			continue
		}
		switch row.Kind {
		case "release":
			var r domain.AppRelease
			if err = json.Unmarshal(row.Data, &r); err != nil {
				return snap, nil, 0, err
			}
			for _, id := range r.Artifacts {
				needed["artifact:"+id] = true
			}
			needed["artifact:"+r.IconSHA256] = true
		case "instance":
			var v domain.AppInstance
			if err = json.Unmarshal(row.Data, &v); err != nil {
				return snap, nil, 0, err
			}
			needed["artifact:"+v.OriginalArtifact] = true
			needed["recipe:"+v.RecipeRevision] = true
		}
	}
	kept := []domain.Record{}
	for _, row := range snap.Records {
		if skip[row.Kind+":"+row.ID] {
			continue
		}
		if (row.Kind == "artifact" || row.Kind == "recipe") && !needed[row.Kind+":"+row.ID] {
			continue
		}
		if row.Kind == "family" {
			var f domain.AppFamily
			if err = json.Unmarshal(row.Data, &f); err != nil {
				return snap, nil, 0, err
			}
			if skip["instance:"+f.DefaultInstanceID] {
				f.DefaultInstanceID = ""
			}
			row.Data, _ = json.Marshal(f)
		}
		kept = append(kept, row)
	}
	keys := map[string]bool{}
	for _, r := range kept {
		keys[r.Kind+":"+r.ID] = true
	}
	refs := []domain.ResourceRef{}
	for _, r := range snap.References {
		if keys[r.FromKind+":"+r.FromID] && keys[r.ToKind+":"+r.ToID] {
			refs = append(refs, r)
		}
	}
	snap.Records = kept
	snap.References = refs
	return snap, aliases, count, nil
}

func skippedPayload(member, instanceID string) bool {
	// External binding copies are also inside data/<instance>/bindings/.
	return strings.HasPrefix(member, "data/"+instanceID+"/") || strings.HasPrefix(member, "environments/"+instanceID+"/")
}
