package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/migration"
)

// WorkbenchInstanceInput associates a Windows full-backup artifact with an
// explicit instance. Linux needs only ID; no runtime is discovered implicitly.
type WorkbenchInstanceInput struct {
	IncludeBindings      []string `json:"include_bindings,omitempty"`
	ID                   string   `json:"id"`
	BottlesArchivePath   string   `json:"bottles_archive_path,omitempty"`
	BottlesArchiveSHA256 string   `json:"bottles_archive_sha256,omitempty"`
}
type WorkbenchBundleInput struct {
	Path           string                   `json:"path"`
	ColdConfirmed  bool                     `json:"cold_confirmed"`
	IncludeCatalog bool                     `json:"include_catalog,omitempty"`
	Instances      []WorkbenchInstanceInput `json:"instances,omitempty"`
}
type WorkbenchBundleResult struct {
	State      string                             `json:"state"`
	Inspection migration.Inspection               `json:"inspection"`
	Instances  map[string]EnvironmentBundleResult `json:"instances"`
}

// WorkbenchSelection resolves an explicit list or the entire current catalog.
// Plans must freeze this result and lock every selected environment in ID order.
func (s *Service) WorkbenchSelection(ctx context.Context, in WorkbenchBundleInput) ([]WorkbenchInstanceInput, error) {
	selected := append([]WorkbenchInstanceInput(nil), in.Instances...)
	if len(selected) == 0 {
		rows, e := s.Store.List(ctx, "instance")
		if e != nil {
			return nil, e
		}
		for _, row := range rows {
			selected = append(selected, WorkbenchInstanceInput{ID: row.ID})
		}
	}
	if len(selected) == 0 || len(selected) > 1000 {
		return nil, errors.New("select 1..1000 instances for workbench export")
	}
	seen := map[string]bool{}
	for _, v := range selected {
		if v.ID == "" || seen[v.ID] {
			return nil, errors.New("missing or duplicate instance selection")
		}
		seen[v.ID] = true
		if _, e := s.Store.Get(ctx, "instance", v.ID); e != nil {
			return nil, e
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].ID < selected[j].ID })
	return selected, nil
}

// ExportWorkbenchBundle packages selected complete instance snapshots into one
// semantic archive. Materials are deduplicated by digest. The caller holds the
// global lock plus *all* selected environment/reference locks until publication.
func (s *Service) ExportWorkbenchBundle(ctx context.Context, in WorkbenchBundleInput) (result WorkbenchBundleResult, err error) {
	result = WorkbenchBundleResult{State: "incomplete", Instances: map[string]EnvironmentBundleResult{}}
	if !in.ColdConfirmed || !filepath.IsAbs(in.Path) || filepath.Ext(in.Path) != ".zahbundle" {
		return result, errors.New("cold confirmation and absolute .zahbundle destination required")
	}
	if _, e := os.Lstat(in.Path); !os.IsNotExist(e) {
		return result, errors.New("destination exists or is inaccessible")
	}
	selected, err := s.WorkbenchSelection(ctx, in)
	if err != nil {
		return result, err
	}
	includeCatalog := in.IncludeCatalog || len(in.Instances) == 0
	if includeCatalog {
		all, e := s.Store.List(ctx, "instance")
		if e != nil {
			return result, e
		}
		selectedIDs := map[string]bool{}
		for _, v := range selected {
			selectedIDs[v.ID] = true
		}
		for _, v := range all {
			if !selectedIDs[v.ID] {
				return result, errors.New("include_catalog requires every current instance in the frozen selection")
			}
		}
	}

	stage, err := os.MkdirTemp(filepath.Dir(in.Path), ".zah-workbench-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(stage)
	records := map[string]domain.Record{}
	payloads := map[string]migration.Entry{}
	sums := map[string]migration.Checksum{}
	resources := map[string]migration.Resource{}
	omissions := map[string]bool{}
	requirements := map[string]bool{}
	for index, item := range selected {
		part := filepath.Join(stage, fmt.Sprintf("part-%d.zahbundle", index))
		proof, e := s.ExportEnvironmentBundle(ctx, EnvironmentBundleInput{IncludeBindings: item.IncludeBindings, ID: item.ID, Path: part, ColdConfirmed: true, BottlesArchivePath: item.BottlesArchivePath, BottlesArchiveSHA256: item.BottlesArchiveSHA256})
		result.Instances[item.ID] = proof
		if e != nil {
			return result, e
		}
		extracted := filepath.Join(stage, fmt.Sprintf("part-%d", index))
		inspected, e := migration.Import(ctx, part, extracted, migration.Limits{})
		if e != nil {
			return result, e
		}
		var catalog catalogSnapshot
		if e = decode(inspected.Catalog, &catalog); e != nil {
			return result, e
		}
		if catalog.Format != environmentBundleFormat {
			return result, errors.New("unexpected intermediate catalog format")
		}
		for _, record := range catalog.Records {
			key := record.Kind + ":" + record.ID
			if old, ok := records[key]; ok && record.Kind != "family" && string(old.Data) != string(record.Data) {
				return result, errors.New("catalog changed while collecting workbench")
			}
			records[key] = record
		}
		for name, sum := range inspected.Checksums {
			if !strings.Contains(name, "/") {
				continue
			}
			if existing, ok := sums[name]; ok {
				if existing != sum {
					return result, errors.New("payload identity collision")
				}
				continue
			}
			sums[name] = sum
			payloads[name] = migration.Entry{Name: name, Path: filepath.Join(extracted, filepath.FromSlash(name))}
		}
		for _, resource := range inspected.Manifest.Resources {
			if old, ok := resources[resource.ID]; ok && old != resource {
				return result, errors.New("resource identity collision")
			}
			resources[resource.ID] = resource
		}
		for _, v := range inspected.Manifest.Omissions {
			omissions[v] = true
		}
		for _, v := range inspected.Manifest.Requirements {
			requirements[v] = true
		}
	}
	// Restore family defaults only when their chosen instance is in this archive;
	// a single-instance export may have intentionally cleared an unrelated default.
	current, refs, err := s.Store.Snapshot(ctx)
	if err != nil {
		return result, err
	}
	if includeCatalog {
		for _, rec := range current {
			records[rec.Kind+":"+rec.ID] = rec
			if rec.Kind == "artifact" {
				name := "objects/sha256/" + rec.ID
				if _, ok := payloads[name]; !ok {
					a, e := s.Artifacts().Verify(ctx, rec.ID)
					if e != nil {
						return result, e
					}
					payloads[name] = migration.Entry{Name: name, Path: a.Path}
					resources["artifact:"+rec.ID] = migration.Resource{ID: "artifact:" + rec.ID, Kind: "artifact", Status: "packaged", Path: name}
				}
			}
		}
	}

	for _, record := range current {
		if record.Kind != "family" {
			continue
		}
		key := record.Kind + ":" + record.ID
		if _, ok := records[key]; !ok {
			continue
		}
		var family domain.AppFamily
		if err = decode(record.Data, &family); err != nil {
			return result, err
		}
		if _, ok := records["instance:"+family.DefaultInstanceID]; !ok {
			family.DefaultInstanceID = ""
		}
		record.Data, _ = json.Marshal(family)
		records[key] = record
	}
	snapshot := catalogSnapshot{Format: environmentBundleFormat, SchemaVersion: 1}
	keys := make([]string, 0, len(records))
	for key := range records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		snapshot.Records = append(snapshot.Records, records[key])
	}
	for _, ref := range refs {
		_, from := records[ref.FromKind+":"+ref.FromID]
		_, to := records[ref.ToKind+":"+ref.ToID]
		if from && to {
			snapshot.References = append(snapshot.References, ref)
		}
	}
	semantic := snapshot
	semantic.Format = catalogBundleFormat
	if err = validateCatalogSnapshot(semantic); err != nil {
		return result, err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return result, err
	}
	manifest := migration.Manifest{OfflineReady: false}
	keys = keys[:0]
	for key := range resources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		manifest.Resources = append(manifest.Resources, resources[key])
	}
	for v := range omissions {
		manifest.Omissions = append(manifest.Omissions, v)
	}
	for v := range requirements {
		manifest.Requirements = append(manifest.Requirements, v)
	}
	sort.Strings(manifest.Omissions)
	sort.Strings(manifest.Requirements)
	entries := make([]migration.Entry, 0, len(payloads))
	for _, entry := range payloads {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	pending := filepath.Join(stage, "workbench.zahbundle")
	if err = migration.Export(ctx, pending, migration.Snapshot{Manifest: manifest, Catalog: raw, Bindings: json.RawMessage(`{"state":"rebind_all_external"}`)}, entries, migration.Limits{}); err != nil {
		return result, err
	}
	// Parts may have been collected at different times. Recheck every environment
	// before final publication; shared host contents were deliberately excluded.
	for _, item := range selected {
		v, e := get[domain.AppInstance](ctx, s.Store, "instance", item.ID)
		if e != nil {
			return result, e
		}
		r, env, e := s.request(ctx, v)
		if e != nil {
			return result, e
		}
		treeBytes, e := os.ReadFile(payloads["data/"+item.ID+"/tree.json"].Path)
		if e != nil {
			return result, e
		}
		var expectedTree privateTree
		if e = decode(treeBytes, &expectedTree); e != nil {
			return result, e
		}
		if _, statErr := os.Lstat(v.PrivateData); !os.IsNotExist(statErr) {
			currentTree, e := collectPrivateTree(ctx, v.PrivateData)
			if e != nil {
				return result, e
			}
			if !reflect.DeepEqual(expectedTree, currentTree) {
				return result, errors.New("private data changed while collecting other workbench instances")
			}
		}
		if e = s.verifyWorkbenchExternal(ctx, item, payloads); e != nil {
			return result, e
		}
		proof := result.Instances[item.ID]
		if proof.Linux != nil {
			if e = s.Backend.VerifyColdSnapshot(ctx, r, env.Identity, proof.Linux.ColdWitness); e != nil {
				return result, e
			}
		} else {
			// Single-instance export owns and removes its temporary archive.
			// Recheck the authenticated copy extracted from that completed part,
			// not the optional manual-input fields (empty for ordinary UI export).
			member := "environments/" + item.ID + "/bottle.tar.gz"
			entry, exists := payloads[member]
			sum, checked := sums[member]
			if !exists || !checked || proof.Windows == nil || sum.SHA256 != proof.Windows.SHA256 {
				return result, errors.New("workbench Bottle payload evidence missing or changed")
			}
			if _, e = s.Backend.ConfirmStopped(ctx, r, env.Identity); e != nil {
				return result, e
			}
			if _, e = s.Backend.VerifyManualBottlesBackup(ctx, r, entry.Path, sum.SHA256); e != nil {
				return result, e
			}
		}
	}
	result.Inspection, err = migration.Inspect(ctx, pending, migration.Limits{})
	if err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = os.Link(pending, in.Path); err != nil {
		return result, err
	}
	result.State = "published_durability_unknown"
	if err = syncPreparedDirectory(filepath.Dir(in.Path)); err != nil {
		return result, err
	}
	result.State = "workbench_snapshot_packaged_needs_target_restore"
	return result, nil
}
