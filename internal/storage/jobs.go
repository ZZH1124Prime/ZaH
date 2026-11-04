package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

// PutWithReferences atomically registers a record and its dependencies.
func (s *Store) PutWithReferences(ctx context.Context, r domain.Record, refs []domain.ResourceRef) error {
	return s.putReferences(ctx, r, refs, "")
}

// PutReplacingReferenceRole replaces only the selected dependency role in the
// same transaction as the record change. Other dependency roles are retained.
func (s *Store) PutReplacingReferenceRole(ctx context.Context, r domain.Record, role string, refs []domain.ResourceRef) error {
	if role == "" || len(role) > 128 || strings.ContainsAny(role, "\x00\r\n") {
		return domain.ErrInvalid
	}
	return s.putReferences(ctx, r, refs, role)
}
func (s *Store) putReferences(ctx context.Context, r domain.Record, refs []domain.ResourceRef, replaceRole string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = put(ctx, tx, r); err != nil {
		return err
	}
	if replaceRole != "" {
		if _, err = tx.ExecContext(ctx, `DELETE FROM resource_refs WHERE from_kind=? AND from_id=? AND role=?`, r.Kind, r.ID, replaceRole); err != nil {
			return err
		}
	}
	for _, ref := range refs {
		if ref.FromKind != r.Kind || ref.FromID != r.ID {
			return fmt.Errorf("%w: reference owner differs", domain.ErrInvalid)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO resource_refs(from_kind,from_id,to_kind,to_id,role) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, ref.FromKind, ref.FromID, ref.ToKind, ref.ToID, ref.Role); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SubmitJob atomically deduplicates submissions. The boolean is true for a newly created job.
func (s *Store) SubmitJob(ctx context.Context, planID, key string, confirmed ...bool) (domain.Job, bool, error) {
	var job domain.Job
	if key == "" || len(key) > 256 || strings.HasPrefix(key, internalIdempotencyPrefix) {
		return job, false, domain.ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return job, false, err
	}
	defer tx.Rollback()
	var discarded int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM idempotency WHERE key=?`, discardKey(planID)).Scan(&discarded); err != nil {
		return job, false, err
	}
	if discarded != 0 {
		return job, false, fmt.Errorf("%w: plan review was discarded; create a new plan", domain.ErrConflict)
	}
	var oldPlan, jobID string
	err = tx.QueryRowContext(ctx, `SELECT plan_id,job_id FROM idempotency WHERE key=?`, key).Scan(&oldPlan, &jobID)
	if err == nil {
		if oldPlan != planID {
			return job, false, fmt.Errorf("%w: idempotency key belongs to another plan", domain.ErrConflict)
		}
		r, e := get(ctx, tx, "job", jobID)
		if e != nil {
			return job, false, e
		}
		e = json.Unmarshal(r.Data, &job)
		return job, false, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return job, false, err
	}
	// A plan describes one immutable side effect, not a reusable command. A
	// renderer restart may lose its request key; never execute the same plan twice.
	var priorID string
	err = tx.QueryRowContext(ctx, `SELECT job_id FROM idempotency WHERE plan_id=? ORDER BY key LIMIT 1`, planID).Scan(&priorID)
	if err == nil {
		r, e := get(ctx, tx, "job", priorID)
		if e != nil {
			return job, false, e
		}
		if e = json.Unmarshal(r.Data, &job); e != nil {
			return job, false, e
		}
		// Return the persisted original key too; no schema change or alias write.
		return job, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return job, false, err
	}
	if _, err = get(ctx, tx, "plan", planID); err != nil {
		return job, false, err
	}
	job = domain.Job{ID: domain.NewID(), PlanID: planID, IdempotencyKey: key, State: domain.Queued, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if len(confirmed) > 1 {
		return job, false, domain.ErrInvalid
	}
	if len(confirmed) == 1 {
		job.HostChangesConfirmed = confirmed[0]
	}
	data, _ := json.Marshal(job)
	if err = put(ctx, tx, domain.Record{ID: job.ID, Kind: "job", Data: data}); err != nil {
		return job, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO idempotency(key,plan_id,job_id) VALUES(?,?,?)`, key, planID, job.ID); err != nil {
		return job, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO resource_refs(from_kind,from_id,to_kind,to_id,role) VALUES('job',?,'plan',?,'plan')`, job.ID, planID); err != nil {
		return job, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO events(type,data,created_at) VALUES('job.queued',?,?)`, string(data), now()); err != nil {
		return job, false, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Job{}, false, err
	}
	return job, true, nil
}

func (s *Store) mutateJob(ctx context.Context, id string, update func(*domain.Job) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := get(ctx, tx, "job", id)
	if err != nil {
		return err
	}
	var j domain.Job
	if err = json.Unmarshal(r.Data, &j); err != nil {
		return err
	}
	if err = update(&j); err != nil {
		return err
	}
	j.UpdatedAt = time.Now().UTC()
	r.Data, err = json.Marshal(j)
	if err != nil {
		return err
	}
	if err = put(ctx, tx, r); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO events(type,data,created_at) VALUES('job.changed',?,?)`, string(r.Data), now()); err != nil {
		return err
	}
	return tx.Commit()
}

// TransitionJob performs a compare-and-swap state transition and records an event.
func (s *Store) TransitionJob(ctx context.Context, id string, from, to domain.JobState, detail string) error {
	if err := domain.ValidateTransition(from, to); err != nil {
		return err
	}
	return s.mutateJob(ctx, id, func(j *domain.Job) error {
		if j.State != from {
			return fmt.Errorf("%w: job state changed", domain.ErrConflict)
		}
		j.State = to
		j.Error = detail
		return nil
	})
}

// SetWorker registers process identity without overwriting a concurrent cancellation.
func (s *Store) SetWorker(ctx context.Context, id string, pid int, start string) error {
	if pid < 1 || start == "" {
		return domain.ErrInvalid
	}
	return s.mutateJob(ctx, id, func(j *domain.Job) error {
		if j.State.Terminal() {
			return domain.ErrConflict
		}
		if j.WorkerPID != 0 && (j.WorkerPID != pid || j.WorkerStart != start) {
			return domain.ErrConflict
		}
		j.WorkerPID = pid
		j.WorkerStart = start
		return nil
	})
}

// SetJobResult persists completed-step evidence separately from state transitions.
func (s *Store) SetJobResult(ctx context.Context, id string, result json.RawMessage) error {
	if !json.Valid(result) || len(result) > 4<<20 {
		return domain.ErrInvalid
	}
	return s.mutateJob(ctx, id, func(j *domain.Job) error { j.Result = append(json.RawMessage(nil), result...); return nil })
}

// SetJobStep records the next step only while a job owns execution.
func (s *Store) SetJobStep(ctx context.Context, id string, index int) error {
	if index < 0 {
		return domain.ErrInvalid
	}
	return s.mutateJob(ctx, id, func(j *domain.Job) error {
		if j.State != domain.Running && j.State != domain.CancelRequested {
			return domain.ErrConflict
		}
		if index < j.StepIndex {
			return domain.ErrConflict
		}
		j.StepIndex = index
		return nil
	})
}

// ImportRecords commits new semantic records and references as one transaction.
// Existing IDs are rejected; callers must remap identities before importing.
func (s *Store) ImportRecords(ctx context.Context, records []domain.Record, refs []domain.ResourceRef) error {
	return s.ImportRecordsAndSources(ctx, records, refs, nil)
}

// ImportRecordsAndSources also retains provenance for already-present materials
// in the same transaction as new catalog records. Existing material facts and
// source records are never replaced.
func (s *Store) ImportRecordsAndSources(ctx context.Context, records []domain.Record, refs []domain.ResourceRef, sources map[string][]json.RawMessage) error {
	return s.ImportRecordsAndRecoveredMaterials(ctx, records, refs, sources, nil)
}

// ImportRecordsAndRecoveredMaterials atomically revives explicitly verified
// archived bytes together with new records and provenance. The expected original
// JSON prevents recovery from overwriting a changed tombstone or existing facts.
func (s *Store) ImportRecordsAndRecoveredMaterials(ctx context.Context, records []domain.Record, refs []domain.ResourceRef, sources map[string][]json.RawMessage, recovered map[string]json.RawMessage) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range records {
		if err = validate(r); err != nil {
			return err
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM records WHERE kind=? AND id=?`, r.Kind, r.ID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("%w: imported record already exists", domain.ErrConflict)
		}
		if r.Kind == "job" || r.Kind == "plan" {
			return fmt.Errorf("%w: execution records cannot be replayed", domain.ErrInvalid)
		}
		if err = put(ctx, tx, r); err != nil {
			return err
		}
	}
	for id, before := range recovered {
		existing, e := get(ctx, tx, "artifact", id)
		if e != nil {
			return e
		}
		if string(existing.Data) != string(before) {
			return fmt.Errorf("%w: material changed during recovery", domain.ErrConflict)
		}
		var fields map[string]json.RawMessage
		if e = json.Unmarshal(existing.Data, &fields); e != nil {
			return e
		}
		var availability string
		if e = json.Unmarshal(fields["availability"], &availability); e != nil || (availability != "removed" && availability != "removal_pending") {
			return domain.ErrConflict
		}
		fields["availability"] = json.RawMessage(`"available"`)
		existing.Data, e = json.Marshal(fields)
		if e != nil {
			return e
		}
		if e = put(ctx, tx, existing); e != nil {
			return e
		}
	}
	for id, incoming := range sources {
		existing, e := get(ctx, tx, "artifact", id)
		if e != nil {
			return e
		}
		var fields map[string]json.RawMessage
		if e = json.Unmarshal(existing.Data, &fields); e != nil {
			return e
		}
		merged := []json.RawMessage{}
		seen := map[string]bool{}
		add := func(value json.RawMessage) error {
			var object map[string]any
			if e := json.Unmarshal(value, &object); e != nil || object == nil {
				return domain.ErrInvalid
			}
			canonical, e := json.Marshal(object)
			if e != nil {
				return e
			}
			key := string(canonical)
			if !seen[key] {
				seen[key] = true
				merged = append(merged, canonical)
			}
			return nil
		}
		if original := fields["source"]; len(original) > 0 {
			if e = add(original); e != nil {
				return e
			}
		}
		var previous []json.RawMessage
		if value := fields["sources"]; len(value) > 0 {
			if e = json.Unmarshal(value, &previous); e != nil {
				return e
			}
		}
		for _, value := range append(previous, incoming...) {
			if e = add(value); e != nil {
				return e
			}
		}
		fields["sources"], e = json.Marshal(merged)
		if e != nil {
			return e
		}
		existing.Data, e = json.Marshal(fields)
		if e != nil {
			return e
		}
		if e = put(ctx, tx, existing); e != nil {
			return e
		}
	}
	for _, r := range refs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO resource_refs(from_kind,from_id,to_kind,to_id,role) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, r.FromKind, r.FromID, r.ToKind, r.ToID, r.Role); err != nil {
			return err
		}
	}
	return tx.Commit()
}
