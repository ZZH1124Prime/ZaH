package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/host"
	"github.com/ZZH1124Prime/ZaH/internal/migration"
)

type externalTree struct {
	Binding domain.FileBinding
	Tree    privateTree
}

func selectedBinding(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
func bindingMember(instance, id string) string { return "data/" + instance + "/bindings/" + id + "/" }
func safeBindingID(id string) bool {
	return id != "" && id != "." && id != ".." && len(id) <= 128 && filepath.Base(id) == id && !strings.ContainsAny(id, "\\\x00\r\n")
}
func (s *Service) selectedBindings(ctx context.Context, instance string, ids []string) ([]domain.FileBinding, error) {
	if len(ids) > 128 {
		return nil, errors.New("too many selected external directories")
	}
	seen := map[string]bool{}
	out := []domain.FileBinding{}
	for _, id := range ids {
		if !safeBindingID(id) || seen[id] {
			return nil, domain.ErrInvalid
		}
		seen[id] = true
		b, e := get[domain.FileBinding](ctx, s.Store, "binding", id)
		if e != nil {
			return nil, e
		}
		if b.InstanceID != instance || b.HostPath == "" || b.State == "needs_binding" {
			return nil, errors.New("selected directory is not a bound directory of this instance")
		}
		if e = host.VerifyBinding(host.BindingEvidence{Path: b.HostPath, Device: b.Device, Inode: b.Inode, ReadOnly: true}); e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, nil
}
func (s *Service) prepareExternalTrees(ctx context.Context, instance string, ids []string, stage string) ([]migration.Entry, []migration.Resource, []externalTree, error) {
	bindings, e := s.selectedBindings(ctx, instance, ids)
	if e != nil {
		return nil, nil, nil, e
	}
	entries := []migration.Entry{}
	resources := []migration.Resource{}
	trees := []externalTree{}
	for _, b := range bindings {
		copyRoot := filepath.Join(stage, "binding-files-"+b.ID)
		tree, e := collectExternalTree(ctx, b, copyRoot)
		if e != nil {
			return nil, nil, nil, e
		}
		raw, e := json.Marshal(tree)
		if e != nil {
			return nil, nil, nil, e
		}
		if len(raw) > 8<<20 {
			return nil, nil, nil, errors.New("external tree metadata exceeds limit")
		}
		metadata := filepath.Join(stage, "binding-"+b.ID+".json")
		if e = os.WriteFile(metadata, raw, 0600); e != nil {
			return nil, nil, nil, e
		}
		prefix := bindingMember(instance, b.ID)
		entries = append(entries, migration.Entry{Name: prefix + "tree.json", Path: metadata})
		for _, v := range tree.Entries {
			if v.Kind == "file" {
				entries = append(entries, migration.Entry{Name: prefix + "files/" + v.Path, Path: filepath.Join(copyRoot, filepath.FromSlash(v.Path))})
			}
		}
		resources = append(resources, migration.Resource{ID: "binding:" + b.ID, Kind: "binding", Status: "packaged", Path: prefix + "tree.json", Reason: "explicitly selected external contents; restores into new managed directory, not original host path"})
		trees = append(trees, externalTree{Binding: b, Tree: tree})
	}
	return entries, resources, trees, nil
}
func (s *Service) verifyExternalTrees(ctx context.Context, trees []externalTree) error {
	for _, v := range trees {
		b := v.Binding
		if e := host.VerifyBinding(host.BindingEvidence{Path: b.HostPath, Device: b.Device, Inode: b.Inode, ReadOnly: true}); e != nil {
			return e
		}
		after, e := collectExternalTree(ctx, b, "")
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(after, v.Tree) {
			return errors.New("selected external directory changed during cold backup")
		}
	}
	return nil
}
func (s *Service) verifyWorkbenchExternal(ctx context.Context, item WorkbenchInstanceInput, payloads map[string]migration.Entry) error {
	bindings, e := s.selectedBindings(ctx, item.ID, item.IncludeBindings)
	if e != nil {
		return e
	}
	trees := []externalTree{}
	for _, b := range bindings {
		entry, ok := payloads[bindingMember(item.ID, b.ID)+"tree.json"]
		if !ok {
			return errors.New("selected external directory tree missing")
		}
		raw, e := os.ReadFile(entry.Path)
		if e != nil {
			return e
		}
		var tree privateTree
		if e = decode(raw, &tree); e != nil {
			return e
		}
		trees = append(trees, externalTree{Binding: b, Tree: tree})
	}
	return s.verifyExternalTrees(ctx, trees)
}
func (s *Service) restoreExternalTrees(ctx context.Context, stage, oldInstance, root string, original catalogSnapshot, inspection migration.Inspection, idMap map[string]string, records []domain.Record, allowed map[string]bool) error {
	for _, rec := range original.Records {
		if rec.Kind != "binding" {
			continue
		}
		var b domain.FileBinding
		if e := decode(rec.Data, &b); e != nil {
			return e
		}
		if b.InstanceID != oldInstance {
			continue
		}
		prefix := bindingMember(oldInstance, b.ID)
		packaged := false
		for _, resource := range inspection.Manifest.Resources {
			if resource.ID == "binding:"+b.ID && resource.Status == "packaged" {
				if !safeBindingID(b.ID) || resource.Path != prefix+"tree.json" {
					return errors.New("external tree resource identity differs")
				}
				packaged = true
			}
		}
		if !packaged {
			continue
		}
		member := prefix + "tree.json"
		sum, ok := inspection.Checksums[member]
		if !ok || sum.Size > 8<<20 {
			return errors.New("external tree metadata missing or excessive")
		}
		raw, e := os.ReadFile(filepath.Join(stage, filepath.FromSlash(member)))
		if e != nil {
			return e
		}
		var tree privateTree
		if e = decode(raw, &tree); e != nil {
			return e
		}
		newID := idMap["binding:"+b.ID]
		if !safeBindingID(newID) {
			return errors.New("new external binding identity missing")
		}
		parent := filepath.Join(root, "bindings")
		if e = os.MkdirAll(parent, 0700); e != nil {
			return e
		}
		target := filepath.Join(parent, newID)
		allowed[member] = true
		if e = restoreManagedTree(ctx, stage, prefix+"files/", target, tree, inspection.Checksums, allowed); e != nil {
			return e
		}
		updated := false
		for i, row := range records {
			if row.Kind == "binding" && row.ID == newID {
				var current domain.FileBinding
				if e = decode(row.Data, &current); e != nil {
					return e
				}
				current.RestoredData = target
				current.HostPath = ""
				current.State = "needs_binding"
				records[i].Data, e = json.Marshal(current)
				if e != nil {
					return e
				}
				updated = true
			}
		}
		if !updated {
			return errors.New("restored external binding record missing")
		}
		if e = syncPreparedDirectory(parent); e != nil {
			return e
		}
	}
	return nil
}
