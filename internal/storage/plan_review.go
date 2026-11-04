package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

const internalIdempotencyPrefix = "zah-internal:"

func discardKey(planID string) string { return internalIdempotencyPrefix + "discard:" + planID }

// DiscardPlan retires an unsubmitted review without mutating its immutable plan
// or deleting audit references. The cancelled job is evidence that it never ran.
func (s *Store) DiscardPlan(ctx context.Context, planID string) (domain.Job, error) {
	var job domain.Job
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return job, e
	}
	defer tx.Rollback()
	if _, e = get(ctx, tx, "plan", planID); e != nil {
		return job, e
	}
	var existing string
	e = tx.QueryRowContext(ctx, `SELECT job_id FROM idempotency WHERE key=?`, discardKey(planID)).Scan(&existing)
	if e == nil {
		r, e := get(ctx, tx, "job", existing)
		if e != nil {
			return job, e
		}
		if e = json.Unmarshal(r.Data, &job); e != nil {
			return job, e
		}
		if job.PlanID != planID || job.IdempotencyKey != discardKey(planID) || job.State != domain.Cancelled || job.WorkerPID != 0 || job.StepIndex != 0 {
			return domain.Job{}, errors.New("discard audit is inconsistent")
		}
		return job, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return job, e
	}
	var submitted int
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM idempotency WHERE plan_id=?`, planID).Scan(&submitted); e != nil {
		return job, e
	}
	if submitted != 0 {
		return job, fmt.Errorf("%w: submitted plans cannot be discarded; cancel their job instead", domain.ErrConflict)
	}
	nowTime := time.Now().UTC()
	job = domain.Job{ID: domain.NewID(), PlanID: planID, IdempotencyKey: discardKey(planID), State: domain.Cancelled, Error: "review discarded before execution", CreatedAt: nowTime, UpdatedAt: nowTime}
	encoded, e := json.Marshal(job)
	if e != nil {
		return job, e
	}
	if e = put(ctx, tx, domain.Record{Kind: "job", ID: job.ID, Data: encoded}); e != nil {
		return job, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO idempotency(key,plan_id,job_id) VALUES(?,?,?)`, job.IdempotencyKey, planID, job.ID); e != nil {
		return job, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO resource_refs(from_kind,from_id,to_kind,to_id,role) VALUES('job',?,'plan',?,'plan')`, job.ID, planID); e != nil {
		return job, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO events(type,data,created_at) VALUES('plan.discarded',?,?)`, string(encoded), now()); e != nil {
		return job, e
	}
	if e = tx.Commit(); e != nil {
		return domain.Job{}, e
	}
	return job, nil
}
