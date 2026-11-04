package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/process"
)

type runtimeRemovalRunner struct {
	proof   backend.SnapshotEvidence
	removed bool
}

func (r *runtimeRemovalRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	args := strings.Join(c.Args, " ")
	if strings.HasPrefix(args, "image inspect ") {
		b, _ := json.Marshal([]any{map[string]any{"Id": r.proof.ImageID, "RepoTags": []string{}, "RepoDigests": []string{}, "Config": map[string]any{"Labels": map[string]string{"org.zah.snapshot": r.proof.OwnershipToken, "org.zah.snapshot.instance": r.proof.SourceInstanceID}}}})
		return process.Result{Stdout: string(b)}, nil
	}
	if strings.HasPrefix(args, "image rm --no-prune ") {
		r.removed = true
	}
	return process.Result{}, nil
}
func runtimeRemovalSource(t *testing.T) (*Service, domain.AppInstance, runtimeCleanupInput, *runtimeRemovalRunner) {
	t.Helper()
	s, v := fullSource(t)
	proof := backend.SnapshotEvidence{ImageCreated: true, ImageID: "sha256:" + strings.Repeat("d", 64), SourceInstanceID: v.ID, OwnershipToken: strings.Repeat("e", 64)}
	result, _ := json.Marshal(EnvironmentBundleResult{Linux: &proof, State: "incomplete"})
	source := domain.Plan{ID: domain.NewID(), Operation: "environment.export", InstanceID: v.ID}
	j := domain.Job{ID: domain.NewID(), PlanID: source.ID, State: domain.Failed, Result: result}
	for _, r := range []domain.Record{record("plan", source.ID, source), record("job", j.ID, j)} {
		if e := s.Store.Put(context.Background(), r); e != nil {
			t.Fatal(e)
		}
	}
	fake := &runtimeRemovalRunner{proof: proof}
	s.Backend = backend.Adapter{Runner: fake}
	return s, v, runtimeCleanupInput{JobID: j.ID, InstanceID: v.ID, ExternalQuietConfirmed: true}, fake
}
func TestRuntimeRemovalUsesPersistedEvidenceAndRechecksReferences(t *testing.T) {
	s, v, in, fake := runtimeRemovalSource(t)
	ctx := context.Background()
	p := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "runtime.remove", "input": in})
	env, _ := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	env.PendingRestore, _ = json.Marshal(PendingEnvironmentRestore{ImageID: fake.proof.ImageID})
	if e := s.Store.Put(ctx, record("environment", env.ID, env)); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Execute(ctx, p); e == nil || fake.removed {
		t.Fatal("removed a newly referenced runtime")
	}
	env.PendingRestore = nil
	if e := s.Store.Put(ctx, record("environment", env.ID, env)); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Execute(ctx, p); e != nil {
		t.Fatal(e)
	}
	if !fake.removed {
		t.Fatal("did not execute scoped removal")
	}
}
func TestRuntimeRemovalRejectsUnconfirmedOrCallerSuppliedOwnership(t *testing.T) {
	s, _, in, fake := runtimeRemovalSource(t)
	ctx := context.Background()
	for _, input := range []any{
		runtimeCleanupInput{JobID: in.JobID, InstanceID: in.InstanceID},
		map[string]any{"image_id": fake.proof.ImageID, "ownership_token": fake.proof.OwnershipToken, "external_quiet_confirmed": true},
	} {
		raw, _ := json.Marshal(map[string]any{"operation": "runtime.remove", "input": input})
		if _, e := s.Handle(ctx, "plan.create", raw); e == nil {
			t.Fatal("accepted unaudited ownership or missing confirmation")
		}
	}
	if fake.removed {
		t.Fatal("planning deleted runtime")
	}
}
