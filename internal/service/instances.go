package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/host"
	"github.com/ZZH1124Prime/ZaH/internal/jobs"
	"github.com/ZZH1124Prime/ZaH/internal/recipe"
)

func (s *Service) addRecipe(ctx context.Context, raw json.RawMessage) (any, error) {
	r, e := recipe.Decode(bytes.NewReader(raw))
	if e != nil {
		return nil, e
	}
	id := r.Digest()
	if old, e := s.Store.Get(ctx, "recipe", id); e == nil {
		return old, nil
	}
	return map[string]any{"id": id, "recipe": r}, s.save(ctx, "recipe", id, r)
}
func (s *Service) createInstance(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		ReleaseID string `json:"release_id"`
		RecipeID  string `json:"recipe_id"`
		Name      string `json:"name"`
	}
	if e := decode(raw, &in); e != nil {
		return nil, e
	}
	if e := required(in.Name); e != nil {
		return nil, e
	}
	rel, e := get[domain.AppRelease](ctx, s.Store, "release", in.ReleaseID)
	if e != nil {
		return nil, e
	}
	r, e := get[recipe.Recipe](ctx, s.Store, "recipe", in.RecipeID)
	if e != nil {
		return nil, e
	}
	if len(rel.Artifacts) == 0 {
		return nil, errors.New("release has no installation material")
	}
	if r.Platform != rel.Platform || r.Architecture != rel.Arch {
		return nil, errors.New("recipe and release platform/architecture must match")
	}
	id := domain.NewID()
	env := domain.Environment{ID: domain.NewID(), Backend: "distrobox-docker", Name: "zah-" + id, Ownership: "managed", State: "unprepared"}
	if r.Platform == "windows" {
		env.Backend = "bottles-flatpak"
	}
	v := domain.AppInstance{ID: id, ReleaseID: rel.ID, EnvironmentID: env.ID, Name: in.Name, RecipeRevision: r.Digest(), PrivateData: filepath.Join(s.Paths.Data, "instances", id, "home"), State: "unprepared", OriginalArtifact: rel.Artifacts[0], Drift: "unknown", ConflictGroups: r.ConflictGroups}
	// Environment registration itself has no external effect. On a later failure,
	// an unreferenced unprepared record remains visible for inspection.
	if e = s.save(ctx, "environment", env.ID, env); e != nil {
		return nil, e
	}
	refs := []domain.ResourceRef{ref("instance", id, "release", rel.ID, "release"), ref("instance", id, "recipe", r.Digest(), "recipe"), ref("instance", id, "environment", env.ID, "environment"), ref("instance", id, "artifact", v.OriginalArtifact, "original_artifact")}
	return v, s.save(ctx, "instance", id, v, refs...)
}
func (s *Service) request(ctx context.Context, v domain.AppInstance) (backend.InstallRequest, domain.Environment, error) {
	if v.RestoreRoot != nil {
		if _, e := s.instanceDataRoot(v); e != nil {
			return backend.InstallRequest{}, domain.Environment{}, e
		}
	}
	if v.State == "forgotten" {
		return backend.InstallRequest{}, domain.Environment{}, domain.ErrNotFound
	}
	env, e := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	if e != nil {
		return backend.InstallRequest{}, env, e
	}
	if env.Ownership != "managed" || env.Name != "zah-"+v.ID {
		return backend.InstallRequest{}, env, errors.New("environment not owned by this instance")
	}
	r, e := get[recipe.Recipe](ctx, s.Store, "recipe", v.RecipeRevision)
	if e != nil {
		return backend.InstallRequest{}, env, e
	}
	if r.Digest() != v.RecipeRevision {
		return backend.InstallRequest{}, env, errors.New("recipe content changed")
	}
	a := s.Artifacts()
	path, e := a.Path(v.OriginalArtifact)
	if e != nil {
		return backend.InstallRequest{}, env, e
	}
	request := backend.InstallRequest{InstanceID: v.ID, ArtifactPath: path, ArtifactSHA256: v.OriginalArtifact, PrivateHome: v.PrivateData, Recipe: r}
	if v.Installation != nil && r.Platform == "windows" {
		request.Material = &backend.InstallMaterial{Format: v.Installation.Format, Mode: v.Installation.Mode, Entry: v.Installation.SourceEntry, DPI: v.Installation.DPI, HideWineTray: v.Installation.HideWineTray}
	}
	if len(env.Registration) > 0 {
		var proof backend.BottlesRegistration
		if e = json.Unmarshal(env.Registration, &proof); e != nil {
			return request, env, e
		}
		request.Windows = &proof
	}
	rows, e := s.Store.List(ctx, "binding")
	if e != nil {
		return request, env, e
	}
	for _, row := range rows {
		var b domain.FileBinding
		if e = json.Unmarshal(row.Data, &b); e != nil {
			return request, env, e
		}
		if b.InstanceID != v.ID {
			continue
		}
		if b.HostPath == "" || b.State == "needs_binding" {
			return request, env, errors.New("instance directories must be rebound before installation or launch")
		}
		request.Bindings = append(request.Bindings, backend.Binding{Host: host.BindingEvidence{Path: b.HostPath, Device: b.Device, Inode: b.Inode, ReadOnly: b.Access == "read"}, Target: b.TargetPath})
	}
	return request, env, nil
}
func (s *Service) validateInstancePlan(ctx context.Context, p *domain.Plan, v domain.AppInstance) error {
	r, env, e := s.request(ctx, v)
	if e != nil {
		return e
	}
	adapter := s.Backend
	switch p.Operation {
	case "instance.install":
		if v.State != "unprepared" {
			return errors.New("installation can only start in a new instance; inspect partial effects first")
		}
		b, e := adapter.PlanInstall(r)
		if e != nil {
			return e
		}
		p.Permissions = append(p.Permissions, b.Warnings...)
		data, _ := json.Marshal(b)
		p.Input = data
		for _, step := range b.Steps {
			params, _ := json.Marshal(step)
			p.Steps = append(p.Steps, domain.Step{ID: domain.NewID(), Kind: step.Kind, Description: step.Reason, Params: params, SafeCancel: step.Kind == "prepare_material"})
		}
	case "instance.verify":
		if r.Recipe.Platform != "linux" || v.State != "needs_inspection" || env.Identity == "" || env.LaunchLease || env.LaunchToken != "" {
			return errors.New("only a previously created Linux installation without an unresolved launch can be verified")
		}
		p.Input, _ = json.Marshal(struct {
			ID       string `json:"id"`
			Identity string `json:"identity"`
		}{v.ID, env.Identity})
		p.DataImpact = []string{"verify the user-repaired entry in this exact managed Linux environment; no installer is replayed", "Distrobox entry may finish first-run initialization; readiness does not certify application behavior"}
		p.Steps = []domain.Step{{ID: domain.NewID(), Kind: "verify_entry", Description: "Verify repaired managed Linux installation", SafeCancel: false}}
	case "instance.launch":
		if v.Installation != nil && v.Installation.Stage != "complete" && v.Installation.Mode != "installer" {
			return errors.New("请先选择并确认安装后的应用入口")
		}
		if r.Recipe.Platform == "windows" && (r.Windows == nil || r.Windows.Control == nil || !r.Windows.ControlledLaunch) {
			return errors.New("Windows Run/Stop requires registered direct Wine mode (winebridge=false) and verified prefix control; configure Bottles then register again")
		}
		if v.State != "ready" || env.Identity == "" {
			return errors.New("instance entry has not been verified")
		}
	case "instance.remove":
		if env.LaunchLease || env.LaunchToken != "" || env.StopRequested {
			return errors.New("stop the application and reconcile its launch before removal")
		}
		if env.Identity == "" {
			return errors.New("no proven managed environment identity")
		}
		if e = s.precheckRemovalHome(ctx, *p, v); e != nil {
			return e
		}
		if r.Recipe.Platform == "windows" {
			p.DataImpact = []string{"permanently removes this managed Bottle, including drive_c application files, settings and saved data", "the separate managed private HOME and archived installers are retained by environment removal"}
		}
		_, e = adapter.PlanRemove(r, env.Identity)
		return e
	}
	return nil
}
func (s *Service) assessBackend(ctx context.Context, raw json.RawMessage) (any, error) {
	var p instanceInput
	if e := decode(raw, &p); e != nil {
		return nil, e
	}
	v, e := get[domain.AppInstance](ctx, s.Store, "instance", p.ID)
	if e != nil {
		return nil, e
	}
	r, _, e := s.request(ctx, v)
	if e != nil {
		return nil, e
	}
	return s.Backend.PlanInstall(r)
}
func (s *Service) inspectInstance(ctx context.Context, raw json.RawMessage) (any, error) {
	var p instanceInput
	if e := decode(raw, &p); e != nil {
		return nil, e
	}
	v, e := get[domain.AppInstance](ctx, s.Store, "instance", p.ID)
	if e != nil {
		return nil, e
	}
	r, env, e := s.request(ctx, v)
	if e != nil {
		return nil, e
	}
	if env.Identity == "" {
		return map[string]any{"instance": v, "environment": env, "state": "unprepared"}, nil
	}
	return s.Backend.Inspect(ctx, r, env.Identity)
}
func (s *Service) executeInstance(ctx context.Context, p domain.Plan) (any, error) {
	v, e := get[domain.AppInstance](ctx, s.Store, "instance", p.InstanceID)
	if e != nil {
		return nil, e
	}
	r, env, e := s.request(ctx, v)
	if e != nil {
		return nil, e
	}
	if p.RecipeRevision != v.RecipeRevision || len(p.ArtifactDigests) != 1 || p.ArtifactDigests[0] != v.OriginalArtifact {
		return nil, errors.New("plan preconditions changed")
	}
	if p.Operation == "instance.install" {
		a := s.Artifacts()
		if _, e = a.Verify(ctx, v.OriginalArtifact); e != nil {
			return nil, e
		}
	}
	if p.Operation == "instance.launch" {
		if r.Recipe.Platform == "windows" && (r.Windows == nil || r.Windows.Control == nil || !r.Windows.ControlledLaunch) {
			return nil, errors.New("Windows launch control evidence missing; re-register direct Wine mode")
		}
		release, claimErr := s.claimLaunch(ctx, v, &env)
		if claimErr != nil {
			return nil, claimErr
		}
		defer release()
		if r.Recipe.Platform == "linux" || r.Recipe.Platform == "windows" && r.Windows != nil && r.Windows.ControlledLaunch {
			return s.executeSupervisedLaunch(ctx, v, r, env)
		}
	}
	adapter := s.Backend
	var steps []backend.Step
	var preflight *backend.InstallPreflight
	switch p.Operation {
	case "instance.install":
		if v.State != "unprepared" {
			return nil, errors.New("instance has prior installation evidence; automatic replay prohibited")
		}
		b, e := adapter.PlanInstall(r)
		if e != nil {
			return nil, e
		}
		frozen, _ := json.Marshal(b)
		if !bytes.Equal(frozen, p.Input) {
			return nil, errors.New("installation plan changed; review a new plan")
		}
		if r.Recipe.Platform == "linux" {
			proof, checkErr := adapter.PreflightInstall(ctx, r)
			if checkErr != nil {
				return map[string]any{"preflight": proof, "required_action": "prepare the missing local image, storage or backend prerequisite; cancel the waiting task before reviewing a new installation plan"}, checkErr
			}
			preflight = &proof
		}
		steps = b.Steps
	case "instance.verify":
		var frozen struct {
			ID       string `json:"id"`
			Identity string `json:"identity"`
		}
		if e := decode(p.Input, &frozen); e != nil {
			return nil, e
		}
		if r.Recipe.Platform != "linux" || v.State != "needs_inspection" || env.LaunchLease || env.LaunchToken != "" || frozen.ID != v.ID || frozen.Identity == "" || frozen.Identity != env.Identity {
			return nil, errors.New("installation verification preconditions changed or an unresolved launch remains")
		}
		steps = []backend.Step{{Kind: "verify_entry", Request: r, ResourceID: env.Identity, Mode: "automatic"}}
	case "instance.launch":
		if v.State != "ready" {
			return nil, errors.New("instance not ready")
		}
		steps = []backend.Step{{Kind: "launch", Request: r, ResourceID: env.Identity, Mode: "automatic"}}
	case "instance.remove":
		if env.LaunchLease || env.LaunchToken != "" || env.StopRequested {
			return nil, errors.New("stop the application and reconcile its launch before removal")
		}
		if e = s.precheckRemovalHome(ctx, p, v); e != nil {
			return nil, e
		}
		b, e := adapter.PlanRemove(r, env.Identity)
		if e != nil {
			return nil, e
		}
		steps = b.Steps
	}
	evidence := []backend.Evidence{}
	if preflight != nil {
		evidence = append(evidence, backend.Evidence{State: "preflight_passed", Preflight: preflight})
	}
	for i, step := range steps {
		if e = ctx.Err(); e != nil {
			return evidence, e
		}
		step.ResourceID = env.Identity
		if step.Mode != "automatic" {
			env.State = "waiting_user"
			_ = s.Store.Put(context.WithoutCancel(ctx), record("environment", env.ID, env))
			return map[string]any{"steps": steps, "evidence": evidence, "required_action": step.Reason}, backend.ErrManual
		}
		// Persist intention before side effects so crashes cannot make a partial
		// installation look unprepared and eligible for blind replay.
		if p.Operation == "instance.install" {
			v.State = "installing"
			if e = s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
				return evidence, e
			}
		}
		if p.Operation == "instance.launch" {
			v.State = "launching"
			if e = s.persistInstanceState(ctx, v); e != nil {
				return evidence, e
			}
		}
		ev, err := adapter.ExecuteStep(ctx, step)
		// Tool output can contain tokens or personal data. Keep status/identity only.
		ev.Result.Stdout = ""
		ev.Result.Stderr = ""
		evidence = append(evidence, ev)
		if ev.ResourceID != "" {
			env.Identity = ev.ResourceID
		}
		if ev.Windows != nil {
			env.Registration, _ = json.Marshal(ev.Windows)
			env.Runtime = r.Recipe.Runner
		}
		env.State = ev.State
		if ev.State == "removed" && err == nil {
			env.LaunchLease = false
		}
		env.CreationEvidence, _ = json.Marshal(evidence)
		persistCtx := context.WithoutCancel(ctx)
		if e = s.Store.Put(persistCtx, record("environment", env.ID, env)); e != nil {
			return evidence, e
		}
		if jid := jobs.ID(ctx); jid != "" {
			b, _ := json.Marshal(evidence)
			if e = s.Store.SetJobResult(persistCtx, jid, b); e != nil {
				return evidence, e
			}
			if e = s.Store.SetJobStep(persistCtx, jid, i+1); e != nil {
				return evidence, e
			}
		}
		if err != nil {
			v.State = "needs_inspection"
			_ = s.persistInstanceState(persistCtx, v)
			return evidence, err
		}
		if ev.State == "entry_verified" || ev.State == "environment_prepared" {
			if e = s.bindingsVerified(persistCtx, v.ID); e != nil {
				return evidence, e
			}
			if v.Installation != nil {
				v.Installation.Stage = "awaiting_completion"
				if v.Installation.Mode == "installer" {
					v.Installation.Stage = "installer_ready"
				}
			}
			v.State = "ready"
			v.Entry = []string{ev.Entry}
			v.Drift = "unknown"
		}
	}
	if p.Operation == "instance.launch" {
		v.State = "ready"
		now := time.Now().UTC()
		v.LastUsed = &now
	}
	if p.Operation == "instance.remove" {
		v.State = "archived"
		env.State = "removed"
	}
	if e = s.persistInstanceState(context.WithoutCancel(ctx), v); e != nil {
		return evidence, e
	}
	return evidence, nil
}
func (s *Service) binding(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		InstanceID string `json:"instance_id"`
		HostPath   string `json:"host_path"`
		TargetPath string `json:"target_path"`
		Access     string `json:"access"`
	}
	if e := decode(raw, &p); e != nil {
		return nil, e
	}
	vinst, e := get[domain.AppInstance](ctx, s.Store, "instance", p.InstanceID)
	if e != nil {
		return nil, e
	}
	if vinst.State != "unprepared" && vinst.State != "needs_runtime_restore" {
		return nil, errors.New("binding changes require a new unprepared instance")
	}
	recipe, e := get[recipe.Recipe](ctx, s.Store, "recipe", vinst.RecipeRevision)
	if e != nil {
		return nil, e
	}
	validTarget := validBindingTarget(recipe.Platform, p.TargetPath)
	if !validTarget || !filepath.IsAbs(p.HostPath) || p.Access != "read" && p.Access != "write" {
		return nil, domain.ErrInvalid
	}
	existing, e := s.Store.List(ctx, "binding")
	if e != nil {
		return nil, e
	}
	boundCount := 0
	for _, row := range existing {
		var other domain.FileBinding
		if e = decode(row.Data, &other); e != nil {
			return nil, e
		}
		if other.InstanceID == vinst.ID {
			boundCount++
		}
		if other.InstanceID == vinst.ID && other.TargetPath == p.TargetPath {
			return nil, errors.New("this application target is already bound")
		}
	}
	if boundCount >= 64 {
		return nil, errors.New("too many instance file bindings")
	}
	evidence, e := host.InspectBinding(p.HostPath, p.Access == "read")
	if e != nil {
		return nil, e
	}
	v := domain.FileBinding{ID: domain.NewID(), InstanceID: p.InstanceID, HostPath: evidence.Path, TargetPath: p.TargetPath, Access: p.Access, State: "awaiting_backend_mapping", Device: evidence.Device, Inode: evidence.Inode}
	return v, s.save(ctx, "binding", v.ID, v, ref("binding", v.ID, "instance", p.InstanceID, "binding"))
}

func (s *Service) rebind(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ID       string `json:"id"`
		HostPath string `json:"host_path"`
	}
	if e := decode(raw, &p); e != nil {
		return nil, e
	}
	b, e := get[domain.FileBinding](ctx, s.Store, "binding", p.ID)
	if e != nil {
		return nil, e
	}
	v, e := get[domain.AppInstance](ctx, s.Store, "instance", b.InstanceID)
	if e != nil {
		return nil, e
	}
	if v.State != "unprepared" && v.State != "needs_runtime_restore" {
		return nil, errors.New("rebind requires an unprepared instance")
	}
	ev, e := host.InspectBinding(p.HostPath, b.Access == "read")
	if e != nil {
		return nil, e
	}
	b.HostPath = ev.Path
	b.Device = ev.Device
	b.Inode = ev.Inode
	b.State = "awaiting_backend_mapping"
	return b, s.Store.Put(ctx, record("binding", b.ID, b))
}
func (s *Service) capability(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		InstanceID      string `json:"instance_id"`
		Capability      string `json:"capability"`
		State           string `json:"state"`
		Evidence        string `json:"evidence"`
		ObservedVersion string `json:"observed_version"`
	}
	if e := decode(raw, &p); e != nil {
		return nil, e
	}
	if p.State != "passed" && p.State != "failed" && p.State != "unknown" {
		return nil, domain.ErrInvalid
	}
	if required(p.Capability) != nil || required(p.Evidence) != nil {
		return nil, domain.ErrInvalid
	}
	v, e := get[domain.AppInstance](ctx, s.Store, "instance", p.InstanceID)
	if e != nil {
		return nil, e
	}
	if v.State == "forgotten" {
		return nil, domain.ErrNotFound
	}
	env, e := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	if e != nil {
		return nil, e
	}
	c := domain.CapabilityResult{ID: domain.NewID(), InstanceID: v.ID, Capability: p.Capability, State: p.State, Host: "linux/amd64", Runtime: env.Runtime, Version: p.ObservedVersion, Evidence: "User-reported: " + p.Evidence, CheckedAt: time.Now().UTC()}
	return c, s.save(ctx, "capability", c.ID, c, ref("capability", c.ID, "instance", v.ID, "instance"))
}

type manualRegistrationInput struct {
	ID             string `json:"id"`
	BottlePath     string `json:"bottle_path"`
	ExecutablePath string `json:"executable_path"`
	ProgramName    string `json:"program_name"`
}

func (s *Service) registerManual(ctx context.Context, p domain.Plan) (any, error) {
	var in manualRegistrationInput
	if e := decode(p.Input, &in); e != nil {
		return nil, e
	}
	v, e := get[domain.AppInstance](ctx, s.Store, "instance", p.InstanceID)
	if e != nil {
		return nil, e
	}
	if in.ID != v.ID || p.RecipeRevision != v.RecipeRevision {
		return nil, errors.New("registration preconditions changed")
	}
	r, env, e := s.request(ctx, v)
	if e != nil {
		return nil, e
	}
	occupied := env.LaunchLease || env.LaunchToken != "" || env.StopRequested || v.State == "launching" || v.State == "running" || env.State == "running"
	if occupied && !s.registrationLaunchRejected(ctx, v, env) {
		return nil, errors.New("请先停止应用；不能重新登记仍在运行或状态不明的启动任务")
	}
	proof, e := s.Backend.VerifyManualBottles(ctx, r, backend.ManualBottlesInput{BottlePath: in.BottlePath, ExecutablePath: in.ExecutablePath, ProgramName: in.ProgramName})
	if e != nil {
		return nil, e
	}
	// Existing registrations are refreshed only after the newly verified target
	// is proven stopped. Never persist fresh scope evidence for a running app.
	if r.Windows != nil {
		next := r
		next.Windows = proof.Windows
		if _, e = s.Backend.ConfirmStopped(ctx, next, proof.ResourceID); e != nil {
			return nil, e
		}
	}
	env.LaunchLease = false
	env.LaunchToken = ""
	env.StopRequested = false
	proof.Result.Stdout = ""
	proof.Result.Stderr = ""
	if e = s.bindingsVerified(ctx, v.ID); e != nil {
		return nil, e
	}
	env.Registration, _ = json.Marshal(proof.Windows)
	env.Identity = proof.ResourceID
	env.State = proof.State
	env.Runtime = r.Recipe.Runner
	env.CreationEvidence, _ = json.Marshal(proof)
	if e = s.Store.Put(ctx, record("environment", env.ID, env)); e != nil {
		return nil, e
	}
	v.State = "ready"
	v.Entry = []string{proof.Entry}
	if e = s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
		return nil, e
	}
	rows, e := s.Store.List(ctx, "job")
	if e != nil {
		return proof, e
	}
	for _, row := range rows {
		var j domain.Job
		if json.Unmarshal(row.Data, &j) != nil || j.State != domain.WaitingUser {
			continue
		}
		prior, e := get[domain.Plan](ctx, s.Store, "plan", j.PlanID)
		if e != nil {
			return proof, e
		}
		if prior.InstanceID == v.ID && prior.Operation == "instance.install" {
			if e = s.Store.TransitionJob(ctx, j.ID, domain.WaitingUser, domain.Succeeded, "manual installation verified by registration plan "+p.ID); e != nil {
				return proof, e
			}
		}
	}
	return proof, nil
}

// persistInstanceState changes execution-owned fields only; user labels and
// favorites may be edited while an application's worker remains alive.
func (s *Service) persistInstanceState(ctx context.Context, v domain.AppInstance) error {
	return s.Store.UpdateRecord(ctx, "instance", v.ID, func(r *domain.Record) error {
		var current domain.AppInstance
		if e := json.Unmarshal(r.Data, &current); e != nil {
			return e
		}
		current.State = v.State
		current.Installation = v.Installation
		current.Entry = v.Entry
		current.Drift = v.Drift
		current.ObservedInstalledVersion = v.ObservedInstalledVersion
		current.LastUsed = v.LastUsed
		r.Data, _ = json.Marshal(current)
		return nil
	})
}
