package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"time"
)

type installationToolInput struct {
	ID       string `json:"id"`
	Action   string `json:"action"`
	Identity string `json:"identity,omitempty"`
}

func (s *Service) planInstallationTool(ctx context.Context, req planRequest) (domain.Plan, error) {
	var in installationToolInput
	if e := decode(req.Input, &in); e != nil {
		return domain.Plan{}, e
	}
	if in.Identity != "" {
		return domain.Plan{}, domain.ErrInvalid
	}
	v, r, env, e := s.entryContext(ctx, in.ID)
	if e != nil {
		return domain.Plan{}, e
	}
	valid := in.Action == "files" || r.Recipe.Platform == "linux" && (in.Action == "install" || in.Action == "packages" || in.Action == "start") || r.Recipe.Platform == "windows" && in.Action == "uninstall"
	if !valid {
		return domain.Plan{}, errors.New("unsupported installation tool")
	}
	in.Identity = env.Identity
	p := domain.Plan{ID: domain.NewID(), Operation: req.Operation, InstanceID: v.ID, EnvironmentIDs: []string{env.ID}, RecipeRevision: v.RecipeRevision, CreatedAt: time.Now().UTC(), DataImpact: []string{"Open the selected dedicated environment's native tool; installation/removal decisions remain in its GUI"}, Steps: []domain.Step{{ID: domain.NewID(), Kind: req.Operation, Description: "Open native installation/management window", SafeCancel: false}}}
	p.Input, _ = json.Marshal(in)
	return p, s.save(ctx, "plan", p.ID, p, ref("plan", p.ID, "instance", v.ID, "instance"), ref("plan", p.ID, "recipe", v.RecipeRevision, "recipe"))
}
func (s *Service) executeInstallationTool(ctx context.Context, p domain.Plan) (any, error) {
	var in installationToolInput
	if e := decode(p.Input, &in); e != nil {
		return nil, e
	}
	v, r, env, e := s.entryContext(ctx, in.ID)
	if e != nil {
		return nil, e
	}
	if p.InstanceID != v.ID || p.RecipeRevision != v.RecipeRevision || in.Identity != env.Identity {
		return nil, errors.New("installation tool review is stale")
	}
	if r.Recipe.Platform == "linux" {
		proof, e := s.Backend.BeginLinuxEntrySelection(ctx, r, env.Identity)
		if e != nil {
			return proof, e
		}
		env.State = proof.State
		if e = s.Store.Put(ctx, record("environment", env.ID, env)); e != nil {
			return proof, e
		}
		if in.Action == "start" {
			return proof, nil
		}
		proof, e = s.Backend.LaunchLinuxInstallationGUI(ctx, r, env.Identity, in.Action)
		proof.Result.Stdout = ""
		proof.Result.Stderr = ""
		// A native package/file manager may remove or replace the chosen entry.
		// Refresh readiness before the product card is shown again.
		refreshErr := s.refreshLinuxInstalledEntry(context.WithoutCancel(ctx), v, r, env)
		return proof, errors.Join(e, refreshErr)
	}
	proof, e := s.Backend.LaunchWindowsInstallationGUI(ctx, r, env.Identity, in.Action)
	proof.Result.Stdout = ""
	proof.Result.Stderr = ""
	proof.Windows = nil
	if proof.State == "launcher_exited_application_unknown" {
		env.State = proof.State
		if saveErr := s.Store.Put(context.WithoutCancel(ctx), record("environment", env.ID, env)); saveErr != nil {
			return proof, saveErr
		}
	}
	return proof, e
}
