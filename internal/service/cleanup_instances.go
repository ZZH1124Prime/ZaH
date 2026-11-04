package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

// Only management's combined environment/data deletion opts into this early check.
// Environment-only removal keeps its existing contract; no data is deleted here.
type removeInstanceInput struct {
	ID                  string `json:"id"`
	PrecheckPrivateData bool   `json:"precheck_private_data,omitempty"`
}

func (s *Service) precheckRemovalHome(ctx context.Context, p domain.Plan, v domain.AppInstance) error {
	var input removeInstanceInput
	if e := decode(p.Input, &input); e != nil {
		return e
	}
	if input.ID != v.ID {
		return domain.ErrInvalid
	}
	if !input.PrecheckPrivateData {
		return nil
	}
	// Runtime absence cannot be required until after removal. Check what can be
	// proved now, and retain the full absence/tree/writer checks at data removal.
	tree, e := s.inspectInstanceCleanupTree(ctx, v)
	if e == nil {
		e = ensureNoCleanupWriters(ctx, v.PrivateData, tree, "/proc")
	}
	if e != nil {
		return fmt.Errorf("删除预检未通过，尚未删除此应用实例的环境或数据：%w", e)
	}
	return nil
}

type instanceCleanupIntent struct {
	ID                 string `json:"id"`
	EnvironmentID      string `json:"environment_id"`
	MetadataSHA256     string `json:"metadata_sha256"`
	RestoreState       string `json:"restore_state"`
	ColdConfirmed      bool   `json:"cold_confirmed"`
	ConfirmPrivateData bool   `json:"confirm_private_data"`
	TreeSHA256         string `json:"tree_sha256,omitempty"`
	Entries            int    `json:"entries,omitempty"`
	Bytes              int64  `json:"bytes,omitempty"`
}

// cleanupChecks makes OS evidence collection testable without mutating a global
// hook or weakening the production use of Linux /proc.
type cleanupChecks struct{ procRoot string }

func safeInstanceID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, e := hex.DecodeString(id)
	return e == nil && strings.ToLower(id) == id
}
func cleanupMetadata(v domain.AppInstance, env domain.Environment) (string, error) {
	v.Name = ""
	v.Favorite = false
	b, e := json.Marshal(struct {
		Instance    domain.AppInstance
		Environment domain.Environment
	}{v, env})
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
func (s *Service) planInstanceCleanup(ctx context.Context, operation, id string, cold, confirmed bool, checks cleanupChecks) (domain.Plan, error) {
	p := domain.Plan{ID: domain.NewID(), Operation: operation, InstanceID: id, CreatedAt: time.Now().UTC()}
	if !safeInstanceID(id) || (operation != "instance.forget" && operation != "instance.data.remove") {
		return p, domain.ErrInvalid
	}
	v, e := get[domain.AppInstance](ctx, s.Store, "instance", id)
	if e != nil {
		return p, e
	}
	env, e := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	if e != nil {
		return p, e
	}
	if operation == "instance.forget" && v.State == "forgotten" {
		return p, fmt.Errorf("%w: instance is already forgotten", domain.ErrConflict)
	}
	if operation == "instance.data.remove" && (!cold || !confirmed) {
		return p, errors.New("private-data removal requires cold_confirmed and confirm_private_data")
	}
	if e = s.checkInstanceCleanupReferences(ctx, v, env, p.ID); e != nil {
		return p, e
	}
	if e = s.proveInstanceInactive(ctx, v, env, p.ID); e != nil {
		return p, e
	}
	restoreState := v.State
	if v.State == "data_removal_pending" {
		if operation != "instance.data.remove" {
			return p, errors.New("finish or inspect pending data removal before forgetting")
		}
		restoreState, e = s.pendingCleanupOriginalState(ctx, v.ID)
		if e != nil {
			return p, e
		}
	}
	intent := instanceCleanupIntent{ID: id, EnvironmentID: env.ID, RestoreState: restoreState, ColdConfirmed: cold, ConfirmPrivateData: confirmed}
	intent.MetadataSHA256, e = cleanupMetadata(v, env)
	if e != nil {
		return p, e
	}
	p.EnvironmentIDs = []string{env.ID}
	if operation == "instance.data.remove" {
		tree, e := s.inspectInstanceCleanupTree(ctx, v)
		if e != nil {
			return p, e
		}
		if e = s.provePrivateDataRuntimeAbsent(ctx, v, env, tree); e != nil {
			return p, e
		}
		if e = ensureNoCleanupWriters(ctx, v.PrivateData, tree, checks.procRoot); e != nil {
			return p, e
		}
		intent.TreeSHA256, e = cleanupTreeDigest(tree)
		if e != nil {
			return p, e
		}
		intent.Entries = len(tree)
		for _, entry := range tree {
			if entry.Kind == "file" {
				intent.Bytes += entry.Size
			}
		}
		p.DataImpact = []string{"permanently removes only this instance's managed private HOME, including personal files and login/configuration data", "external symlink targets, host bindings and their restored copies outside HOME, archived installers and runtime layers are preserved", "failure can leave a partially deleted HOME; review the failed job and create a new cleanup plan"}
		p.Preconditions = []string{"explicit confirmation that external writers/upstream tools are stopped", "no live runtime lease; never-initialized evidence or verified Linux removal plus live absence check", "no live same-user process has a file/cwd/mapping into the private tree; no mountpoints or changed entries"}
	} else {
		p.DataImpact = []string{"removes instance from the active catalog and clears its family default and favorites", "keeps an audit tombstone and all private files, original installation material, runtime evidence and external files", "detaches binding/capability metadata and live resource references in a single database transaction"}
		p.Preconditions = []string{"no runtime lease, unresolved execution or active/unsubmitted dependent plan", "runtime inactivity is established from creation/execution evidence; removed Linux runtime is checked absent again"}
	}
	p.Input, e = json.Marshal(intent)
	if e != nil {
		return p, e
	}
	p.Steps = []domain.Step{{ID: domain.NewID(), Kind: operation, Description: strings.Join(p.DataImpact, "; "), Params: p.Input, SafeCancel: false}}
	return p, nil
}

func (s *Service) checkInstanceCleanupReferences(ctx context.Context, v domain.AppInstance, env domain.Environment, ownPlan string) error {
	jobs, e := s.Store.List(ctx, "job")
	if e != nil {
		return e
	}
	hasJobs := map[string]bool{}
	active := map[string]bool{}
	for _, r := range jobs {
		var j domain.Job
		if e = json.Unmarshal(r.Data, &j); e != nil {
			return e
		}
		hasJobs[j.PlanID] = true
		if !j.State.Terminal() {
			active[j.PlanID] = true
		}
	}
	for _, target := range []struct{ kind, id string }{{"instance", v.ID}, {"environment", env.ID}} {
		refs, e := s.Store.References(ctx, target.kind, target.id)
		if e != nil {
			return e
		}
		for _, ref := range refs {
			if ref.FromKind == "plan" {
				if ref.FromID == ownPlan {
					continue
				}
				if hasJobs[ref.FromID] && !active[ref.FromID] {
					continue
				}
				return fmt.Errorf("%w: pending plan protects cleanup target", domain.ErrReferenced)
			}
			if target.kind == "environment" && ref.FromKind == "instance" && ref.FromID == v.ID {
				continue
			}
			if target.kind == "instance" {
				switch ref.FromKind {
				case "binding":
					b, e := get[domain.FileBinding](ctx, s.Store, "binding", ref.FromID)
					if e == nil && b.InstanceID == v.ID {
						continue
					}
				case "capability":
					c, e := get[domain.CapabilityResult](ctx, s.Store, "capability", ref.FromID)
					if e == nil && c.InstanceID == v.ID {
						continue
					}
				case "family":
					f, e := get[domain.AppFamily](ctx, s.Store, "family", ref.FromID)
					if e == nil && f.DefaultInstanceID == v.ID {
						continue
					}
				}
			}
			return fmt.Errorf("%w: cleanup target is shared or referenced", domain.ErrReferenced)
		}
	}
	return nil
}
func (s *Service) proveInstanceInactive(ctx context.Context, v domain.AppInstance, env domain.Environment, ownPlan string) error {
	if v.ID == "" || env.ID != v.EnvironmentID || env.Name != "zah-"+v.ID || env.Ownership != "managed" || env.LaunchLease {
		return errors.New("cleanup requires exclusively managed identity and no launch lease")
	}
	switch v.State {
	case "unprepared", "archived", "forgotten", "data_removal_pending":
	default:
		return errors.New("instance has active or unresolved runtime state; reconcile it first")
	}
	plans, e := s.Store.List(ctx, "plan")
	if e != nil {
		return e
	}
	byID := map[string]domain.Plan{}
	for _, row := range plans {
		var p domain.Plan
		if e = json.Unmarshal(row.Data, &p); e != nil {
			return e
		}
		byID[p.ID] = p
	}
	jobs, e := s.Store.List(ctx, "job")
	if e != nil {
		return e
	}
	hadExecution := false
	for _, row := range jobs {
		var j domain.Job
		if e = json.Unmarshal(row.Data, &j); e != nil {
			return e
		}
		p, ok := byID[j.PlanID]
		if !ok {
			return errors.New("job has no plan evidence")
		}
		if p.InstanceID != v.ID || p.ID == ownPlan {
			continue
		}
		if !j.State.Terminal() {
			return errors.New("instance has a queued, running or unresolved job")
		}
		if p.Operation != "instance.forget" && p.Operation != "instance.data.remove" && (j.WorkerPID != 0 || j.StepIndex != 0 || j.State != domain.Cancelled) {
			hadExecution = true
		}
	}
	if env.Identity == "" && env.State == "unprepared" && len(env.CreationEvidence) == 0 && len(env.Registration) == 0 && len(env.PendingRestore) == 0 && len(v.Entry) == 0 && v.LastUsed == nil && !hadExecution {
		return nil // creation record and complete execution history prove ZaH never initialized this runtime
	}
	if (env.Backend != "distrobox-docker" && env.Backend != "bottles-flatpak") || env.State != "removed" || env.Identity == "" {
		return errors.New("no verified absence proof for this runtime; automatic cleanup refused")
	}
	var evidence []backend.Evidence
	if e = json.Unmarshal(env.CreationEvidence, &evidence); e != nil {
		var single backend.Evidence
		if e = json.Unmarshal(env.CreationEvidence, &single); e != nil {
			return errors.New("runtime removal evidence missing")
		}
		evidence = []backend.Evidence{single}
	}
	found := false
	for _, ev := range evidence {
		if ev.ResourceID == env.Identity && ev.State == "removed" && ev.Result.ExitCode == 0 && !ev.Result.DescendantsUnknown {
			found = true
		}
	}
	if !found {
		return errors.New("runtime state label lacks matching successful removal evidence")
	}
	if env.Backend == "bottles-flatpak" {
		r, _, e := s.request(ctx, v)
		if e != nil {
			return e
		}
		proof, e := s.Backend.VerifyBottleAbsent(ctx, r, env.Identity)
		if e != nil {
			return e
		}
		if proof.State != "absent" {
			return errors.New("Bottle absence not proved")
		}
		return nil
	}
	proof, e := s.Backend.VerifyAbsent(ctx, v.ID, "linux", env.Identity)
	if e != nil {
		return e
	}
	if proof.State != "absent" {
		return errors.New("runtime is not proved absent")
	}
	return nil
}
func (s *Service) pendingCleanupOriginalState(ctx context.Context, id string) (string, error) {
	rows, e := s.Store.List(ctx, "plan")
	if e != nil {
		return "", e
	}
	var latest domain.Plan
	var state string
	for _, row := range rows {
		var p domain.Plan
		if e = json.Unmarshal(row.Data, &p); e != nil {
			return "", e
		}
		if p.Operation != "instance.data.remove" || p.InstanceID != id {
			continue
		}
		var input instanceCleanupIntent
		if e = decode(p.Input, &input); e != nil {
			return "", e
		}
		if input.ID != id {
			return "", domain.ErrInvalid
		}
		if p.CreatedAt.After(latest.CreatedAt) {
			latest = p
			state = input.RestoreState
		}
	}
	if state != "unprepared" && state != "archived" && state != "forgotten" {
		return "", errors.New("pending data removal has no valid original-state evidence")
	}
	return state, nil
}
func (s *Service) executeInstanceCleanup(ctx context.Context, p domain.Plan, checks cleanupChecks) (CleanupResult, error) {
	result := CleanupResult{ID: p.InstanceID, AuditRetained: true, PrivateDataPreserved: true}
	var input instanceCleanupIntent
	if e := decode(p.Input, &input); e != nil {
		return result, e
	}
	if !safeInstanceID(input.ID) || p.InstanceID != input.ID || len(p.EnvironmentIDs) != 1 || p.EnvironmentIDs[0] != input.EnvironmentID {
		return result, domain.ErrInvalid
	}
	v, e := get[domain.AppInstance](ctx, s.Store, "instance", input.ID)
	if e != nil {
		return result, e
	}
	env, e := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	if e != nil {
		return result, e
	}
	if p.Operation == "instance.forget" && v.State == "forgotten" {
		result.State = "forgotten"
		return result, nil
	}
	if e = s.checkInstanceCleanupReferences(ctx, v, env, p.ID); e != nil {
		return result, e
	}
	if e = s.proveInstanceInactive(ctx, v, env, p.ID); e != nil {
		return result, e
	}
	fingerprint, e := cleanupMetadata(v, env)
	if e != nil {
		return result, e
	}
	if fingerprint != input.MetadataSHA256 {
		return result, fmt.Errorf("%w: instance cleanup preconditions changed", domain.ErrConflict)
	}
	if p.Operation == "instance.forget" {
		if e = s.Store.ForgetInstance(ctx, v.ID, v.State); e != nil {
			return result, e
		}
		result.State = "forgotten"
		return result, nil
	}
	if p.Operation != "instance.data.remove" || !input.ColdConfirmed || !input.ConfirmPrivateData {
		return result, domain.ErrInvalid
	}
	tree, e := s.inspectInstanceCleanupTree(ctx, v)
	if e != nil {
		return result, e
	}
	hash, e := cleanupTreeDigest(tree)
	if e != nil {
		return result, e
	}
	if len(tree) == 0 && input.Entries > 0 && v.State == input.RestoreState {
		result.State = "private_data_removed"
		result.PrivateDataPreserved = false
		return result, nil
	}
	if hash != input.TreeSHA256 {
		return result, fmt.Errorf("%w: private files changed after plan review", domain.ErrConflict)
	}
	if e = s.provePrivateDataRuntimeAbsent(ctx, v, env, tree); e != nil {
		return result, e
	}
	if e = ensureNoCleanupWriters(ctx, v.PrivateData, tree, checks.procRoot); e != nil {
		return result, e
	}
	if e = ctx.Err(); e != nil {
		return result, e
	}
	if e = s.Store.UpdateRecord(ctx, "instance", v.ID, func(r *domain.Record) error {
		var current domain.AppInstance
		if e := json.Unmarshal(r.Data, &current); e != nil {
			return e
		}
		if current.State != v.State || current.PrivateData != v.PrivateData {
			return domain.ErrConflict
		}
		current.State = "data_removal_pending"
		r.Data, _ = json.Marshal(current)
		return nil
	}); e != nil {
		return result, e
	}
	result.State = "data_removal_pending"
	result.PrivateDataPreserved = false
	// Each unlink is a safe cancellation boundary. Already removed data cannot
	// be rolled back; retain pending state and require a new plan for the remainder.
	bytesRemoved, e := removeCleanupTree(ctx, v.PrivateData, tree)
	result.BytesRemoved = bytesRemoved
	if e != nil {
		return result, e
	}
	if e = s.Store.UpdateRecord(context.WithoutCancel(ctx), "instance", v.ID, func(r *domain.Record) error {
		var current domain.AppInstance
		if e := json.Unmarshal(r.Data, &current); e != nil {
			return e
		}
		if current.State != "data_removal_pending" {
			return domain.ErrConflict
		}
		current.State = input.RestoreState
		r.Data, _ = json.Marshal(current)
		return nil
	}); e != nil {
		return result, e
	}
	result.State = "private_data_removed"
	return result, nil
}

// A root-owned Docker workload can be invisible to same-user /proc inspection.
// Therefore a nonempty Linux HOME also requires an actual backend absence check,
// even when the manager has never recorded an installation.
func (s *Service) provePrivateDataRuntimeAbsent(ctx context.Context, v domain.AppInstance, env domain.Environment, tree []cleanupTreeEntry) error {
	if len(tree) == 0 || env.Backend != "distrobox-docker" {
		return nil
	}
	proof, e := s.Backend.VerifyAbsent(ctx, v.ID, "linux", env.Identity)
	if e != nil {
		return e
	}
	if proof.State != "absent" {
		return errors.New("private-data cleanup requires verified runtime absence")
	}
	return nil
}
