package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/infra"
	"path/filepath"
	"time"
)

func (s *Service) planRebuild(ctx context.Context, req planRequest) (domain.Plan, error) {
	var in instanceInput
	p := domain.Plan{ID: domain.NewID(), Operation: req.Operation, CreatedAt: time.Now().UTC()}
	if e := decode(req.Input, &in); e != nil {
		return p, e
	}
	v, e := get[domain.AppInstance](ctx, s.Store, "instance", in.ID)
	if e != nil {
		return p, e
	}
	_, env, e := s.request(ctx, v)
	if e != nil {
		return p, e
	}
	if v.State != "needs_runtime_restore" || env.LaunchLease || env.LaunchToken != "" || env.StopRequested {
		return p, errors.New("instance is not available for environment restoration")
	}
	var pending PendingEnvironmentRestore
	if e = decode(env.PendingRestore, &pending); e != nil {
		return p, e
	}
	p.InstanceID = v.ID
	p.EnvironmentIDs = []string{env.ID}
	p.RecipeRevision = v.RecipeRevision
	p.Input = env.PendingRestore
	p.DataImpact = []string{"restore only this new managed environment from its verified snapshot; never run the application or replay its installer", "Distrobox initialization may install missing container dependencies; Docker/Distrobox containers share host resources and are not a security sandbox", "verify the new environment identity, private HOME, ordinary application UID and executable; mark ready only after verification"}
	p.Permissions = []string{"restoration uses the same backend privileges as application installation; only restore archives from a trusted source"}
	p.Steps = []domain.Step{{ID: domain.NewID(), Kind: p.Operation, Description: "重建并验证受管运行环境", SafeCancel: false}}
	return p, s.save(ctx, "plan", p.ID, p, ref("plan", p.ID, "instance", v.ID, "target"))
}
func (s *Service) executeRebuild(ctx context.Context, p domain.Plan) (any, error) {
	v, e := get[domain.AppInstance](ctx, s.Store, "instance", p.InstanceID)
	if e != nil {
		return nil, e
	}
	r, env, e := s.request(ctx, v)
	if e != nil {
		return nil, e
	}
	if v.State != "needs_runtime_restore" || p.RecipeRevision != v.RecipeRevision || env.LaunchLease || env.LaunchToken != "" || env.StopRequested {
		return nil, errors.New("restoration preconditions changed")
	}
	if len(p.EnvironmentIDs) != 1 || p.EnvironmentIDs[0] != env.ID {
		return nil, errors.New("restoration lock target changed")
	}
	var pending, reviewed PendingEnvironmentRestore
	if e = decode(env.PendingRestore, &pending); e != nil {
		return nil, e
	}
	if e = decode(p.Input, &reviewed); e != nil {
		return nil, e
	}
	if pending.ArchiveSHA256 != reviewed.ArchiveSHA256 || pending.ArchivePath != reviewed.ArchivePath || pending.Backend != reviewed.Backend {
		return nil, errors.New("restoration snapshot changed")
	}
	if pending.AttemptToken == "" {
		pending.AttemptToken = domain.NewID()
		env.PendingRestore, _ = json.Marshal(pending)
		if e = s.Store.Put(ctx, record("environment", env.ID, env)); e != nil {
			return nil, e
		}
	}
	persist := func(proof backend.Evidence) error {
		env.Identity = proof.ResourceID
		env.State = proof.State
		env.CreationEvidence, _ = json.Marshal(proof)
		if proof.Windows != nil {
			env.Registration, _ = json.Marshal(proof.Windows)
		}
		env.PendingRestore, _ = json.Marshal(pending)
		return s.Store.Put(context.WithoutCancel(ctx), record("environment", env.ID, env))
	}
	stage := filepath.Join(s.Paths.Cache, "restore")
	if e = infra.EnsurePrivateDir(stage); e != nil {
		return nil, e
	}
	var proof backend.Evidence
	if pending.Backend == "distrobox-docker" {
		proof, e = s.Backend.RebuildLinux(ctx, r, pending.ArchivePath, pending.ArchiveSHA256, pending.AttemptToken, stage, persist)
	} else if pending.Backend == "bottles-flatpak" {
		if pending.BottleProfile == nil {
			return nil, errors.New("this legacy Bottle backup has no portable restore profile; export it again with the updated ZaH")
		}
		if pending.BottleState == nil {
			pending.BottleState = &backend.BottleRebuildState{}
		}
		save := func() error {
			env.PendingRestore, _ = json.Marshal(pending)
			return s.Store.Put(context.WithoutCancel(ctx), record("environment", env.ID, env))
		}
		proof, e = s.Backend.RebuildBottle(ctx, r, pending.ArchivePath, pending.ArchiveSHA256, pending.AttemptToken, *pending.BottleProfile, pending.BottleState, save)
	} else {
		return nil, errors.New("unsupported restoration backend")
	}
	if e != nil {
		return proof, e
	}
	oldV, e := s.Store.Get(context.WithoutCancel(ctx), "instance", v.ID)
	if e != nil {
		return proof, e
	}
	oldE, e := s.Store.Get(context.WithoutCancel(ctx), "environment", env.ID)
	if e != nil {
		return proof, e
	}
	v.State = "ready"
	v.Entry = []string{proof.Entry}
	env.State = "stopped"
	env.Identity = proof.ResourceID
	env.CreationEvidence, _ = json.Marshal(proof)
	if proof.Windows != nil {
		env.Registration, _ = json.Marshal(proof.Windows)
	}
	pending.State = "entry_verified"
	env.PendingRestore, _ = json.Marshal(pending)
	e = s.Store.ReplaceInstallation(context.WithoutCancel(ctx), oldV, oldE, record("instance", v.ID, v), record("environment", env.ID, env), record("recipe", v.RecipeRevision, r.Recipe))
	return proof, e
}
