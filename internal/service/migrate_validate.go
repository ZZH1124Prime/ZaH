package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ZZH1124Prime/ZaH/internal/catalog"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/recipe"
)

// validateCatalogSnapshot treats a checksummed archive as untrusted structured
// input. Checksums establish byte consistency, never catalog semantics or trust.
func validateCatalogSnapshot(snap catalogSnapshot) error {
	if snap.Format != catalogBundleFormat || snap.SchemaVersion != 1 {
		return errors.New("unsupported catalog snapshot")
	}
	if len(snap.Records) > 10000 || len(snap.References) > 100000 {
		return errors.New("catalog record limit exceeded")
	}
	families := map[string]domain.AppFamily{}
	releases := map[string]domain.AppRelease{}
	instances := map[string]domain.AppInstance{}
	envs := map[string]domain.Environment{}
	recipes := map[string]recipe.Recipe{}
	artifacts := map[string]bool{}
	bindings := []domain.FileBinding{}
	caps := []domain.CapabilityResult{}
	seen := map[string]bool{}
	for _, r := range snap.Records {
		key := r.Kind + ":" + r.ID
		if r.ID == "" || len(r.ID) > 256 || strings.ContainsAny(r.ID, "/\\\x00\r\n:") || seen[key] || len(r.Data) > 4<<20 {
			return errors.New("invalid/duplicate catalog identity or oversized record")
		}
		seen[key] = true
		var fields map[string]json.RawMessage
		if e := json.Unmarshal(r.Data, &fields); e != nil || fields == nil {
			return errors.New("record data must be a JSON object")
		}
		if r.Kind != "artifact" && r.Kind != "recipe" {
			var id string
			if e := json.Unmarshal(fields["id"], &id); e != nil || id != r.ID {
				return errors.New("record envelope and entity identity differ")
			}
		}
		switch r.Kind {
		case "family":
			var v domain.AppFamily
			if e := decode(r.Data, &v); e != nil {
				return e
			}
			if e := required(v.Name); e != nil {
				return e
			}
			if len(v.StableID) > 256 || strings.ContainsAny(v.StableID, "/\\\x00\r\n:") {
				return errors.New("invalid stable application identity")
			}
			families[r.ID] = v
		case "release":
			var v domain.AppRelease
			if e := decode(r.Data, &v); e != nil {
				return e
			}
			if (v.Platform != "linux" && v.Platform != "windows") || (v.Arch != "amd64" && v.Arch != "unknown") || len(v.Artifacts) == 0 || len(v.Artifacts) > 128 || len(v.Version) > 512 {
				return errors.New("invalid release platform, architecture, version or empty materials")
			}
			if v.IconSHA256 != "" && !catalog.ValidDigest(v.IconSHA256) {
				return errors.New("invalid release icon digest")
			}
			releases[r.ID] = v
		case "instance":
			var v domain.AppInstance
			if e := decode(r.Data, &v); e != nil {
				return e
			}
			if e := required(v.Name); e != nil {
				return e
			}
			instances[r.ID] = v
		case "environment":
			var v domain.Environment
			if e := decode(r.Data, &v); e != nil {
				return e
			}
			if v.Backend != "distrobox-docker" && v.Backend != "bottles-flatpak" {
				return errors.New("unsupported environment backend")
			}
			envs[r.ID] = v
		case "recipe":
			v, e := recipe.Decode(bytes.NewReader(r.Data))
			if e != nil {
				return e
			}
			if v.Digest() != r.ID {
				return errors.New("recipe digest mismatch")
			}
			recipes[r.ID] = v
		case "artifact":
			var v catalog.Artifact
			if e := decode(r.Data, &v); e != nil {
				return e
			}
			if v.Availability != "" && v.Availability != "available" {
				return fmt.Errorf("material %s is unavailable; reimport before exporting a complete catalog/materials bundle", r.ID)
			}
			if !catalog.ValidDigest(r.ID) || v.SHA256 != r.ID || v.Size < 0 {
				return errors.New("invalid artifact identity or size")
			}
			artifacts[r.ID] = true
		case "binding":
			var v domain.FileBinding
			if e := decode(r.Data, &v); e != nil {
				return e
			}
			if (v.Access != "read" && v.Access != "write") || strings.ContainsAny(v.TargetPath, "\x00\r\n") {
				return errors.New("invalid file binding")
			}
			bindings = append(bindings, v)
		case "capability":
			var v domain.CapabilityResult
			if e := decode(r.Data, &v); e != nil {
				return e
			}
			if e := required(v.Capability); e != nil {
				return e
			}
			caps = append(caps, v)
		default:
			return fmt.Errorf("nonportable record kind %q", r.Kind)
		}
	}
	for _, r := range releases {
		if r.IconSHA256 != "" {
			if _, ok := artifacts[r.IconSHA256]; !ok {
				return errors.New("release icon missing")
			}
		}
		if _, ok := families[r.FamilyID]; !ok {
			return errors.New("release family missing")
		}
		materialSet := map[string]bool{}
		for _, id := range r.Artifacts {
			if !artifacts[id] || materialSet[id] {
				return errors.New("release material missing or duplicate")
			}
			materialSet[id] = true
		}
	}
	usedEnvs := map[string]bool{}
	for _, v := range instances {
		r, ok := releases[v.ReleaseID]
		if !ok {
			return errors.New("instance release missing")
		}
		p, ok := recipes[v.RecipeRevision]
		if !ok || p.Platform != r.Platform || p.Architecture != r.Arch {
			return errors.New("instance recipe differs from release")
		}
		env, ok := envs[v.EnvironmentID]
		if !ok || usedEnvs[v.EnvironmentID] {
			return errors.New("instance environment missing or shared")
		}
		usedEnvs[v.EnvironmentID] = true
		expected := "distrobox-docker"
		if r.Platform == "windows" {
			expected = "bottles-flatpak"
		}
		if v.Installation != nil {
			if e := validateInstallation(v.Installation, r.Platform); e != nil {
				return e
			}
		}
		if env.Backend != expected {
			return errors.New("instance environment backend mismatch")
		}
		found := false
		for _, a := range r.Artifacts {
			if a == v.OriginalArtifact {
				found = true
			}
		}
		if !found {
			return errors.New("instance original material not in release")
		}
	}
	for _, f := range families {
		if f.DefaultInstanceID != "" {
			i, ok := instances[f.DefaultInstanceID]
			if !ok || releases[i.ReleaseID].FamilyID != f.ID {
				return errors.New("default instance is not in its family")
			}
		}
	}
	bindingTargets := map[string]bool{}
	bindingCounts := map[string]int{}
	for _, b := range bindings {
		if _, ok := instances[b.InstanceID]; !ok {
			return errors.New("binding instance missing")
		}
		platform := releases[instances[b.InstanceID].ReleaseID].Platform
		key := b.InstanceID + "\x00" + b.TargetPath
		bindingCounts[b.InstanceID]++
		if !validBindingTarget(platform, b.TargetPath) || bindingTargets[key] || bindingCounts[b.InstanceID] > 64 {
			return errors.New("invalid, duplicate or excessive platform file bindings")
		}
		bindingTargets[key] = true
	}
	for _, c := range caps {
		if _, ok := instances[c.InstanceID]; !ok {
			return errors.New("capability instance missing")
		}
	}
	for _, r := range snap.References {
		if !seen[r.FromKind+":"+r.FromID] || !seen[r.ToKind+":"+r.ToID] || r.Role == "" || len(r.Role) > 128 || strings.ContainsAny(r.Role, "\x00\r\n") {
			return errors.New("invalid snapshot resource reference")
		}
	}
	return nil
}
