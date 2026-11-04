package service

import (
	"context"
	"encoding/json"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"strings"
	"testing"
)

func TestDependencyJobsRequireExplicitApproval(t *testing.T) {
	s := catalogService(t)
	ctx := context.Background()
	p := domain.Plan{ID: domain.NewID(), Operation: "host.dependencies.install"}
	if e := s.save(ctx, "plan", p.ID, p); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(map[string]any{"plan_id": p.ID, "idempotency_key": "no-approval"})
	if _, e := s.Handle(ctx, "job.submit", raw); e == nil {
		t.Fatal("unconfirmed job submitted")
	}
	raw, _ = json.Marshal(map[string]any{"plan_id": p.ID, "idempotency_key": "approved", "host_changes_confirmed": true})
	value, e := s.Handle(ctx, "job.submit", raw)
	if e != nil {
		t.Fatal(e)
	}
	j := value.(domain.Job)
	if !j.HostChangesConfirmed {
		t.Fatal("approval not persisted")
	}
	raw, _ = json.Marshal(map[string]any{"plan_id": p.ID, "idempotency_key": "another", "host_changes_confirmed": true})
	again, e := s.Handle(ctx, "job.submit", raw)
	if e != nil || again.(domain.Job).ID != j.ID {
		t.Fatal("duplicate installation job", e)
	}
	if _, e = s.executeDependencies(ctx, p); e == nil {
		t.Fatal("non-worker invocation accepted")
	}
}
func TestDependencyLogRedactsSensitiveContent(t *testing.T) {
	for _, line := range []string{"Download https://user:password@host/file?token=secret", "TOKEN=secret", "Authorization: bearer secret"} {
		if got := cleanDependencyLine(line); strings.Contains(got, "secret") || strings.Contains(got, "user:") {
			t.Fatal("secret retained", got)
		}
	}
	if got := cleanDependencyLine("Get https://example.test/pkg.deb"); strings.Contains(got, "example") {
		t.Fatal("URL retained")
	}
	if len(cleanDependencyLine(strings.Repeat("x", 10000))) > 2100 {
		t.Fatal("unbounded line")
	}
}
