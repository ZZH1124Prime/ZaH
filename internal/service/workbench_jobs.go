package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

// planWorkbench freezes selection before worker scheduling, so every selected
// environment is locked even when the original request selected the whole catalog.
func (s *Service) planWorkbench(ctx context.Context, req planRequest) (domain.Plan, error) {
	p := domain.Plan{ID: domain.NewID(), Operation: req.Operation, CreatedAt: time.Now().UTC()}
	var in WorkbenchBundleInput
	if e := decode(req.Input, &in); e != nil {
		return p, e
	}
	if !in.ColdConfirmed || !filepath.IsAbs(in.Path) || filepath.Ext(in.Path) != ".zahbundle" {
		return p, domain.ErrInvalid
	}
	if _, e := os.Lstat(in.Path); !os.IsNotExist(e) {
		return p, errors.New("export destination must not exist")
	}
	if len(in.Instances) == 0 {
		in.IncludeCatalog = true
	}
	selected, e := s.WorkbenchSelection(ctx, in)
	if e != nil {
		return p, e
	}
	in.Instances = selected
	if in.IncludeCatalog {
		rows, err := s.Store.List(ctx, "instance")
		if err != nil {
			return p, err
		}
		if len(rows) != len(selected) {
			return p, errors.New("complete catalog export requires every instance")
		}
	}
	refs := []domain.ResourceRef{}
	environments, artifacts := map[string]bool{}, map[string]bool{}
	for _, item := range selected {
		if _, e := s.selectedBindings(ctx, item.ID, item.IncludeBindings); e != nil {
			return p, e
		}
		v, e := get[domain.AppInstance](ctx, s.Store, "instance", item.ID)
		if e != nil {
			return p, e
		}
		_, env, e := s.request(ctx, v)
		if e != nil {
			return p, e
		}
		if env.Identity == "" {
			return p, errors.New("every selected instance requires a verified runtime identity")
		}
		environments[env.ID] = true
		artifacts[v.OriginalArtifact] = true
		refs = append(refs, ref("plan", p.ID, "instance", v.ID, "target"), ref("plan", p.ID, "artifact", v.OriginalArtifact, "input"))
	}
	for id := range environments {
		p.EnvironmentIDs = append(p.EnvironmentIDs, id)
	}
	for digest := range artifacts {
		p.ArtifactDigests = append(p.ArtifactDigests, digest)
	}
	sort.Strings(p.EnvironmentIDs)
	sort.Strings(p.ArtifactDigests)
	p.Input, e = json.Marshal(in)
	if e != nil {
		return p, e
	}
	p.Preconditions = []string{"all selected applications and external writers are stopped", "every selected environment locked in stable ID order", "source private data and upstream cold state rechecked before publication"}
	p.DataImpact = []string{"creates one bundle with selected independent runtime snapshots and private HOME trees", "shared materials deduplicated by SHA256; explicitly selected external content is copied; all external paths require rebinding", "may create untagged Docker snapshot images; existing images are not pruned"}
	if in.IncludeCatalog {
		p.DataImpact = append(p.DataImpact, "includes the complete material library and catalog; every catalog instance must be selected")
	}
	p.Permissions = []string{"unencrypted archive may contain private data and login state"}
	p.Steps = []domain.Step{{ID: domain.NewID(), Kind: p.Operation, Description: "Export reviewed cold workbench", Params: p.Input, SafeCancel: false}}
	return p, s.save(ctx, "plan", p.ID, p, refs...)
}

func (s *Service) executeWorkbench(ctx context.Context, p domain.Plan) (any, error) {
	var in WorkbenchBundleInput
	if e := decode(p.Input, &in); e != nil {
		return nil, e
	}
	if len(in.Instances) == 0 {
		return nil, errors.New("frozen workbench selection required")
	}
	locked := map[string]bool{}
	for _, id := range p.EnvironmentIDs {
		locked[id] = true
	}
	for _, item := range in.Instances {
		v, e := get[domain.AppInstance](ctx, s.Store, "instance", item.ID)
		if e != nil {
			return nil, e
		}
		if !locked[v.EnvironmentID] {
			return nil, errors.New("environment changed after workbench plan")
		}
	}
	return s.ExportWorkbenchBundle(ctx, in)
}
