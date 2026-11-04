package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/infra"
	"github.com/ZZH1124Prime/ZaH/internal/migration"
)

const environmentBundleFormat = "zah.instance-environment"

// EnvironmentBundleInput selects one instance. ColdConfirmed is the explicit
// user's assertion that external writers/upstream GUI operations have stopped.
// It never substitutes for Docker's actual cold-state check.
type EnvironmentBundleInput struct {
	IncludeBindings      []string `json:"include_bindings,omitempty"`
	ID                   string   `json:"id"`
	Path                 string   `json:"path"`
	ColdConfirmed        bool     `json:"cold_confirmed"`
	BottlesArchivePath   string   `json:"bottles_archive_path,omitempty"`
	BottlesArchiveSHA256 string   `json:"bottles_archive_sha256,omitempty"`
}

// EnvironmentBundleResult retains backend resource evidence even if packaging
// fails after Docker committed an image. Existing resources are never pruned.
type EnvironmentBundleResult struct {
	State      string                         `json:"state"`
	Inspection migration.Inspection           `json:"inspection"`
	Linux      *backend.SnapshotEvidence      `json:"linux,omitempty"`
	Windows    *backend.BottlesBackupEvidence `json:"windows,omitempty"`
}

// PendingEnvironmentRestore identifies bytes restored locally, not a live
// environment. Image loading/manual upstream import and entry verification are
// separate operations; no imported ownership or readiness evidence is trusted.
type PendingEnvironmentRestore struct {
	AttemptToken  string                      `json:"attempt_token,omitempty"`
	BottleProfile *backend.PortableBottle     `json:"bottle_profile,omitempty"`
	BottleState   *backend.BottleRebuildState `json:"bottle_state,omitempty"`
	SchemaVersion int                         `json:"schema_version"`
	Backend       string                      `json:"backend"`
	ArchivePath   string                      `json:"archive_path"`
	ArchiveSHA256 string                      `json:"archive_sha256"`
	ImageID       string                      `json:"image_id,omitempty"`
	State         string                      `json:"state"`
}

type privateTree struct {
	SchemaVersion int                `json:"schema_version"`
	Entries       []privateTreeEntry `json:"entries"`
}
type privateTreeEntry struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Target string `json:"target,omitempty"`
}

type treeReader struct {
	ctx context.Context
	r   io.Reader
}

func (r treeReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
func treeHash(ctx context.Context, p string) (string, int64, error) {
	f, e := openPrivateRegular(p)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	before, e := f.Stat()
	if e != nil {
		return "", 0, e
	}
	if !before.Mode().IsRegular() || before.Size() > 32<<30 {
		return "", 0, errors.New("private file type or size unsupported")
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(treeReader{ctx, f}, (32<<30)+1))
	if e != nil {
		return "", 0, e
	}
	after, e := f.Stat()
	if e != nil || before.Size() != n || n > 32<<30 || after.Size() != n || !after.ModTime().Equal(before.ModTime()) {
		return "", 0, errors.New("private file changed while reading")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
func treePath(p string) bool {
	return p != "" && len(p) <= 4096 && filepath.IsLocal(filepath.FromSlash(p)) && filepath.ToSlash(filepath.Clean(filepath.FromSlash(p))) == p && !strings.ContainsAny(p, "\\\x00\r\n")
}

// collectPrivateTree never follows links while walking. Links resolving inside
// the same HOME are normalized to relative targets; external/dangling/cyclic
// links and special files are explicit errors rather than silently omitted data.
func collectPrivateTree(ctx context.Context, root string) (privateTree, error) {
	tree := privateTree{SchemaVersion: 1}
	if e := infra.ValidatePrivateDir(root); e != nil {
		return tree, e
	}
	var total int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		if !treePath(rel) {
			return errors.New("unsupported private-data path")
		}
		if len(tree.Entries) >= 90000 {
			return errors.New("private tree entry count exceeds limit")
		}
		st, e := d.Info()
		if e != nil {
			return e
		}
		item := privateTreeEntry{Path: rel, Mode: uint32(st.Mode().Perm() & 0700)}
		switch {
		case d.IsDir():
			item.Kind = "directory"
		case d.Type()&os.ModeSymlink != 0:
			target, e := filepath.EvalSymlinks(p)
			if e != nil {
				return fmt.Errorf("private link %q is dangling or cyclic; rebind before export", rel)
			}
			targetRel, e := filepath.Rel(root, target)
			if e != nil || !filepath.IsLocal(targetRel) {
				return fmt.Errorf("private link %q points outside managed HOME; explicit target rebinding is required", rel)
			}
			targetInfo, e := os.Stat(target)
			if e != nil || (!targetInfo.IsDir() && !targetInfo.Mode().IsRegular()) {
				return fmt.Errorf("private link %q targets a special file", rel)
			}
			normalized, e := filepath.Rel(filepath.Dir(p), target)
			if e != nil {
				return e
			}
			item.Kind = "symlink"
			item.Mode = 0
			item.Target = filepath.ToSlash(normalized)
		case d.Type().IsRegular():
			item.Kind = "file"
			item.SHA256, item.Size, e = treeHash(ctx, p)
			if e != nil {
				return e
			}
			if item.Size > (64<<30)-total {
				return errors.New("private data exceeds bundle size limit")
			}
			total += item.Size
		default:
			return fmt.Errorf("private data contains unsupported socket/device/FIFO: %s", rel)
		}
		tree.Entries = append(tree.Entries, item)
		return nil
	})
	return tree, err
}

func (s *Service) instanceSnapshot(ctx context.Context, id string) (catalogSnapshot, domain.AppInstance, error) {
	var empty catalogSnapshot
	v, e := get[domain.AppInstance](ctx, s.Store, "instance", id)
	if e != nil {
		return empty, v, e
	}
	rel, e := get[domain.AppRelease](ctx, s.Store, "release", v.ReleaseID)
	if e != nil {
		return empty, v, e
	}
	all, refs, e := s.Store.Snapshot(ctx)
	if e != nil {
		return empty, v, e
	}
	selected := map[string]bool{"instance:" + v.ID: true, "release:" + v.ReleaseID: true, "family:" + rel.FamilyID: true, "environment:" + v.EnvironmentID: true, "recipe:" + v.RecipeRevision: true}
	if rel.IconSHA256 != "" {
		selected["artifact:"+rel.IconSHA256] = true
	}
	for _, a := range rel.Artifacts {
		selected["artifact:"+a] = true
	}
	for _, record := range all {
		if record.Kind == "binding" {
			var b domain.FileBinding
			if e = json.Unmarshal(record.Data, &b); e != nil {
				return empty, v, e
			}
			if b.InstanceID == v.ID {
				selected[record.Kind+":"+record.ID] = true
			}
		}
		if record.Kind == "capability" {
			var c domain.CapabilityResult
			if e = json.Unmarshal(record.Data, &c); e != nil {
				return empty, v, e
			}
			if c.InstanceID == v.ID {
				selected[record.Kind+":"+record.ID] = true
			}
		}
	}
	snap := catalogSnapshot{Format: environmentBundleFormat, SchemaVersion: 1}
	for _, rec := range all {
		if !selected[rec.Kind+":"+rec.ID] {
			continue
		}
		if rec.Kind == "family" {
			var f domain.AppFamily
			if e = json.Unmarshal(rec.Data, &f); e != nil {
				return empty, v, e
			}
			if f.DefaultInstanceID != v.ID {
				f.DefaultInstanceID = ""
			}
			rec.Data, _ = json.Marshal(f)
		}
		snap.Records = append(snap.Records, rec)
	}
	for _, ref := range refs {
		if selected[ref.FromKind+":"+ref.FromID] && selected[ref.ToKind+":"+ref.ToID] {
			snap.References = append(snap.References, ref)
		}
	}
	semantics := snap
	semantics.Format = catalogBundleFormat
	if e = validateCatalogSnapshot(semantics); e != nil {
		return empty, v, e
	}
	return snap, v, nil
}

// ExportEnvironmentBundle includes selected instance material, semantic catalog,
// a cold backend snapshot and the complete managed HOME tree. External bindings
// are explicitly rebind-only. Caller holds global/environment/reference locks.
func (s *Service) ExportEnvironmentBundle(ctx context.Context, in EnvironmentBundleInput) (result EnvironmentBundleResult, err error) {
	result.State = "incomplete"
	if !in.ColdConfirmed {
		return result, errors.New("confirm all application and external writers are stopped before cold export")
	}
	if !filepath.IsAbs(in.Path) || filepath.Ext(in.Path) != ".zahbundle" {
		return result, domain.ErrInvalid
	}
	if _, e := os.Lstat(in.Path); !os.IsNotExist(e) {
		return result, errors.New("bundle destination already exists or is inaccessible")
	}
	snap, v, err := s.instanceSnapshot(ctx, in.ID)
	if err != nil {
		return result, err
	}
	r, env, err := s.request(ctx, v)
	if err != nil {
		return result, err
	}
	if env.Identity == "" {
		return result, errors.New("registered environment identity required for a full instance backup")
	}
	// Stage the complete bundle on the destination filesystem; the user's final
	// name appears only after tree and backend consistency have been rechecked.
	stage, err := os.MkdirTemp(filepath.Dir(in.Path), ".zah-instance-export-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(stage)
	manifest := migration.Manifest{OfflineReady: false, Omissions: []string{"unselected external host bindings are not copied; selected contents restore to new managed directories and require explicit rebinding", "live process state, host keyrings and device-bound authentication are not portable"}, Requirements: []string{"prepare compatible backend/runtime on target", "rebuild or import environment through supported upstream workflow and verify entry/capabilities"}}
	entries := []migration.Entry{}
	for _, rec := range snap.Records {
		switch rec.Kind {
		case "artifact":
			a, e := s.Artifacts().Verify(ctx, rec.ID)
			if e != nil {
				return result, e
			}
			name := "objects/sha256/" + rec.ID
			entries = append(entries, migration.Entry{Name: name, Path: a.Path})
			manifest.Resources = append(manifest.Resources, migration.Resource{ID: "artifact:" + rec.ID, Kind: "artifact", Status: "packaged", Path: name})
		case "binding":
			if !selectedBinding(in.IncludeBindings, rec.ID) {
				manifest.Resources = append(manifest.Resources, migration.Resource{ID: "binding:" + rec.ID, Kind: "binding", Status: "rebind", Reason: "external content not selected for packing"})
			}
		}
	}
	externalEntries, externalResources, externalTrees, e := s.prepareExternalTrees(ctx, v.ID, in.IncludeBindings, stage)
	if e != nil {
		return result, e
	}
	entries = append(entries, externalEntries...)
	manifest.Resources = append(manifest.Resources, externalResources...)
	backendMember := "environments/" + v.ID + "/image.tar"
	backendPath := filepath.Join(stage, "image.tar")
	switch r.Recipe.Platform {
	case "linux":
		evidence, e := s.Backend.ExportLinuxSnapshot(ctx, r, env.Identity, backendPath)
		result.Linux = &evidence
		if e != nil {
			return result, e
		}
	case "windows":
		if r.Windows == nil {
			return result, errors.New("verified bottle registration required")
		}
		if env.LaunchLease || env.LaunchToken != "" || env.StopRequested {
			return result, errors.New("stop the application before exporting its Bottle")
		}
		if _, e := s.Backend.ConfirmStopped(ctx, r, env.Identity); e != nil {
			return result, e
		}
		if in.BottlesArchivePath == "" {
			generated, e := s.Backend.ExportBottlesSnapshot(ctx, r, env.Identity, filepath.Join(stage, "bottle.tar.gz"))
			if e != nil {
				return result, e
			}
			in.BottlesArchivePath = generated.Path
			in.BottlesArchiveSHA256 = generated.SHA256
		}
		evidence, e := s.Backend.VerifyManualBottlesBackup(ctx, r, in.BottlesArchivePath, in.BottlesArchiveSHA256)
		result.Windows = &evidence
		if e != nil {
			return result, e
		}
		profile, e := s.Backend.PortableBottle(ctx, r)
		if e != nil {
			return result, e
		}
		profileBytes, e := json.Marshal(profile)
		if e != nil {
			return result, e
		}
		profilePath := filepath.Join(stage, "restore.json")
		if e = os.WriteFile(profilePath, profileBytes, 0600); e != nil {
			return result, e
		}
		entries = append(entries, migration.Entry{Name: "environments/" + v.ID + "/restore.json", Path: profilePath})
		backendMember = "environments/" + v.ID + "/bottle.tar.gz"
		backendPath = in.BottlesArchivePath
		manifest.Omissions = append(manifest.Omissions, evidence.Excluded...)
	default:
		return result, domain.ErrInvalid
	}
	entries = append(entries, migration.Entry{Name: backendMember, Path: backendPath})
	manifest.Resources = append(manifest.Resources, migration.Resource{ID: "environment:" + env.ID, Kind: env.Backend, Status: "packaged", Path: backendMember}, migration.Resource{ID: "host-runtime:" + v.ID, Kind: "runtime", Status: "target_required", Reason: "target Docker/Distrobox or Flatpak/Bottles/runner verification required"})
	// Windows data lives inside the full bottle backup. Preserve the manager's
	// private HOME too when it exists; an empty one is an explicitly recorded tree.
	var tree privateTree
	if _, e := os.Lstat(v.PrivateData); os.IsNotExist(e) && r.Recipe.Platform == "windows" {
		tree = privateTree{SchemaVersion: 1, Entries: []privateTreeEntry{{Path: ".", Kind: "directory", Mode: 0700}}}
	} else {
		tree, err = collectPrivateTree(ctx, v.PrivateData)
		if err != nil {
			return result, err
		}
	}
	for _, item := range tree.Entries {
		if item.Kind == "file" {
			entries = append(entries, migration.Entry{Name: "data/" + v.ID + "/home/" + item.Path, Path: filepath.Join(v.PrivateData, filepath.FromSlash(item.Path))})
		}
	}
	treeBytes, err := json.Marshal(tree)
	if err != nil {
		return result, err
	}
	if len(treeBytes) > 8<<20 {
		return result, errors.New("private tree metadata exceeds limit")
	}
	treePath := filepath.Join(stage, "tree.json")
	if err = os.WriteFile(treePath, treeBytes, 0600); err != nil {
		return result, err
	}
	treeMember := "data/" + v.ID + "/tree.json"
	entries = append(entries, migration.Entry{Name: treeMember, Path: treePath})
	manifest.Resources = append(manifest.Resources, migration.Resource{ID: "private-data:" + v.ID, Kind: "private_data", Status: "packaged", Path: treeMember})
	catalogJSON, err := json.Marshal(snap)
	if err != nil {
		return result, err
	}
	pending := filepath.Join(stage, "instance.zahbundle")
	if err = migration.Export(ctx, pending, migration.Snapshot{Manifest: manifest, Catalog: catalogJSON, Bindings: json.RawMessage(`{"state":"rebind_all_external"}`)}, entries, migration.Limits{}); err != nil {
		return result, err
	}
	if _, e := os.Lstat(v.PrivateData); !os.IsNotExist(e) {
		after, e := collectPrivateTree(ctx, v.PrivateData)
		if e != nil {
			return result, e
		}
		if !reflect.DeepEqual(tree, after) {
			return result, errors.New("private data changed during packaging")
		}
	}
	if err = s.verifyExternalTrees(ctx, externalTrees); err != nil {
		return result, err
	}
	if r.Recipe.Platform == "linux" {
		if err = s.Backend.VerifyColdSnapshot(ctx, r, env.Identity, result.Linux.ColdWitness); err != nil {
			return result, err
		}
	} else {
		if _, err = s.Backend.ConfirmStopped(ctx, r, env.Identity); err != nil {
			return result, err
		}
		if _, err = s.Backend.VerifyManualBottlesBackup(ctx, r, in.BottlesArchivePath, in.BottlesArchiveSHA256); err != nil {
			return result, err
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
	dir, e := os.Open(filepath.Dir(in.Path))
	if e != nil {
		return result, e
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return result, err
	}
	result.State = "instance_snapshot_packaged_needs_target_restore"
	return result, nil
}

// RestoreEnvironmentBundle verifies bytes, recreates new private data trees and
// retains backend archives before atomically registering remapped identities.
// It invokes no backend, loads no image and never marks an instance ready.
func (s *Service) RestoreEnvironmentBundle(ctx context.Context, archive string, options ...RestoreOptions) (CatalogRestoreResult, error) {
	result, err := s.restoreBundle(ctx, archive, environmentBundleFormat, func(stage string, inspection migration.Inspection, records []domain.Record, idMap map[string]string) (func(), error) {
		created := []string{}
		cleanup := func() {
			for _, p := range created {
				// These are newly created rollback-only trees; restore traversal before
				// cleanup because archived directory modes may be read-only.
				_ = filepath.WalkDir(p, func(name string, d fs.DirEntry, e error) error {
					if e == nil && d.IsDir() {
						return os.Chmod(name, 0700)
					}
					return e
				})
				_ = os.RemoveAll(p)
			}
		}
		fail := func(e error) (func(), error) { return cleanup, e }
		var original catalogSnapshot
		if e := decode(inspection.Catalog, &original); e != nil {
			return fail(e)
		}
		instances := []domain.AppInstance{}
		for _, rec := range original.Records {
			if rec.Kind == "instance" {
				var inst domain.AppInstance
				if e := decode(rec.Data, &inst); e != nil {
					return fail(e)
				}
				instances = append(instances, inst)
			}
		}
		if len(instances) == 0 || len(instances) > 1000 {
			return fail(errors.New("environment bundle must contain 1..1000 instances"))
		}
		dataRoot := s.Paths.Data
		var managedRoot *domain.ManagedDataRoot
		if len(options) == 1 && options[0].Directory != "" {
			current, e := inspectRestoreParent(options[0].Directory)
			if e != nil {
				return fail(e)
			}
			if options[0].Parent != nil && *current != *options[0].Parent {
				return fail(errors.New("selected restore parent changed after planning"))
			}
			// Avoid creating a directory when every version was skipped.
			for _, v := range instances {
				if idMap["instance:"+v.ID] != "" {
					dataRoot = filepath.Join(current.Path, "zah-import-"+domain.NewID())
					if e = os.Mkdir(dataRoot, 0700); e != nil {
						return fail(e)
					}
					created = append(created, dataRoot)
					managedRoot, e = inspectRestoreParent(dataRoot)
					if e != nil {
						return fail(e)
					}
					break
				}
			}
		}
		allowed := map[string]bool{"manifest.json": true, "catalog.json": true, "bindings.json": true, "checksums.json": true}
		for _, old := range instances {
			id := idMap["instance:"+old.ID]
			environmentID := idMap["environment:"+old.EnvironmentID]
			if id == "" && len(options) == 1 && options[0].Merge {
				for member := range inspection.Checksums {
					if skippedPayload(member, old.ID) {
						allowed[member] = true
					}
				}
				continue
			}
			if id == "" || environmentID == "" {
				return fail(errors.New("instance identity remapping missing"))
			}
			root := filepath.Join(dataRoot, "instances", id)
			if e := infra.EnsurePrivateDir(filepath.Dir(root)); e != nil {
				return fail(e)
			}
			if e := os.Mkdir(root, 0700); e != nil {
				return fail(e)
			}
			created = append(created, root)
			treeMember := "data/" + old.ID + "/tree.json"
			treeFile := filepath.Join(stage, filepath.FromSlash(treeMember))
			treeInfo, e := os.Stat(treeFile)
			if e != nil {
				return fail(e)
			}
			if treeInfo.Size() > 8<<20 {
				return fail(errors.New("private tree metadata exceeds limit"))
			}
			raw, e := os.ReadFile(treeFile)
			if e != nil {
				return fail(e)
			}
			var tree privateTree
			if e = decode(raw, &tree); e != nil {
				return fail(e)
			}
			allowed[treeMember] = true
			home := filepath.Join(root, "home")
			if e = restorePrivateTree(ctx, stage, old.ID, home, tree, inspection.Checksums, allowed); e != nil {
				return fail(e)
			}
			if e = s.restoreExternalTrees(ctx, stage, old.ID, root, original, inspection, idMap, records, allowed); e != nil {
				return fail(e)
			}
			var envIndex = -1
			var env domain.Environment
			for i, rec := range records {
				if rec.Kind == "environment" && rec.ID == environmentID {
					envIndex = i
					if e = decode(rec.Data, &env); e != nil {
						return fail(e)
					}
				}
			}
			if envIndex < 0 {
				return fail(errors.New("restored environment record missing"))
			}
			filename := "image.tar"
			if env.Backend == "bottles-flatpak" {
				filename = "bottle.tar.gz"
			}
			member := "environments/" + old.ID + "/" + filename
			allowed[member] = true
			sum, ok := inspection.Checksums[member]
			if !ok {
				return fail(errors.New("backend snapshot missing"))
			}
			archivePath := filepath.Join(root, filename)
			if e = copyTreeFile(ctx, filepath.Join(stage, filepath.FromSlash(member)), archivePath, sum.SHA256, sum.Size, 0600); e != nil {
				return fail(e)
			}
			pending := PendingEnvironmentRestore{SchemaVersion: 1, Backend: env.Backend, ArchivePath: archivePath, ArchiveSHA256: sum.SHA256, State: "needs_upstream_restore_and_rebind"}
			if env.Backend == "distrobox-docker" {
				info, e := backend.InspectDockerArchive(ctx, archivePath, backend.MaxSnapshotBytes)
				if e != nil {
					return fail(e)
				}
				if info.SHA256 != sum.SHA256 {
					return fail(errors.New("snapshot checksum differs"))
				}
				pending.ImageID = info.ImageID
			} else if env.Backend == "bottles-flatpak" {
				profileMember := "environments/" + old.ID + "/restore.json"
				if check, ok := inspection.Checksums[profileMember]; ok {
					if check.Size > 64<<10 {
						return fail(errors.New("Bottle restore metadata exceeds limit"))
					}
					raw, e := os.ReadFile(filepath.Join(stage, filepath.FromSlash(profileMember)))
					if e != nil {
						return fail(e)
					}
					var profile backend.PortableBottle
					if e = decode(raw, &profile); e != nil {
						return fail(e)
					}
					pending.BottleProfile = &profile
					allowed[profileMember] = true
				}
				if _, e := backend.InspectBottlesBackupArchive(ctx, archivePath, sum.SHA256); e != nil {
					return fail(e)
				}
			}

			env.State = "restore_pending"
			env.PendingRestore, _ = json.Marshal(pending)
			records[envIndex].Data, _ = json.Marshal(env)
			for i, rec := range records {
				if rec.Kind == "instance" && rec.ID == id {
					var inst domain.AppInstance
					if e = decode(rec.Data, &inst); e != nil {
						return fail(e)
					}
					inst.State = "needs_runtime_restore"
					inst.PrivateData = home
					inst.RestoreRoot = managedRoot
					inst.Entry = nil
					records[i].Data, _ = json.Marshal(inst)
				}
			}
			for _, dir := range []string{root, filepath.Dir(root), dataRoot} {
				if e := syncPreparedDirectory(dir); e != nil {
					return fail(e)
				}
			}
		}
		for key := range inspection.Checksums {
			if strings.HasPrefix(key, "objects/sha256/") {
				continue
			}
			if !allowed[key] {
				return fail(fmt.Errorf("unexpected full-instance payload %q", key))
			}
		}
		if managedRoot != nil {
			if e := syncPreparedDirectory(filepath.Dir(dataRoot)); e != nil {
				return fail(e)
			}
		}
		return cleanup, nil
	}, options...)
	if err == nil {
		result.State = "needs_runtime_restore_and_rebind"
	}
	return result, err
}

func restorePrivateTree(ctx context.Context, stage, oldID, home string, tree privateTree, checks map[string]migration.Checksum, allowed map[string]bool) error {
	return restoreManagedTree(ctx, stage, "data/"+oldID+"/home/", home, tree, checks, allowed)
}
func restoreManagedTree(ctx context.Context, stage, prefix, home string, tree privateTree, checks map[string]migration.Checksum, allowed map[string]bool) error {
	if tree.SchemaVersion != 1 || len(tree.Entries) == 0 || len(tree.Entries) > 90000 {
		return errors.New("invalid private tree metadata")
	}
	seen := map[string]string{}
	for _, item := range tree.Entries {
		if !treePath(item.Path) || seen[item.Path] != "" || item.Mode&^uint32(0700) != 0 {
			return errors.New("invalid private tree path or mode")
		}
		seen[item.Path] = item.Kind
		switch item.Kind {
		case "directory":
			if item.Target != "" || item.SHA256 != "" || item.Size != 0 {
				return errors.New("invalid directory metadata")
			}
		case "file":
			if item.Path == "." || item.Target != "" || item.Size < 0 {
				return errors.New("invalid private file metadata")
			}
		case "symlink":
			if item.Path == "." || item.Mode != 0 || item.SHA256 != "" || item.Size != 0 || filepath.IsAbs(item.Target) || strings.ContainsAny(item.Target, "\\\x00\r\n") || !treePath(filepath.ToSlash(filepath.Join(filepath.Dir(item.Path), item.Target))) {
				return errors.New("unsafe private symlink target")
			}
		default:
			return errors.New("unknown private tree kind")
		}
	}
	if seen["."] != "directory" {
		return errors.New("private tree root missing")
	}
	for _, item := range tree.Entries {
		if item.Path == "." {
			continue
		}
		parent := filepath.ToSlash(filepath.Dir(item.Path))
		if seen[parent] != "directory" {
			return errors.New("private tree parent is not a declared directory")
		}
	}
	items := append([]privateTreeEntry(nil), tree.Entries...)
	sort.Slice(items, func(i, j int) bool { return len(items[i].Path) < len(items[j].Path) })
	for _, item := range items {
		if item.Kind == "directory" {
			if e := os.Mkdir(filepath.Join(home, filepath.FromSlash(item.Path)), 0700); e != nil {
				return e
			}
		}
	}
	for _, item := range items {
		if item.Kind != "file" {
			continue
		}
		member := prefix + item.Path
		sum, ok := checks[member]
		if !ok || sum.SHA256 != item.SHA256 || sum.Size != item.Size {
			return errors.New("private file is missing or metadata differs from checksums")
		}
		allowed[member] = true
		if e := copyTreeFile(ctx, filepath.Join(stage, filepath.FromSlash(member)), filepath.Join(home, filepath.FromSlash(item.Path)), item.SHA256, item.Size, os.FileMode(item.Mode)); e != nil {
			return e
		}
	}
	for _, item := range items {
		if item.Kind == "symlink" {
			if e := os.Symlink(filepath.FromSlash(item.Target), filepath.Join(home, filepath.FromSlash(item.Path))); e != nil {
				return e
			}
		}
	}
	for _, item := range items {
		if item.Kind == "symlink" {
			target, e := filepath.EvalSymlinks(filepath.Join(home, filepath.FromSlash(item.Path)))
			if e != nil {
				return errors.New("restored private symlink is dangling or cyclic")
			}
			relative, e := filepath.Rel(home, target)
			if e != nil || !filepath.IsLocal(relative) {
				return errors.New("restored private symlink escapes HOME")
			}
		}
	}
	// Restore restrictive directory modes last, after child writes. Keep root 0700
	// so future manager operations cannot accidentally expose the private tree.
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		if item.Kind == "directory" && item.Path != "." {
			p := filepath.Join(home, filepath.FromSlash(item.Path))
			f, e := os.Open(p)
			if e != nil {
				return e
			}
			if e = os.Chmod(p, os.FileMode(item.Mode)); e == nil {
				e = f.Sync()
			}
			f.Close()
			if e != nil {
				return e
			}
		}
	}
	return syncPreparedDirectory(home)
}
func copyTreeFile(ctx context.Context, src, dst, digest string, size int64, mode os.FileMode) error {
	st, e := os.Lstat(src)
	if e != nil {
		return e
	}
	if !st.Mode().IsRegular() || st.Size() != size {
		return errors.New("staged file type or size changed")
	}
	in, e := openPrivateRegular(src)
	if e != nil {
		return e
	}
	actual, e := in.Stat()
	if e != nil || !os.SameFile(st, actual) {
		in.Close()
		return errors.New("staged source identity changed")
	}
	defer in.Close()
	out, e := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer out.Close()
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(out, h), io.LimitReader(treeReader{ctx, in}, size+1))
	if e != nil {
		return e
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != digest {
		return errors.New("staged file checksum changed")
	}
	if e = out.Chmod(mode); e != nil {
		return e
	}
	return out.Sync()
}

func openPrivateRegular(p string) (*os.File, error) {
	fd, e := syscall.Open(p, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), p)
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("source must be a non-symlink regular file")
	}
	return f, nil
}
func syncPreparedDirectory(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
