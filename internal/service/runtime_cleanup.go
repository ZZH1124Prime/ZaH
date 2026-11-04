package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/jobs"
)

type runtimeCleanupInput struct {
	JobID                  string `json:"job_id"`
	InstanceID             string `json:"instance_id"`
	ExternalQuietConfirmed bool   `json:"external_quiet_confirmed"`
}
type runtimeCleanupIntent struct {
	Input   runtimeCleanupInput         `json:"input"`
	Backend backend.SnapshotRemovalPlan `json:"backend"`
}

func (s *Service) ownedSnapshot(ctx context.Context, in runtimeCleanupInput) (backend.SnapshotEvidence, error) {
	var proof backend.SnapshotEvidence
	if !in.ExternalQuietConfirmed {
		return proof, errors.New("confirm all external Docker modifications are stopped")
	}
	j, e := get[domain.Job](ctx, s.Store, "job", in.JobID)
	if e != nil {
		return proof, e
	}
	if !j.State.Terminal() {
		return proof, errors.New("snapshot source job is not terminal")
	}
	p, e := get[domain.Plan](ctx, s.Store, "plan", j.PlanID)
	if e != nil {
		return proof, e
	}
	var result EnvironmentBundleResult
	switch p.Operation {
	case "environment.export":
		if p.InstanceID != in.InstanceID {
			return proof, domain.ErrInvalid
		}
		if e = decode(j.Result, &result); e != nil {
			return proof, e
		}
	case "workbench.export":
		var batch WorkbenchBundleResult
		if e = decode(j.Result, &batch); e != nil {
			return proof, e
		}
		var ok bool
		result, ok = batch.Instances[in.InstanceID]
		if !ok {
			return proof, domain.ErrNotFound
		}
	default:
		return proof, errors.New("only locally executed export jobs confer snapshot ownership")
	}
	if result.Linux == nil || !result.Linux.ImageCreated || result.Linux.ImageLoaded || result.Linux.SourceInstanceID != in.InstanceID {
		return proof, errors.New("no locally-created snapshot evidence for this instance")
	}
	return *result.Linux, nil
}
func (s *Service) runtimeReferences(ctx context.Context, image, ownPlan, sourceJob string) error {
	rows, e := s.Store.List(ctx, "job")
	if e != nil {
		return e
	}
	hasJob, active := map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		var j domain.Job
		if e = json.Unmarshal(row.Data, &j); e != nil {
			return e
		}
		hasJob[j.PlanID] = true
		if !j.State.Terminal() {
			active[j.PlanID] = true
		}
		if j.ID != sourceJob && j.ID != jobs.ID(ctx) && !j.State.Terminal() && bytes.Contains(j.Result, []byte(image)) {
			return fmt.Errorf("%w: runtime used by active task", domain.ErrReferenced)
		}
	}
	for _, kind := range []string{"environment", "recipe", "plan"} {
		rows, e = s.Store.List(ctx, kind)
		if e != nil {
			return e
		}
		for _, row := range rows {
			if kind == "plan" && (row.ID == ownPlan || hasJob[row.ID] && !active[row.ID]) {
				continue
			}
			if bytes.Contains(row.Data, []byte(image)) {
				return fmt.Errorf("%w: image retained by %s %s", domain.ErrReferenced, kind, row.ID)
			}
		}
	}
	return nil
}
func (s *Service) planRuntimeCleanup(ctx context.Context, req planRequest) (domain.Plan, error) {
	p := domain.Plan{ID: domain.NewID(), Operation: req.Operation, CreatedAt: time.Now().UTC()}
	var in runtimeCleanupInput
	if e := decode(req.Input, &in); e != nil {
		return p, e
	}
	proof, e := s.ownedSnapshot(ctx, in)
	if e != nil {
		return p, e
	}
	v, e := get[domain.AppInstance](ctx, s.Store, "instance", in.InstanceID)
	if e != nil {
		return p, e
	}
	if e = s.runtimeReferences(ctx, proof.ImageID, p.ID, in.JobID); e != nil {
		return p, e
	}
	b, e := s.Backend.PlanSnapshotImageRemoval(ctx, proof)
	if e != nil {
		return p, e
	}
	b.ExternalQuietConfirmed = true
	p.InstanceID = v.ID
	p.EnvironmentIDs = []string{v.EnvironmentID}
	p.Input, e = json.Marshal(runtimeCleanupIntent{Input: in, Backend: b})
	if e != nil {
		return p, e
	}
	p.Preconditions = b.Requirements
	p.Permissions = []string{"external Docker clients must remain idle; cooperative ZaH locks do not exclude them"}
	p.DataImpact = []string{"removes only this locally-created untagged snapshot image by exact ownership label and ID", "no force, no parent-image prune; preserves bundle archive, private data and installation materials"}
	p.Steps = []domain.Step{{ID: domain.NewID(), Kind: p.Operation, Description: "Remove unreferenced owned snapshot image", Params: p.Input, SafeCancel: false}}
	return p, s.save(ctx, "plan", p.ID, p, ref("plan", p.ID, "job", in.JobID, "creation_evidence"), ref("plan", p.ID, "instance", v.ID, "target"))
}
func (s *Service) executeRuntimeCleanup(ctx context.Context, p domain.Plan) (any, error) {
	var in runtimeCleanupIntent
	if e := decode(p.Input, &in); e != nil {
		return nil, e
	}
	if in.Input.InstanceID != p.InstanceID {
		return nil, domain.ErrInvalid
	}
	proof, e := s.ownedSnapshot(ctx, in.Input)
	if e != nil {
		return nil, e
	}
	current, _ := json.Marshal(proof)
	frozen, _ := json.Marshal(in.Backend.Proof)
	if !bytes.Equal(current, frozen) {
		return nil, errors.New("snapshot evidence changed after review")
	}
	if e = s.runtimeReferences(ctx, proof.ImageID, p.ID, in.Input.JobID); e != nil {
		return nil, e
	}
	return s.Backend.RemoveSnapshotImage(ctx, in.Backend)
}
