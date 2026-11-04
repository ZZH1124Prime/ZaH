package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/recipe"
)

func (s *Service) planLinuxEntry(ctx context.Context, req planRequest, in entryInput, v domain.AppInstance, r backend.InstallRequest, env domain.Environment) (domain.Plan, error) {
	if req.Operation != "installation.finish" || len(in.Args) != 0 {
		return domain.Plan{}, errors.New("choose an installed Linux entry; desktop files retain their own arguments")
	}
	candidates, e := s.Backend.LinuxProgramCandidates(ctx, r, env.Identity)
	if e != nil {
		return domain.Plan{}, e
	}
	var selected *backend.LinuxProgramCandidate
	for _, candidate := range candidates {
		if candidate.Path == in.ExecutablePath && candidate.Kind == in.Kind {
			copy := candidate
			selected = &copy
			break
		}
	}
	if selected == nil {
		return domain.Plan{}, errors.New("select an entry from this container's installed programs")
	}
	in.CWD = selected.CWD
	in.SHA256 = selected.SHA256
	in.Identity = env.Identity
	in.Program = selected.Name
	p := domain.Plan{ID: domain.NewID(), Operation: req.Operation, InstanceID: v.ID, EnvironmentIDs: []string{env.ID}, ArtifactDigests: []string{v.OriginalArtifact}, RecipeRevision: v.RecipeRevision, CreatedAt: time.Now().UTC(), DataImpact: []string{"Save only the explicitly chosen installed Linux desktop entry or AppImage; do not run it"}, Steps: []domain.Step{{ID: domain.NewID(), Kind: req.Operation, Description: "Verify and save selected Linux application", SafeCancel: false}}}
	p.Input, _ = json.Marshal(in)
	return p, s.save(ctx, "plan", p.ID, p, ref("plan", p.ID, "instance", v.ID, "instance"), ref("plan", p.ID, "recipe", v.RecipeRevision, "recipe"), ref("plan", p.ID, "artifact", v.OriginalArtifact, "input"))
}
func (s *Service) executeLinuxEntry(ctx context.Context, p domain.Plan, in entryInput, v domain.AppInstance, r backend.InstallRequest, env domain.Environment) (any, error) {
	selected, e := s.Backend.VerifyLinuxEntry(ctx, r, env.Identity, backend.LinuxProgramCandidate{Name: in.Program, Kind: in.Kind, Path: in.ExecutablePath, CWD: in.CWD, SHA256: in.SHA256})
	if e != nil {
		return nil, e
	}
	oldV, e := s.Store.Get(ctx, "instance", v.ID)
	if e != nil {
		return nil, e
	}
	oldE, e := s.Store.Get(ctx, "environment", env.ID)
	if e != nil {
		return nil, e
	}
	saved := &recipe.LinuxEntry{Kind: selected.Kind, Path: selected.Path, CWD: selected.CWD, SHA256: selected.SHA256}
	if rel, e := filepath.Rel(r.PrivateHome, selected.Path); e == nil && filepath.IsLocal(rel) {
		saved.Path = filepath.ToSlash(rel)
		saved.HomeRelative = true
	}
	r.Recipe.PendingEntry = false
	r.Recipe.LinuxEntry = saved
	r.Recipe.Entry = selected.Path
	if r.Recipe.Strategy == "appimage" {
		r.Recipe.Entry = "squashfs-root/AppRun"
	}
	r.Recipe.Revision = domain.NewID()
	if e = r.Recipe.Validate(); e != nil {
		return nil, e
	}
	v.RecipeRevision = r.Recipe.Digest()
	v.Entry = []string{selected.Path}
	v.Installation.Stage = "complete"
	// Container may remain running after package installation, without an app lease.
	// Do not invent an application-running state or stop the container here.
	e = s.Store.ReplaceInstallation(ctx, oldV, oldE, record("instance", v.ID, v), record("environment", env.ID, env), record("recipe", v.RecipeRevision, r.Recipe))
	return selected, e
}

// refreshLinuxInstalledEntry invalidates a saved choice only after a successful
// inventory proves it missing or changed. Inventory failures preserve the choice
// and are returned to the user rather than being treated as an uninstall.
func (s *Service) refreshLinuxInstalledEntry(ctx context.Context, v domain.AppInstance, r backend.InstallRequest, env domain.Environment) error {
	selected := r.Recipe.LinuxEntry
	if selected == nil || v.Installation == nil || v.Installation.Stage != "complete" {
		return nil
	}
	candidates, err := s.Backend.LinuxProgramCandidates(ctx, r, env.Identity)
	if err != nil {
		return err
	}
	path := selected.Path
	if selected.HomeRelative {
		path = filepath.Join(r.PrivateHome, filepath.FromSlash(path))
	}
	for _, candidate := range candidates {
		if candidate.Path == path && candidate.Kind == selected.Kind && candidate.SHA256 == selected.SHA256 && candidate.CWD == selected.CWD {
			return nil
		}
	}
	return s.invalidateLinuxInstalledEntry(ctx, v, r, env)
}

func (s *Service) invalidateLinuxInstalledEntry(ctx context.Context, v domain.AppInstance, r backend.InstallRequest, env domain.Environment) error {
	oldV, err := s.Store.Get(ctx, "instance", v.ID)
	if err != nil {
		return err
	}
	oldE, err := s.Store.Get(ctx, "environment", env.ID)
	if err != nil {
		return err
	}
	r.Recipe.PendingEntry = true
	r.Recipe.LinuxEntry = nil
	r.Recipe.Entry = ""
	r.Recipe.Revision = domain.NewID()
	if err = r.Recipe.Validate(); err != nil {
		return err
	}
	v.RecipeRevision = r.Recipe.Digest()
	v.Entry = nil
	v.Installation.Stage = "awaiting_completion"
	return s.Store.ReplaceInstallation(ctx, oldV, oldE, record("instance", v.ID, v), record("environment", env.ID, env), record("recipe", v.RecipeRevision, r.Recipe))
}
