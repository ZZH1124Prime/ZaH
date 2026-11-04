package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

func TestDiscardPlanRetainsImmutableAuditAndPreventsExecution(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := rec("plan", "p", `{"id":"p","operation":"install","steps":[]}`)
	if e := s.Put(ctx, p); e != nil {
		t.Fatal(e)
	}
	job, e := s.DiscardPlan(ctx, "p")
	if e != nil || job.State != domain.Cancelled || job.WorkerPID != 0 {
		t.Fatalf("discard %+v %v", job, e)
	}
	again, e := s.DiscardPlan(ctx, "p")
	if e != nil || again.ID != job.ID {
		t.Fatalf("duplicate discard %+v %v", again, e)
	}
	after, e := s.Get(ctx, "plan", "p")
	if e != nil || string(after.Data) != string(p.Data) {
		t.Fatal("immutable plan changed", e)
	}
	if _, _, e = s.SubmitJob(ctx, "p", "user-retry"); !errors.Is(e, domain.ErrConflict) {
		t.Fatalf("discarded plan executed: %v", e)
	}
	if _, _, e = s.SubmitJob(ctx, "p", discardKey("p")); !errors.Is(e, domain.ErrInvalid) {
		t.Fatalf("client used internal key: %v", e)
	}
	if e = s.Put(ctx, rec("plan", "submitted", `{}`)); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.SubmitJob(ctx, "submitted", "normal"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DiscardPlan(ctx, "submitted"); !errors.Is(e, domain.ErrConflict) {
		t.Fatalf("submitted plan discarded: %v", e)
	}
}
func TestDiscardAndSubmitAreMutuallyExclusive(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for attempt := 0; attempt < 20; attempt++ {
		id := domain.NewID()
		if e := s.Put(ctx, rec("plan", id, `{}`)); e != nil {
			t.Fatal(e)
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() { <-start; _, e := s.DiscardPlan(ctx, id); results <- e }()
		go func() { <-start; _, _, e := s.SubmitJob(ctx, id, "submit-"+id); results <- e }()
		close(start)
		a, b := <-results, <-results
		if (a == nil) == (b == nil) {
			t.Fatalf("expected exactly one winner: %v %v", a, b)
		}
	}
}
