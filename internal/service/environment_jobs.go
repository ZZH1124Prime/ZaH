package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/infra"
	"github.com/ZZH1124Prime/ZaH/internal/migration"
)

type environmentVerifyInput struct {
	ID          string `json:"id"`
	ContainerID string `json:"container_id"`
}

func (s *Service) planEnvironment(ctx context.Context, req planRequest) (domain.Plan, error) {
	p := domain.Plan{ID: domain.NewID(), Operation: req.Operation, Input: req.Input, CreatedAt: time.Now().UTC()}
	var id string
	switch req.Operation {
	case "environment.export":
		var in EnvironmentBundleInput
		if e := decode(req.Input, &in); e != nil {
			return p, e
		}
		if !in.ColdConfirmed {
			return p, errors.New("confirm all application and external writers have stopped")
		}
		if !filepath.IsAbs(in.Path) || filepath.Ext(in.Path) != ".zahbundle" {
			return p, domain.ErrInvalid
		}
		if _, e := os.Lstat(in.Path); !os.IsNotExist(e) {
			return p, errors.New("export destination must not exist")
		}
		if _, e := s.selectedBindings(ctx, in.ID, in.IncludeBindings); e != nil {
			return p, e
		}
		id = in.ID
		p.DataImpact = []string{"creates a cold single-instance bundle including archived installers, runtime snapshot and private HOME", "unselected external bindings are omitted; selected copies restore to new managed directories and require explicit rebinding", "may create an untagged Docker snapshot image; no existing image is pruned"}
		p.Permissions = []string{"plain archive can include private application data and login state; no encryption"}
	case "environment.restore":
		var in bundleInput
		if e := decode(req.Input, &in); e != nil {
			return p, e
		}
		if !filepath.IsAbs(in.Path) {
			return p, domain.ErrInvalid
		}
		parent, e := inspectRestoreParent(in.Directory)
		in.Parent = parent
		if e != nil {
			return p, e
		}
		inspection, e := migration.Inspect(ctx, in.Path, migration.Limits{})
		if e != nil {
			return p, e
		}
		in.Fingerprint, e = bundleIdentity(inspection)
		if e != nil {
			return p, e
		}
		p.Input, _ = json.Marshal(in)
		p.DataImpact = []string{"creates new instance IDs and restores private bytes before atomic catalog registration", "runtime stays unprepared; image loading/upstream import and entry verification are separate"}
	case "environment.load":
		var in instanceInput
		if e := decode(req.Input, &in); e != nil {
			return p, e
		}
		id = in.ID
	case "environment.verify":
		var in environmentVerifyInput
		if e := decode(req.Input, &in); e != nil {
			return p, e
		}
		if in.ContainerID == "" {
			return p, domain.ErrInvalid
		}
		id = in.ID
	default:
		return p, domain.ErrInvalid
	}
	refs := []domain.ResourceRef{}
	if id != "" {
		v, e := get[domain.AppInstance](ctx, s.Store, "instance", id)
		if e != nil {
			return p, e
		}
		r, env, e := s.request(ctx, v)
		if e != nil {
			return p, e
		}
		p.InstanceID = id
		p.EnvironmentIDs = []string{env.ID}
		p.RecipeRevision = v.RecipeRevision
		p.ArtifactDigests = []string{v.OriginalArtifact}
		refs = append(refs, ref("plan", p.ID, "instance", id, "target"), ref("plan", p.ID, "artifact", v.OriginalArtifact, "input"))
		if req.Operation == "environment.export" {
			if env.Identity == "" {
				return p, errors.New("environment has no verified upstream identity")
			}
		} else {
			var pending PendingEnvironmentRestore
			if e = decode(env.PendingRestore, &pending); e != nil {
				return p, e
			}
			if pending.Backend != "distrobox-docker" {
				return p, errors.New("Bottles restore uses its supported GUI then instance.register verification")
			}
			if v.State != "needs_runtime_restore" {
				return p, errors.New("instance is not awaiting runtime restoration")
			}
			if req.Operation == "environment.load" {
				load, e := s.Backend.PlanLinuxRestore(ctx, r, pending.ArchivePath, pending.ArchiveSHA256)
				if e != nil {
					return p, e
				}
				p.Input, _ = json.Marshal(load)
				p.DataImpact = load.RequiredActions
				p.Permissions = []string{"loads one verified tagless image into existing Docker image storage; does not launch or reconfigure Docker"}
			}
			if req.Operation == "environment.verify" {
				if pending.ImageID == "" {
					return p, errors.New("restored image identity missing")
				}
				p.DataImpact = []string{"checks a manually rebuilt container against new instance name, ownership label, expected image, private HOME and entry", "marks entry ready only after checks; application capabilities require separate validation"}
			}
		}
	}
	p.Preconditions = []string{"saved recipe, material and restored archive identities are revalidated", "global modification and environment locks held"}
	p.Steps = []domain.Step{{ID: domain.NewID(), Kind: p.Operation, Description: "Reviewed environment backup or restoration stage", Params: p.Input, SafeCancel: false}}
	return p, s.save(ctx, "plan", p.ID, p, refs...)
}

func (s *Service) executeEnvironment(ctx context.Context, p domain.Plan) (any, error) {
	if p.Operation == "environment.restore" {
		var in bundleInput
		if e := decode(p.Input, &in); e != nil {
			return nil, e
		}
		if in.Fingerprint == "" {
			return nil, errors.New("reviewed bundle identity required")
		}
		return s.RestoreEnvironmentBundle(withBundleIdentity(ctx, in.Fingerprint), in.Path, RestoreOptions{Merge: in.Merge, Directory: in.Directory, Parent: in.Parent})
	}
	v, e := get[domain.AppInstance](ctx, s.Store, "instance", p.InstanceID)
	if e != nil {
		return nil, e
	}
	r, env, e := s.request(ctx, v)
	if e != nil {
		return nil, e
	}
	if p.RecipeRevision != v.RecipeRevision {
		return nil, errors.New("recipe changed after planning")
	}
	if p.Operation == "environment.export" {
		var in EnvironmentBundleInput
		if e = decode(p.Input, &in); e != nil {
			return nil, e
		}
		if in.ID != p.InstanceID {
			return nil, domain.ErrInvalid
		}
		return s.ExportEnvironmentBundle(ctx, in)
	}
	var pending PendingEnvironmentRestore
	if e = decode(env.PendingRestore, &pending); e != nil {
		return nil, e
	}
	switch p.Operation {
	case "environment.load":
		var plan backend.LinuxRestorePlan
		if e = decode(p.Input, &plan); e != nil {
			return nil, e
		}
		current, _ := json.Marshal(r)
		saved, _ := json.Marshal(plan.Request)
		if !bytes.Equal(current, saved) || pending.ArchivePath != plan.Archive.Path || pending.ArchiveSHA256 != plan.Archive.SHA256 {
			return nil, errors.New("restore plan preconditions changed")
		}
		stage := filepath.Join(s.Paths.Cache, "restore")
		if e = infra.EnsurePrivateDir(stage); e != nil {
			return nil, e
		}
		proof, loadErr := s.Backend.LoadLinuxSnapshot(ctx, plan, stage)
		if loadErr != nil {
			return proof, loadErr
		}
		pending.ImageID = proof.ImageID
		pending.State = "image_loaded_manual_rebuild_required"
		env.PendingRestore, _ = json.Marshal(pending)
		if e = s.Store.Put(context.WithoutCancel(ctx), record("environment", env.ID, env)); e != nil {
			return proof, e
		}
		return proof, nil
	case "environment.verify":
		var in environmentVerifyInput
		if e = decode(p.Input, &in); e != nil {
			return nil, e
		}
		if in.ID != v.ID {
			return nil, domain.ErrInvalid
		}
		proof, e := s.Backend.VerifyManualLinuxRestore(ctx, r, in.ContainerID, pending.ImageID)
		if e != nil {
			return proof, e
		}
		if e = s.bindingsVerified(ctx, v.ID); e != nil {
			return proof, e
		}
		proof.Result.Stdout = ""
		proof.Result.Stderr = ""
		env.Identity = proof.ResourceID
		env.State = proof.State
		env.CreationEvidence, _ = json.Marshal(proof)
		pending.State = "entry_verified"
		env.PendingRestore, _ = json.Marshal(pending)
		if e = s.Store.Put(ctx, record("environment", env.ID, env)); e != nil {
			return proof, e
		}
		v.State = "ready"
		v.Entry = []string{proof.Entry}
		if e = s.persistInstanceState(ctx, v); e != nil {
			return proof, e
		}
		return proof, nil
	default:
		return nil, domain.ErrInvalid
	}
}
