// Package service exposes validated management use cases shared by CLI and RPC.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/catalog"
	"github.com/ZZH1124Prime/ZaH/internal/dependencies"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/host"
	"github.com/ZZH1124Prime/ZaH/internal/infra"
	"github.com/ZZH1124Prime/ZaH/internal/migration"
	"github.com/ZZH1124Prime/ZaH/internal/storage"
)

// Service owns only management data. Backends are invoked solely by workers.
type Service struct {
	Dependencies dependencies.Installer
	Store        *storage.Store
	Paths        infra.Paths
	Backend      backend.Adapter
	// LaunchDiagnostics is a worker-owned, private, size-bounded log; never an RPC input.
	LaunchDiagnostics io.Writer
	supervisorBinary  string // Test injection only; never exposed through RPC.
}

func Open(paths infra.Paths) (*Service, error) {
	if err := paths.Ensure(); err != nil {
		return nil, err
	}
	s, err := storage.Open(filepath.Join(paths.Data, "catalog.sqlite"))
	if err != nil {
		return nil, err
	}
	return &Service{Store: s, Paths: paths}, nil
}
func (s *Service) Close() error { return s.Store.Close() }
func (s *Service) Artifacts() catalog.Store {
	return catalog.Store{Root: filepath.Join(s.Paths.Data, "artifacts", "sha256")}
}
func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return fmt.Errorf("%w: %v", domain.ErrInvalid, e)
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return fmt.Errorf("%w: trailing JSON", domain.ErrInvalid)
	}
	return nil
}
func record(kind, id string, v any) domain.Record {
	b, _ := json.Marshal(v)
	return domain.Record{Kind: kind, ID: id, Data: b}
}
func (s *Service) save(ctx context.Context, kind, id string, v any, refs ...domain.ResourceRef) error {
	return s.Store.PutWithReferences(ctx, record(kind, id, v), refs)
}
func ref(fromKind, fromID, toKind, toID, role string) domain.ResourceRef {
	return domain.ResourceRef{FromKind: fromKind, FromID: fromID, ToKind: toKind, ToID: toID, Role: role}
}
func required(v string) error {
	if strings.TrimSpace(v) == "" || len(v) > 512 || strings.ContainsAny(v, "\x00\r\n") {
		return fmt.Errorf("%w: empty/oversized/invalid text", domain.ErrInvalid)
	}
	return nil
}
func get[T any](ctx context.Context, s *storage.Store, kind, id string) (T, error) {
	var v T
	r, e := s.Get(ctx, kind, id)
	if e == nil {
		e = json.Unmarshal(r.Data, &v)
	}
	return v, e
}

// Handle is the complete RPC allowlist; there is no arbitrary record/SQL/shell API.
func (s *Service) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "installation.configure", "release.icon.set", "family.create", "release.create", "catalog.rename", "catalog.delete", "recipe.add", "instance.create", "binding.register", "binding.rebind", "capability.record", "instance.preferences", "plan.create", "plan.discard", "job.submit":
		l, e := infra.AcquireLock(filepath.Join(s.Paths.Runtime, "modify.lock"))
		if e != nil {
			return nil, fmt.Errorf("%w: another modification is active", domain.ErrConflict)
		}
		defer l.Close()
	}
	switch method {
	case "installation.location":
		var in instanceInput
		if e := decode(params, &in); e != nil {
			return nil, e
		}
		v, e := get[domain.AppInstance](ctx, s.Store, "instance", in.ID)
		if e != nil {
			return nil, e
		}
		r, _, e := s.request(ctx, v)
		if e != nil {
			return nil, e
		}
		location, e := s.Backend.InstallationLocation(r)
		if e != nil {
			return nil, e
		}
		return map[string]string{"path": location}, nil
	case "installation.inspect":
		var in struct {
			ArtifactID string `json:"artifact_id"`
		}
		if e := decode(params, &in); e != nil {
			return nil, e
		}
		return s.inspectMaterial(ctx, in.ArtifactID)
	case "installation.options":
		var in struct {
			Platform string `json:"platform"`
		}
		if e := decode(params, &in); e != nil {
			return nil, e
		}
		return s.Backend.InstallOptions(ctx, in.Platform)
	case "installation.configure":
		return s.configureInstallation(ctx, params)
	case "installation.programs":
		var in instanceInput
		if e := decode(params, &in); e != nil {
			return nil, e
		}
		v, e := get[domain.AppInstance](ctx, s.Store, "instance", in.ID)
		if e != nil {
			return nil, e
		}
		r, env, e := s.request(ctx, v)
		if e != nil {
			return nil, e
		}
		lock, e := infra.AcquireLock(filepath.Join(s.Paths.Runtime, "environment-"+env.ID+".lock"))
		if e != nil {
			return nil, e
		}
		defer lock.Close()
		if r.Recipe.Platform == "linux" {
			return s.Backend.LinuxProgramCandidates(ctx, r, env.Identity)
		}
		candidates, e := s.Backend.ProgramCandidates(ctx, r)
		if e != nil {
			return nil, e
		}
		installer, _ := backend.InstallerPath(r)
		visible := candidates[:0]
		for _, candidate := range candidates {
			if candidate.Path != installer {
				visible = append(visible, candidate)
			}
		}
		return visible, nil

	case "host.inspect":
		return host.InspectRuntime(ctx), nil
	case "system.info":
		return map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "protocol": 1, "desktop_contract": 7, "paths": s.Paths, "runtime_validation": "not_performed"}, nil
	case "catalog.list":
		var p struct {
			Kind string `json:"kind"`
		}
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		switch p.Kind {
		case "family", "release", "instance", "environment", "artifact", "recipe", "binding", "capability", "plan", "job":
			return s.Store.List(ctx, p.Kind)
		default:
			return nil, domain.ErrInvalid
		}
	case "catalog.get":
		var p struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		}
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		return s.Store.Get(ctx, p.Kind, p.ID)
	case "events.resume":
		var p struct {
			After *int64          `json:"after"`
			Limit json.RawMessage `json:"limit"`
		}
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		if p.After != nil && (*p.After < 0 || *p.After > 9007199254740991) {
			return nil, domain.ErrInvalid
		}
		limit := 100
		if len(p.Limit) != 0 {
			var requested int
			if e := json.Unmarshal(p.Limit, &requested); e != nil || requested < 1 || requested > 500 {
				return nil, domain.ErrInvalid
			}
			limit = requested
		}
		return s.Store.ResumeEvents(ctx, p.After, limit)
	case "events.read":
		var p struct {
			After int64 `json:"after"`
			Limit int   `json:"limit"`
		}
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		if p.After < 0 {
			return nil, domain.ErrInvalid
		}
		if p.Limit <= 0 {
			p.Limit = 100
		}
		if p.Limit > 500 {
			p.Limit = 500
		}
		return s.Store.Events(ctx, p.After, p.Limit)
	case "catalog.snapshot":
		out := map[string]any{}
		for _, kind := range []string{"family", "release", "instance", "environment", "artifact", "binding", "capability", "job"} {
			r, e := s.Store.List(ctx, kind)
			if e != nil {
				return nil, e
			}
			out[kind] = r
		}
		return out, nil
	case "catalog.rename":
		var p struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		if e := required(p.Name); e != nil {
			return nil, e
		}
		switch p.Kind {
		case "family":
			v, e := get[domain.AppFamily](ctx, s.Store, p.Kind, p.ID)
			if e != nil {
				return nil, e
			}
			v.Name = p.Name
			return v, s.Store.Put(ctx, record(p.Kind, p.ID, v))
		case "instance":
			v, e := get[domain.AppInstance](ctx, s.Store, p.Kind, p.ID)
			if e != nil {
				return nil, e
			}
			if v.State == "forgotten" {
				return nil, domain.ErrNotFound
			}
			v.Name = p.Name
			return v, s.Store.Put(ctx, record(p.Kind, p.ID, v))
		default:
			return nil, domain.ErrInvalid
		}
	case "catalog.delete":
		var p struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		}
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		switch p.Kind {
		case "family", "release", "recipe":
			return map[string]string{"id": p.ID}, s.Store.Delete(ctx, p.Kind, p.ID)
		default:
			return nil, errors.New("this resource requires a reviewed cleanup plan")
		}
	case "family.create":
		var p struct {
			Name     string `json:"name"`
			Platform string `json:"platform,omitempty"`
		}
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		if e := required(p.Name); e != nil {
			return nil, e
		}
		if p.Platform != "" && p.Platform != "linux" && p.Platform != "windows" {
			return nil, domain.ErrInvalid
		}
		id := domain.NewID()
		v := domain.AppFamily{ID: id, StableID: id, Platform: p.Platform, Name: p.Name}
		return v, s.save(ctx, "family", v.ID, v)
	case "release.icon.set":
		return s.setReleaseIcon(ctx, params)
	case "release.icon.get":
		return s.releaseIcon(ctx, params)
	case "release.create":
		var p struct {
			FamilyID  string   `json:"family_id"`
			Platform  string   `json:"platform"`
			Version   string   `json:"version"`
			Arch      string   `json:"arch"`
			Artifacts []string `json:"artifacts"`
		}
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		if p.Platform != "linux" && p.Platform != "windows" {
			return nil, domain.ErrInvalid
		}
		if p.Arch != "amd64" && p.Arch != "unknown" {
			return nil, fmt.Errorf("unsupported architecture")
		}
		if _, e := s.Store.Get(ctx, "family", p.FamilyID); e != nil {
			return nil, e
		}
		if len(p.Artifacts) == 0 {
			return nil, errors.New("release requires archived materials")
		}
		v := domain.AppRelease{ID: domain.NewID(), FamilyID: p.FamilyID, Platform: p.Platform, Version: p.Version, Arch: p.Arch, Artifacts: p.Artifacts}
		refs := []domain.ResourceRef{ref("release", v.ID, "family", v.FamilyID, "family")}
		for _, digest := range p.Artifacts {
			if _, e := s.Store.Get(ctx, "artifact", digest); e != nil {
				return nil, e
			}
			refs = append(refs, ref("release", v.ID, "artifact", digest, "material"))
		}
		for _, digest := range v.Artifacts {
			metadata, e := get[catalog.Artifact](ctx, s.Store, "artifact", digest)
			if e != nil || metadata.Size > 32<<20 {
				continue
			}
			a, e := s.Artifacts().Verify(ctx, digest)
			if e != nil {
				continue
			}
			png, e := catalog.ArchiveIcon(a.Path)
			if e != nil {
				continue
			}
			icon, e := s.storeIcon(ctx, png)
			if e != nil {
				continue
			}
			v.IconSHA256 = icon
			refs = append(refs, ref("release", v.ID, "artifact", icon, "icon"))
			break
		}
		return v, s.save(ctx, "release", v.ID, v, refs...)
	case "plan.create":
		return s.plan(ctx, params)
	case "plan.discard":
		var in struct {
			ID string `json:"id"`
		}
		if e := decode(params, &in); e != nil {
			return nil, e
		}
		return s.Store.DiscardPlan(ctx, in.ID)
	case "job.submit":
		var p struct {
			Confirmed bool   `json:"host_changes_confirmed,omitempty"`
			PlanID    string `json:"plan_id"`
			Key       string `json:"idempotency_key"`
		}
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		if e := required(p.Key); e != nil {
			return nil, e
		}
		plan, e := get[domain.Plan](ctx, s.Store, "plan", p.PlanID)
		if e != nil {
			return nil, e
		}
		if plan.Operation == "host.dependencies.install" && !p.Confirmed {
			return nil, errors.New("explicit host dependency installation confirmation required")
		}
		if p.Confirmed && plan.Operation != "host.dependencies.install" {
			return nil, domain.ErrInvalid
		}
		j, _, e := s.Store.SubmitJob(ctx, p.PlanID, p.Key, p.Confirmed)
		return j, e
	case "job.cancel":
		var p struct {
			ID string `json:"id"`
		}
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		j, e := get[domain.Job](ctx, s.Store, "job", p.ID)
		if e != nil {
			return nil, e
		}
		target := domain.CancelRequested
		if j.State == domain.Queued || j.State == domain.Interrupted || j.State == domain.WaitingUser {
			target = domain.Cancelled
		}
		if j.State.Terminal() {
			return j, nil
		}
		e = s.Store.TransitionJob(ctx, j.ID, j.State, target, "cancellation requested; completed external effects are retained")
		if e != nil {
			return nil, e
		}
		return get[domain.Job](ctx, s.Store, "job", p.ID)
	case "artifact.verify":
		var p struct {
			SHA256 string `json:"sha256"`
		}
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		a := s.Artifacts()
		return a.Verify(ctx, p.SHA256)
	case "bundle.inspect":
		var p struct {
			Path string `json:"path"`
		}
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		inspection, e := migration.Inspect(ctx, p.Path, migration.Limits{})
		return compactInspection(inspection), e
	case "recipe.add":
		return s.addRecipe(ctx, params)
	case "instance.create":
		return s.createInstance(ctx, params)
	case "backend.assess":
		return s.assessBackend(ctx, params)
	case "instance.inspect":
		return s.inspectInstance(ctx, params)
	case "instance.desktopPlan":
		var p instanceInput
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		return s.DesktopPlan(ctx, p.ID)
	case "binding.rebind":
		return s.rebind(ctx, params)
	case "capability.record":
		return s.capability(ctx, params)
	case "binding.register":
		return s.binding(ctx, params)
	case "instance.preferences":
		var p struct {
			ID       string `json:"id"`
			Favorite bool   `json:"favorite"`
			Default  bool   `json:"default"`
		}
		if e := decode(params, &p); e != nil {
			return nil, e
		}
		v, e := get[domain.AppInstance](ctx, s.Store, "instance", p.ID)
		if e != nil {
			return nil, e
		}
		if v.State == "forgotten" {
			return nil, domain.ErrNotFound
		}
		v.Favorite = p.Favorite
		if e = s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
			return nil, e
		}
		if p.Default {
			rel, e := get[domain.AppRelease](ctx, s.Store, "release", v.ReleaseID)
			if e != nil {
				return nil, e
			}
			fam, e := get[domain.AppFamily](ctx, s.Store, "family", rel.FamilyID)
			if e != nil {
				return nil, e
			}
			fam.DefaultInstanceID = v.ID
			if e = s.Store.Put(ctx, record("family", fam.ID, fam)); e != nil {
				return nil, e
			}
		}
		return v, nil
	default:
		return nil, fmt.Errorf("unknown method: %s", method)
	}
}

type planRequest struct {
	Operation string          `json:"operation"`
	Input     json.RawMessage `json:"input"`
}
type artifactInput struct {
	Path string `json:"path"`
}
type instanceInput struct {
	ID string `json:"id"`
}
type bundleInput struct {
	Directory   string                  `json:"directory,omitempty"`
	Parent      *domain.ManagedDataRoot `json:"parent,omitempty"`
	Merge       bool                    `json:"merge,omitempty"`
	Path        string                  `json:"path"`
	Fingerprint string                  `json:"fingerprint,omitempty"`
}

func (s *Service) plan(ctx context.Context, raw json.RawMessage) (any, error) {
	var req planRequest
	if e := decode(raw, &req); e != nil {
		return nil, e
	}
	p := domain.Plan{ID: domain.NewID(), Operation: req.Operation, Input: req.Input, CreatedAt: time.Now().UTC()}
	switch req.Operation {
	case "environment.rebuild":
		return s.planRebuild(ctx, req)
	case "host.dependencies.install":
		return s.planDependencies(ctx, req)
	case "installation.tools":
		return s.planInstallationTool(ctx, req)
	case "installation.finish", "installation.maintenance":
		return s.planEntry(ctx, req)
	case "instance.stop":
		return s.planStopOne(ctx, req)
	case "applications.stopAll":
		return s.planStopAll(ctx, req)
	case "bundle.encrypt", "bundle.decrypt":
		return s.planEncryption(ctx, req)
	case "runtime.remove":
		return s.planRuntimeCleanup(ctx, req)
	case "workbench.export":
		return s.planWorkbench(ctx, req)
	case "environment.export", "environment.restore", "environment.load", "environment.verify":
		return s.planEnvironment(ctx, req)
	case "artifact.remove", "instance.forget", "instance.data.remove":
		var in struct {
			ID                 string `json:"id"`
			ColdConfirmed      bool   `json:"cold_confirmed,omitempty"`
			ConfirmPrivateData bool   `json:"confirm_private_data,omitempty"`
		}
		if e := decode(req.Input, &in); e != nil {
			return nil, e
		}
		raw, _ := json.Marshal(map[string]any{"operation": req.Operation, "id": in.ID, "cold_confirmed": in.ColdConfirmed, "confirm_private_data": in.ConfirmPrivateData})
		next, e := s.PlanCleanup(ctx, raw)
		if e != nil {
			return nil, e
		}
		p = next
	case "artifact.import":
		var in artifactInput
		if e := decode(req.Input, &in); e != nil {
			return nil, e
		}
		if !filepath.IsAbs(in.Path) {
			return nil, errors.New("source must be absolute")
		}
		st, e := os.Lstat(in.Path)
		if e != nil {
			return nil, e
		}
		if !st.Mode().IsRegular() {
			return nil, errors.New("source must be a regular file")
		}
		p.Preconditions = []string{"source is revalidated by worker; independent copy digest verified before commit"}
		p.DataImpact = []string{"adds persistent installation material; source is unchanged"}
	case "instance.install", "instance.launch", "instance.remove", "instance.verify":
		var in instanceInput
		if req.Operation == "instance.remove" {
			var removal removeInstanceInput
			if e := decode(req.Input, &removal); e != nil {
				return nil, e
			}
			in.ID = removal.ID
		} else if e := decode(req.Input, &in); e != nil {
			return nil, e
		}
		v, e := get[domain.AppInstance](ctx, s.Store, "instance", in.ID)
		if e != nil {
			return nil, e
		}
		p.InstanceID = v.ID
		p.EnvironmentIDs = []string{v.EnvironmentID}
		p.RecipeRevision = v.RecipeRevision
		p.ArtifactDigests = []string{v.OriginalArtifact}
		p.Preconditions = []string{"managed ownership and backend identity must match", "no conflicting operation", "archive digest and recipe revision are checked again"}
		p.Permissions = []string{"application has host integration; Distrobox is not a security sandbox"}
		p.DataImpact = []string{"only named managed instance/environment may change", "private data and installation material retained on environment removal"}
		if e = s.validateInstancePlan(ctx, &p, v); e != nil {
			return nil, e
		}
	case "instance.register":
		var in manualRegistrationInput
		if e := decode(req.Input, &in); e != nil {
			return nil, e
		}
		v, e := get[domain.AppInstance](ctx, s.Store, "instance", in.ID)
		if e != nil {
			return nil, e
		}
		r, _, e := s.request(ctx, v)
		if e != nil {
			return nil, e
		}
		if r.Recipe.Platform != "windows" {
			return nil, errors.New("Windows registration requires a Windows instance")
		}
		if !filepath.IsAbs(in.BottlePath) || !filepath.IsAbs(in.ExecutablePath) {
			return nil, domain.ErrInvalid
		}
		p.InstanceID = v.ID
		p.EnvironmentIDs = []string{v.EnvironmentID}
		p.ArtifactDigests = []string{v.OriginalArtifact}
		p.RecipeRevision = v.RecipeRevision
		p.DataImpact = []string{"verify user-completed Bottles installation against upstream catalog and installed PE entry", "no installer is executed; readiness only means entry verification, not application compatibility"}
	case "bundle.import", "bundle.restore", "bundle.export":
		var in bundleInput
		if e := decode(req.Input, &in); e != nil {
			return nil, e
		}
		if !filepath.IsAbs(in.Path) {
			return nil, domain.ErrInvalid
		}
		if req.Operation != "bundle.export" {
			inspection, e := migration.Inspect(ctx, in.Path, migration.Limits{})
			if e != nil {
				return nil, e
			}
			in.Fingerprint, e = bundleIdentity(inspection)
			if e != nil {
				return nil, e
			}
			p.Input, _ = json.Marshal(in)
		} else {
			if filepath.Ext(in.Path) != ".zahbundle" {
				return nil, errors.New("bundle destination must end .zahbundle")
			}
			if _, e := os.Lstat(in.Path); !os.IsNotExist(e) {
				return nil, errors.New("destination already exists or is unavailable")
			}
		}
		p.DataImpact = []string{"catalog and installation materials ONLY; installed runtimes and private/shared application data are excluded", "restored instances require reinstallation and directory rebinding; existing identities are preserved"}
		p.Preconditions = []string{"archive hashes and size limits rechecked by worker"}
	default:
		return nil, fmt.Errorf("unsupported operation %q", req.Operation)
	}
	if len(p.Steps) == 0 {
		p.Steps = []domain.Step{{ID: domain.NewID(), Kind: p.Operation, Description: strings.Join(p.DataImpact, "; "), Params: p.Input, SafeCancel: p.Operation == "artifact.import" || p.Operation == "bundle.import"}}
	}
	refs := []domain.ResourceRef{}
	for _, d := range p.ArtifactDigests {
		refs = append(refs, ref("plan", p.ID, "artifact", d, "input"))
	}
	if p.InstanceID != "" {
		refs = append(refs, ref("plan", p.ID, "instance", p.InstanceID, "target"))
	}
	return p, s.save(ctx, "plan", p.ID, p, refs...)
}

// Execute is called only after the worker holds the global modification lock.
func (s *Service) Execute(ctx context.Context, p domain.Plan) (result any, err error) {
	defer func() { result = compactExecutionResult(result) }()
	switch p.Operation {
	case "environment.rebuild":
		return s.executeRebuild(ctx, p)
	case "host.dependencies.install":
		return s.executeDependencies(ctx, p)
	case "instance.stop":
		return s.executeStopOne(ctx, p)
	case "applications.stopAll":
		return s.executeStopAll(ctx, p)
	case "bundle.encrypt", "bundle.decrypt":
		return s.executeEncryption(ctx, p)
	case "runtime.remove":
		return s.executeRuntimeCleanup(ctx, p)
	case "workbench.export":
		return s.executeWorkbench(ctx, p)
	case "environment.export", "environment.restore", "environment.load", "environment.verify":
		return s.executeEnvironment(ctx, p)
	case "artifact.remove", "instance.forget", "instance.data.remove":
		return s.ExecuteCleanup(ctx, p)
	case "artifact.import":
		var in artifactInput
		if e := decode(p.Input, &in); e != nil {
			return nil, e
		}
		store := s.Artifacts()
		a, e := store.Import(ctx, in.Path)
		if e != nil {
			return nil, e
		}
		if old, e := get[catalog.Artifact](ctx, s.Store, "artifact", a.SHA256); e == nil {
			a.Sources = old.Sources
			if len(a.Sources) == 0 {
				a.Sources = append(a.Sources, old.Source)
			}
			a.Sources = append(a.Sources, a.Source)
			a.Source = old.Source
		} else if !errors.Is(e, domain.ErrNotFound) {
			return nil, e
		}
		if e = s.save(ctx, "artifact", a.SHA256, a); e != nil {
			return nil, e
		}
		return a, nil
	case "bundle.export":
		var in bundleInput
		if e := decode(p.Input, &in); e != nil {
			return nil, e
		}
		return s.ExportCatalogBundle(ctx, in.Path)
	case "bundle.restore":
		var in bundleInput
		if e := decode(p.Input, &in); e != nil {
			return nil, e
		}
		if in.Fingerprint == "" {
			return nil, errors.New("reviewed bundle identity required")
		}
		if in.Directory != "" {
			return nil, errors.New("catalog-only archive has no environment data directory")
		}
		return s.RestoreCatalogBundle(withBundleIdentity(ctx, in.Fingerprint), in.Path, RestoreOptions{Merge: in.Merge})
	case "bundle.import":
		var in bundleInput
		if e := decode(p.Input, &in); e != nil {
			return nil, e
		}
		target := filepath.Join(s.Paths.Data, "imports", p.ID)
		if e := os.MkdirAll(filepath.Dir(target), 0700); e != nil {
			return nil, e
		}
		i, e := migration.Import(ctx, in.Path, target, migration.Limits{})
		if e != nil {
			return nil, e
		}
		if e = verifyBundleIdentity(i, in.Fingerprint); e != nil {
			_ = os.RemoveAll(target)
			return nil, e
		}
		return map[string]any{"inspection": i, "staging_path": target, "state": "awaiting_restore_plan", "note": "package verified; no application has been registered as restored"}, nil
	case "installation.tools":
		return s.executeInstallationTool(ctx, p)
	case "installation.finish", "installation.maintenance":
		return s.executeEntry(ctx, p)
	case "instance.register":
		return s.registerManual(ctx, p)
	case "instance.install", "instance.launch", "instance.remove", "instance.verify":
		return s.executeInstance(ctx, p)
	default:
		return nil, fmt.Errorf("unsupported operation %q", p.Operation)
	}
}
