package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/infra"
)

type stopTarget struct {
	InstanceID    string `json:"instance_id"`
	EnvironmentID string `json:"environment_id"`
	Identity      string `json:"identity"`
}
type stopAllIntent struct {
	Confirmed       bool         `json:"confirmed"`
	Targets         []stopTarget `json:"targets"`
	QueuedLaunchIDs []string     `json:"queued_launch_ids,omitempty"`
}
type StopResult struct {
	InstanceID string `json:"instance_id"`
	Name       string `json:"name"`
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
}
type StopAllResult struct {
	Results []StopResult `json:"results"`
	Stopped int          `json:"stopped"`
	Failed  int          `json:"failed"`
}

func requiresStopReview(v domain.AppInstance, env domain.Environment) bool {
	if env.Identity != "" || incompleteRestore(env) {
		return true
	}
	// A creation/launch may have taken effect before identity persistence failed.
	// Include the uncertainty in the result; never guess a resource ID to stop it.
	switch v.State {
	case "installing", "launching", "running", "ready", "needs_inspection":
		return true
	}
	return false
}

// planStopAll freezes only registered managed identities. It never enumerates
// arbitrary Docker resources or accepts an identity supplied by the renderer.
func (s *Service) planStopAll(ctx context.Context, req planRequest) (domain.Plan, error) {
	p := domain.Plan{ID: domain.NewID(), Operation: req.Operation, CreatedAt: time.Now().UTC()}
	var in struct {
		Confirmed bool `json:"confirmed"`
	}
	if err := decode(req.Input, &in); err != nil {
		return p, err
	}
	if !in.Confirmed {
		return p, errors.New("confirm stopping all managed applications and possible loss of unsaved work")
	}
	rows, err := s.Store.List(ctx, "instance")
	if err != nil {
		return p, err
	}
	intent := stopAllIntent{Confirmed: true, Targets: []stopTarget{}}
	refs := []domain.ResourceRef{}
	for _, row := range rows {
		var v domain.AppInstance
		if err = json.Unmarshal(row.Data, &v); err != nil {
			return p, err
		}
		if v.State == "forgotten" || v.State == "unprepared" || v.State == "archived" {
			continue
		}
		env, e := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
		if e != nil {
			return p, e
		}
		if env.Ownership != "managed" || env.Name != "zah-"+v.ID || !requiresStopReview(v, env) {
			continue
		}
		intent.Targets = append(intent.Targets, stopTarget{v.ID, env.ID, env.Identity})
		refs = append(refs, ref("plan", p.ID, "instance", v.ID, "stop_target"), ref("plan", p.ID, "environment", env.ID, "stop_target"))
	}
	if len(intent.Targets) > 256 {
		return p, errors.New("stop batch exceeds 256 registered environments")
	}
	intent.QueuedLaunchIDs, err = s.queuedLaunches(ctx)
	if err != nil {
		return p, err
	}
	for _, id := range intent.QueuedLaunchIDs {
		refs = append(refs, ref("plan", p.ID, "job", id, "queued_launch"))
	}
	p.Input, err = json.Marshal(intent)
	if err != nil {
		return p, err
	}
	// Deliberately no EnvironmentIDs: live launch workers own those locks. The
	// execution uses a stop barrier and per-environment control gates instead.
	p.DataImpact = []string{"stops only frozen ZaH managed environments after identity revalidation; unsaved application data may be lost", "preserves installations and private data; never global Docker/Wine process shutdown"}
	p.Steps = []domain.Step{{ID: domain.NewID(), Kind: p.Operation, Description: "Stop registered applications with per-target evidence", Params: p.Input, SafeCancel: false}}
	return p, s.save(ctx, "plan", p.ID, p, refs...)
}
func (s *Service) lifecycleGate(ctx context.Context, id string) (*infra.LockSet, error) {
	return infra.AcquireLocks(ctx, s.Paths.Runtime, "lifecycle-"+id)
}
func (s *Service) executeStopAll(ctx context.Context, p domain.Plan) (StopAllResult, error) {
	result := StopAllResult{Results: []StopResult{}}
	var in stopAllIntent
	if err := decode(p.Input, &in); err != nil {
		return result, err
	}
	if !in.Confirmed || len(in.Targets) > 256 {
		return result, domain.ErrInvalid
	}
	barrier, err := infra.AcquireLock(filepath.Join(s.Paths.Runtime, "stop-all.lock"))
	if err != nil {
		return result, err
	}
	defer barrier.Close()
	if err = s.validateStopSnapshot(ctx, in); err != nil {
		return result, err
	}
	for _, id := range in.QueuedLaunchIDs {
		j, e := get[domain.Job](ctx, s.Store, "job", id)
		if e != nil {
			return result, e
		}
		if j.State == domain.Queued {
			if e = s.Store.TransitionJob(ctx, id, domain.Queued, domain.Cancelled, "cancelled before launch by confirmed stop-all"); e != nil {
				return result, e
			}
		}
	}
	for _, target := range in.Targets {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		item := s.stopTarget(ctx, target)
		result.Results = append(result.Results, item)
		if item.State == "stopped" || item.State == "skipped" {
			result.Stopped++
		} else {
			result.Failed++
		}
	}
	if result.Failed > 0 {
		return result, fmt.Errorf("%d managed environments could not be confirmed stopped; inspect per-target results", result.Failed)
	}
	return result, nil
}
func (s *Service) stopTarget(ctx context.Context, t stopTarget) StopResult {
	out := StopResult{InstanceID: t.InstanceID, State: "failed"}
	fail := func(err error) StopResult { out.Error = err.Error(); return out }
	gate, err := s.lifecycleGate(ctx, t.EnvironmentID)
	if err != nil {
		return fail(err)
	}
	v, err := get[domain.AppInstance](ctx, s.Store, "instance", t.InstanceID)
	if err != nil {
		gate.Close()
		return fail(err)
	}
	out.Name = v.Name
	env, err := get[domain.Environment](ctx, s.Store, "environment", t.EnvironmentID)
	if err != nil {
		gate.Close()
		return fail(err)
	}
	if v.EnvironmentID != t.EnvironmentID || env.Identity != t.Identity || env.Name != "zah-"+v.ID || env.Ownership != "managed" {
		gate.Close()
		return fail(errors.New("registered environment identity changed"))
	}
	if env.Identity == "" {
		gate.Close()
		return fail(errors.New("environment creation or restoration has no verified identity; inspect the incomplete operation before confirming all environments stopped"))
	}
	// An active launch is stopped by its supervising worker only after the helper
	// has actually entered the environment. Stopping an earlier phase could race
	// distrobox initialization and let it start again after this operation returns.
	if v.State == "launching" {
		if env.LaunchToken == "" {
			gate.Close()
			return fail(errors.New("legacy or interrupted launcher has no supervised handshake; cannot safely race its pending start"))
		}
		env.StopRequested = true
		err = s.Store.Put(ctx, record("environment", env.ID, env))
		gate.Close()
		if err != nil {
			return fail(err)
		}
		waitCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			current, e := get[domain.Environment](waitCtx, s.Store, "environment", env.ID)
			if e != nil {
				return fail(e)
			}
			if current.Identity != t.Identity {
				return fail(errors.New("environment identity changed while stopping"))
			}
			if current.State == "stopped" && !current.LaunchLease {
				out.State = "stopped"
				return out
			}
			instance, e := get[domain.AppInstance](waitCtx, s.Store, "instance", v.ID)
			if e != nil {
				return fail(e)
			}
			if instance.State != "launching" {
				return fail(errors.New("launch ended without confirmed stopped state; inspect task evidence"))
			}
			select {
			case <-waitCtx.Done():
				return fail(errors.New("stop requested; launcher has not acknowledged a safely stopped environment"))
			case <-ticker.C:
			}
		}
	}
	defer gate.Close()
	// Do not interrupt installation/restoration or any other worker holding the
	// environment lock. Batch stop is not a way to bypass their transaction.
	idle, err := infra.AcquireLock(filepath.Join(s.Paths.Runtime, "environment-"+env.ID+".lock"))
	if err != nil {
		return fail(errors.New("environment has an active non-launch operation"))
	}
	defer idle.Close()
	r, _, err := s.request(ctx, v)
	if err != nil {
		return fail(err)
	}
	proof, err := s.Backend.Stop(ctx, r, env.Identity)
	if err != nil {
		return fail(err)
	}
	env.State = proof.State
	if v.State == "needs_inspection" && s.rejectedLegacyLaunch(ctx, v, env) {
		v.State = "ready"
		if err := s.persistInstanceState(context.WithoutCancel(ctx), v); err != nil {
			return fail(err)
		}
	}
	if v.State == "needs_inspection" { // a vanished worker may still have an orphan launcher
		env.StopRequested = true
		_ = s.Store.Put(context.WithoutCancel(ctx), record("environment", env.ID, env))
		return fail(errors.New("environment stopped; interrupted launcher still requires reconciliation before another version can run"))
	}
	env.LaunchLease = false
	env.StopRequested = false
	env.LaunchToken = ""
	if err = s.Store.Put(context.WithoutCancel(ctx), record("environment", env.ID, env)); err != nil {
		return fail(err)
	}
	out.State = "stopped"
	return out
}

func (s *Service) queuedLaunches(ctx context.Context) ([]string, error) {
	rows, err := s.Store.List(ctx, "job")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, row := range rows {
		var j domain.Job
		if err = json.Unmarshal(row.Data, &j); err != nil {
			return nil, err
		}
		if j.State != domain.Queued {
			continue
		}
		p, e := get[domain.Plan](ctx, s.Store, "plan", j.PlanID)
		if e != nil {
			return nil, e
		}
		if p.Operation == "instance.launch" {
			ids = append(ids, j.ID)
		}
	}
	return ids, nil
}
func (s *Service) validateStopSnapshot(ctx context.Context, in stopAllIntent) error {
	frozen := map[string]stopTarget{}
	for _, t := range in.Targets {
		if _, ok := frozen[t.InstanceID]; ok {
			return domain.ErrInvalid
		}
		frozen[t.InstanceID] = t
	}
	rows, err := s.Store.List(ctx, "instance")
	if err != nil {
		return err
	}
	count := 0
	for _, row := range rows {
		var v domain.AppInstance
		if err = json.Unmarshal(row.Data, &v); err != nil {
			return err
		}
		if v.State == "forgotten" || v.State == "unprepared" || v.State == "archived" {
			continue
		}
		env, e := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
		if e != nil {
			return e
		}
		if env.Ownership != "managed" || env.Name != "zah-"+v.ID || !requiresStopReview(v, env) {
			continue
		}
		if frozen[v.ID] != (stopTarget{v.ID, env.ID, env.Identity}) {
			return errors.New("managed applications changed after confirmation; review a new stop-all request")
		}
		count++
	}
	if count != len(frozen) {
		return errors.New("managed applications changed after confirmation; review a new stop-all request")
	}
	queued, err := s.queuedLaunches(ctx)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, id := range in.QueuedLaunchIDs {
		allowed[id] = true
	}
	for _, id := range queued {
		if !allowed[id] {
			return errors.New("new launch queued after confirmation; review a new stop-all request")
		}
	}
	return nil
}

// A single stop freezes exactly one registered identity. Other applications and
// their queued launches are deliberately outside its scope.
func (s *Service) planStopOne(ctx context.Context, req planRequest) (domain.Plan, error) {
	p := domain.Plan{ID: domain.NewID(), Operation: req.Operation, CreatedAt: time.Now().UTC()}
	var in instanceInput
	if err := decode(req.Input, &in); err != nil {
		return p, err
	}
	v, err := get[domain.AppInstance](ctx, s.Store, "instance", in.ID)
	if err != nil {
		return p, err
	}
	env, err := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	if err != nil {
		return p, err
	}
	if env.Ownership != "managed" || env.Name != "zah-"+v.ID || env.Identity == "" {
		return p, errors.New("verified managed environment required")
	}
	p.InstanceID = v.ID
	p.Input, _ = json.Marshal(stopTarget{v.ID, env.ID, env.Identity})
	p.DataImpact = []string{"stops this application's managed environment; unsaved data may be lost; installed files are retained"}
	p.Steps = []domain.Step{{ID: domain.NewID(), Kind: p.Operation, Description: "Stop this registered environment", Params: p.Input, SafeCancel: false}}
	return p, s.save(ctx, "plan", p.ID, p, ref("plan", p.ID, "instance", v.ID, "stop_target"), ref("plan", p.ID, "environment", env.ID, "stop_target"))
}
func (s *Service) executeStopOne(ctx context.Context, p domain.Plan) (StopAllResult, error) {
	result := StopAllResult{Results: []StopResult{}}
	var target stopTarget
	if err := decode(p.Input, &target); err != nil {
		return result, err
	}
	if target.InstanceID != p.InstanceID {
		return result, domain.ErrInvalid
	}
	barrier, err := infra.AcquireLock(filepath.Join(s.Paths.Runtime, "stop-all.lock"))
	if err != nil {
		return result, err
	}
	defer barrier.Close()
	// Cancel only this instance's queued launch, under the worker modification
	// lock and the same barrier checked before any launch claims its lease.
	ids, err := s.queuedLaunches(ctx)
	if err != nil {
		return result, err
	}
	for _, id := range ids {
		j, e := get[domain.Job](ctx, s.Store, "job", id)
		if e != nil {
			return result, e
		}
		plan, e := get[domain.Plan](ctx, s.Store, "plan", j.PlanID)
		if e != nil {
			return result, e
		}
		if plan.InstanceID == target.InstanceID {
			if e = s.Store.TransitionJob(ctx, id, domain.Queued, domain.Cancelled, "cancelled before single-application stop"); e != nil {
				return result, e
			}
		}
	}
	item := s.stopTarget(ctx, target)
	result.Results = append(result.Results, item)
	if item.State != "stopped" {
		result.Failed = 1
		return result, errors.New(item.Error)
	}
	result.Stopped = 1
	return result, nil
}

// A failed create/restore can leave a backend resource without final registration.
// Include it in stop results so close cannot silently report all environments stopped.
func incompleteRestore(env domain.Environment) bool {
	if env.State == "restore_creating" {
		return true
	}
	if len(env.PendingRestore) == 0 {
		return false
	}
	var pending PendingEnvironmentRestore
	if json.Unmarshal(env.PendingRestore, &pending) != nil {
		return true
	}
	return pending.BottleState != nil && (pending.BottleState.Phase == "creating" || pending.BottleState.Fresh != nil)
}
