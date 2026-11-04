package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

type entryInput struct {
	Kind           string   `json:"kind,omitempty"`
	CWD            string   `json:"cwd,omitempty"`
	Args           []string `json:"args,omitempty"`
	ID             string   `json:"id"`
	ExecutablePath string   `json:"executable_path,omitempty"`
	Confirmed      bool     `json:"confirmed"`
	Identity       string   `json:"identity,omitempty"`
	SHA256         string   `json:"sha256,omitempty"`
	Program        string   `json:"program,omitempty"`
}

func (s *Service) entryContext(ctx context.Context, id string) (domain.AppInstance, backend.InstallRequest, domain.Environment, error) {
	v, e := get[domain.AppInstance](ctx, s.Store, "instance", id)
	if e != nil {
		return v, backend.InstallRequest{}, domain.Environment{}, e
	}
	r, env, e := s.request(ctx, v)
	if e != nil {
		return v, r, env, e
	}
	if v.Installation == nil || v.State != "ready" || env.LaunchLease || env.LaunchToken != "" || (r.Recipe.Platform == "windows" && (r.Windows == nil || r.Windows.Control == nil)) {
		return v, r, env, errors.New("stop the managed application/installer before selecting or changing its entry")
	}
	return v, r, env, nil
}
func (s *Service) planEntry(ctx context.Context, req planRequest) (domain.Plan, error) {
	var in entryInput
	if e := decode(req.Input, &in); e != nil {
		return domain.Plan{}, e
	}
	if !in.Confirmed || in.Identity != "" || in.SHA256 != "" || in.Program != "" {
		return domain.Plan{}, domain.ErrInvalid
	}
	v, r, env, e := s.entryContext(ctx, in.ID)
	if e != nil {
		return domain.Plan{}, e
	}
	if r.Recipe.Platform == "linux" {
		return s.planLinuxEntry(ctx, req, in, v, r, env)
	}
	p := domain.Plan{ID: domain.NewID(), Operation: req.Operation, InstanceID: v.ID, EnvironmentIDs: []string{env.ID}, ArtifactDigests: []string{v.OriginalArtifact}, RecipeRevision: v.RecipeRevision, CreatedAt: time.Now().UTC()}
	if req.Operation == "installation.maintenance" {
		if in.ExecutablePath != "" || in.CWD != "" || len(in.Args) != 0 {
			return p, domain.ErrInvalid
		}
		in.ExecutablePath, e = backend.InstallerPath(r)
		if e != nil {
			return p, e
		}
		in.Program = "ZaH installer"
	} else {
		// The original EXE may itself be a portable application. The user chooses
		// the final entry explicitly; its filename cannot classify its purpose.
		in.Program = "ZaH app-" + p.ID
	}
	in.SHA256, e = s.Backend.EntryDigest(ctx, r, in.ExecutablePath)
	if e != nil {
		return p, e
	}
	in.Identity = env.Identity
	p.Input, _ = json.Marshal(in)
	p.DataImpact = []string{"save the explicitly selected executable inside this dedicated Bottle; preserve installed files and all other programs", "no executable runs in this step; the native installer remains available for later maintenance"}
	p.Preconditions = []string{"user explicitly confirms installation is complete or requests maintenance; launcher exit alone is never installation completion", "the exact registered Wine environment must be stopped; executable digest and Bottle identity are rechecked"}
	p.Steps = []domain.Step{{ID: domain.NewID(), Kind: req.Operation, Description: "Verify stopped Bottle and save the chosen entry", SafeCancel: false}}
	refs := []domain.ResourceRef{ref("plan", p.ID, "instance", v.ID, "instance"), ref("plan", p.ID, "recipe", v.RecipeRevision, "recipe"), ref("plan", p.ID, "artifact", v.OriginalArtifact, "input")}
	return p, s.save(ctx, "plan", p.ID, p, refs...)
}
func (s *Service) executeEntry(ctx context.Context, p domain.Plan) (any, error) {
	var in entryInput
	if e := decode(p.Input, &in); e != nil {
		return nil, e
	}
	v, r, env, e := s.entryContext(ctx, in.ID)
	if e != nil {
		return nil, e
	}
	if p.InstanceID != v.ID || p.RecipeRevision != v.RecipeRevision || in.Identity != env.Identity || !in.Confirmed {
		return nil, errors.New("entry review is stale")
	}
	if r.Recipe.Platform == "linux" {
		return s.executeLinuxEntry(ctx, p, in, v, r, env)
	}
	oldV, e := s.Store.Get(ctx, "instance", v.ID)
	if e != nil {
		return nil, e
	}
	oldE, e := s.Store.Get(ctx, "environment", env.ID)
	if e != nil {
		return nil, e
	}
	proof, e := s.Backend.SaveProgram(ctx, r, env.Identity, in.Program, in.ExecutablePath, in.SHA256, backend.WindowsEntryOptions{CWD: in.CWD, Args: in.Args})
	if e != nil {
		return nil, e
	}
	r.Recipe.Entry = in.Program
	r.Recipe.Revision = domain.NewID()
	if e = r.Recipe.Validate(); e != nil {
		return nil, e
	}
	v.RecipeRevision = r.Recipe.Digest()
	v.Entry = []string{in.Program}
	v.Installation.Stage = "complete"
	if p.Operation == "installation.maintenance" {
		v.Installation.Stage = "installer_ready"
	}
	env.Identity = proof.ResourceID
	env.Registration, _ = json.Marshal(proof.Windows)
	env.State = "stopped"
	env.CreationEvidence, _ = json.Marshal(proof)
	e = s.Store.ReplaceInstallation(ctx, oldV, oldE, record("instance", v.ID, v), record("environment", env.ID, env), record("recipe", v.RecipeRevision, r.Recipe))
	// Persist launch settings only in the private registration, not the job log.
	proof.Windows = nil
	return proof, e
}
