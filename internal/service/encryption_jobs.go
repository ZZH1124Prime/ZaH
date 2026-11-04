package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

func (s *Service) planEncryption(ctx context.Context, req planRequest) (domain.Plan, error) {
	p := domain.Plan{ID: domain.NewID(), Operation: req.Operation, CreatedAt: time.Now().UTC()}
	var b BundleEncryptionPlan
	var e error
	if req.Operation == "bundle.encrypt" {
		var in EncryptBundleInput
		if e = decode(req.Input, &in); e != nil {
			return p, e
		}
		b, e = s.PlanEncryptBundle(ctx, in)
	} else {
		var in DecryptBundleInput
		if e = decode(req.Input, &in); e != nil {
			return p, e
		}
		b, e = s.PlanDecryptBundle(ctx, in)
	}
	if e != nil {
		return p, e
	}
	p.Input, e = json.Marshal(b)
	if e != nil {
		return p, e
	}
	p.Permissions = b.Warnings
	p.Preconditions = []string{"selected source digest and file limits are rechecked by worker", "no existing destination is replaced"}
	p.DataImpact = []string{"creates a separate reviewed output file; preserves original source", "decrypt publishes only a fully authenticated and validated plaintext bundle, without importing it"}
	p.Steps = []domain.Step{{ID: domain.NewID(), Kind: p.Operation, Description: "Transform reviewed bundle encryption envelope", Params: p.Input, SafeCancel: true}}
	return p, s.save(ctx, "plan", p.ID, p)
}
func (s *Service) executeEncryption(ctx context.Context, p domain.Plan) (any, error) {
	var b BundleEncryptionPlan
	if e := decode(p.Input, &b); e != nil {
		return nil, e
	}
	if b.Operation != p.Operation {
		return nil, domain.ErrInvalid
	}
	return s.ExecuteBundleEncryption(ctx, b)
}
