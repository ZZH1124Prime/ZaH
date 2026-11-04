package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/catalog"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/infra"
)

// CleanupResult describes one reviewed cleanup operation. BytesRemoved counts
// logical file lengths unlinked, not guaranteed reclaimed physical storage.
// PrivateDataPreserved is false only for explicit instance.data.remove.
type CleanupResult struct {
	ID                   string `json:"id"`
	State                string `json:"state"`
	BytesRemoved         int64  `json:"bytes_removed"`
	AuditRetained        bool   `json:"audit_retained"`
	PrivateDataPreserved bool   `json:"private_data_preserved"`
}
type cleanupIntent struct {
	ID       string `json:"id"`
	Device   uint64 `json:"device"`
	Inode    uint64 `json:"inode"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
	HadFile  bool   `json:"had_file"`
}

// PlanCleanup builds an immutable cleanup intent. The caller saves the plan
// and its declared target references through the ordinary plan repository.
func (s *Service) PlanCleanup(ctx context.Context, raw json.RawMessage) (domain.Plan, error) {
	var input struct {
		Operation          string `json:"operation"`
		ID                 string `json:"id"`
		ColdConfirmed      bool   `json:"cold_confirmed,omitempty"`
		ConfirmPrivateData bool   `json:"confirm_private_data,omitempty"`
	}
	if e := decode(raw, &input); e != nil {
		return domain.Plan{}, e
	}
	if input.Operation == "instance.forget" || input.Operation == "instance.data.remove" {
		return s.planInstanceCleanup(ctx, input.Operation, input.ID, input.ColdConfirmed, input.ConfirmPrivateData, cleanupChecks{procRoot: "/proc"})
	}
	if input.Operation != "artifact.remove" || !catalog.ValidDigest(input.ID) {
		return domain.Plan{}, domain.ErrInvalid
	}
	p := domain.Plan{ID: domain.NewID(), Operation: input.Operation, ArtifactDigests: []string{input.ID}, CreatedAt: time.Now().UTC()}
	a, e := get[catalog.Artifact](ctx, s.Store, "artifact", input.ID)
	if e != nil {
		return p, e
	}
	if a.SHA256 != input.ID {
		return p, errors.New("material record digest differs from identity")
	}
	if a.Availability == "removed" {
		return p, fmt.Errorf("%w: material is already removed", domain.ErrConflict)
	}
	if e = s.checkCleanupReferences(ctx, input.ID, p.ID); e != nil {
		return p, e
	}
	intent, e := s.inspectCleanupMaterial(ctx, a)
	if e != nil {
		return p, e
	}
	p.Input, e = json.Marshal(intent)
	if e != nil {
		return p, e
	}
	p.Preconditions = []string{"material digest and exact managed file identity are rechecked", "no release, instance, unsubmitted plan or nonterminal job references the material", "completed plan and job audit records remain"}
	p.DataImpact = []string{"permanently deletes only the independently archived digest object; original source files are preserved", "retains material metadata and provenance as a removed tombstone", "private application data, installed environments and shared runtime layers are preserved"}
	p.Steps = []domain.Step{{ID: domain.NewID(), Kind: p.Operation, Description: "Remove unreferenced archived material and retain audit metadata", Params: p.Input, SafeCancel: false}}
	return p, nil
}

// checkCleanupReferences permits historical plan evidence only when every job
// submitted for that plan is terminal. An unsubmitted plan still protects input.
func (s *Service) checkCleanupReferences(ctx context.Context, digest, ownPlan string) error {
	refs, e := s.Store.References(ctx, "artifact", digest)
	if e != nil {
		return e
	}
	jobs, e := s.Store.List(ctx, "job")
	if e != nil {
		return e
	}
	plansWithJobs := map[string]bool{}
	activePlans := map[string]bool{}
	for _, r := range jobs {
		var j domain.Job
		if e = json.Unmarshal(r.Data, &j); e != nil {
			return e
		}
		plansWithJobs[j.PlanID] = true
		if !j.State.Terminal() {
			activePlans[j.PlanID] = true
		}
	}
	for _, r := range refs {
		if r.FromKind == "plan" && r.FromID == ownPlan {
			continue
		}
		if r.FromKind != "plan" || !plansWithJobs[r.FromID] || activePlans[r.FromID] {
			return fmt.Errorf("%w: material retained by %s %s", domain.ErrReferenced, r.FromKind, r.FromID)
		}
	}
	return nil
}
func (s *Service) inspectCleanupMaterial(ctx context.Context, a catalog.Artifact) (cleanupIntent, error) {
	intent := cleanupIntent{ID: a.SHA256}
	if a.Availability != "" && a.Availability != "available" && a.Availability != "removal_pending" {
		return intent, errors.New("material availability requires inspection")
	}
	path, e := s.Artifacts().Path(a.SHA256)
	if e != nil {
		return intent, e
	}
	if filepath.Clean(a.Path) != path {
		return intent, errors.New("material record does not identify its managed digest path")
	}
	if e = infra.ValidatePrivateDir(s.Artifacts().Root); e != nil {
		return intent, e
	}
	st, e := os.Lstat(path)
	if errors.Is(e, os.ErrNotExist) && a.Availability == "removal_pending" {
		return intent, nil
	}
	if e != nil {
		return intent, e
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 || sys.Uid != uint32(os.Geteuid()) || sys.Nlink != 1 {
		return intent, errors.New("material must be a singly-linked regular file owned by this user")
	}
	verified, e := s.Artifacts().Verify(ctx, a.SHA256)
	if e != nil {
		return intent, e
	}
	if verified.Size != a.Size {
		return intent, errors.New("material size differs from registered metadata")
	}
	after, e := os.Lstat(path)
	if e != nil {
		return intent, e
	}
	if !os.SameFile(st, after) || st.Size() != after.Size() || !st.ModTime().Equal(after.ModTime()) {
		return intent, errors.New("material changed during verification")
	}
	intent.Device = uint64(sys.Dev)
	intent.Inode = sys.Ino
	intent.Size = st.Size()
	intent.Modified = st.ModTime().UnixNano()
	intent.HadFile = true
	return intent, nil
}

// ExecuteCleanup requires the worker's global modification lock. Cancellation is
// checked before the destructive boundary; durable tombstones describe partial
// outcomes if a process or filesystem fails after that boundary.
func (s *Service) ExecuteCleanup(ctx context.Context, p domain.Plan) (CleanupResult, error) {
	if p.Operation == "instance.forget" || p.Operation == "instance.data.remove" {
		return s.executeInstanceCleanup(ctx, p, cleanupChecks{procRoot: "/proc"})
	}
	var in cleanupIntent
	result := CleanupResult{AuditRetained: true, PrivateDataPreserved: true}
	if p.Operation != "artifact.remove" {
		return result, domain.ErrInvalid
	}
	if e := decode(p.Input, &in); e != nil {
		return result, e
	}
	result.ID = in.ID
	if !catalog.ValidDigest(in.ID) || len(p.ArtifactDigests) != 1 || p.ArtifactDigests[0] != in.ID {
		return result, domain.ErrInvalid
	}
	a, e := get[catalog.Artifact](ctx, s.Store, "artifact", in.ID)
	if e != nil {
		return result, e
	}
	if a.SHA256 != in.ID {
		return result, errors.New("material record digest differs from identity")
	}
	path, e := s.Artifacts().Path(in.ID)
	if e != nil {
		return result, e
	}
	if a.Availability == "removed" {
		if _, e = os.Lstat(path); errors.Is(e, os.ErrNotExist) {
			result.State = "removed"
			return result, nil
		}
		return result, errors.New("removed material unexpectedly exists; explicit inspection required")
	}
	if e = s.checkCleanupReferences(ctx, in.ID, p.ID); e != nil {
		return result, e
	}
	observed, e := s.inspectCleanupMaterial(ctx, a)
	if e != nil {
		return result, e
	}
	if observed != in {
		return result, fmt.Errorf("%w: material identity changed since planning", domain.ErrConflict)
	}
	if e = ctx.Err(); e != nil {
		return result, e
	}
	durable := context.WithoutCancel(ctx)
	if e = s.Store.UpdateRecord(durable, "artifact", in.ID, func(r *domain.Record) error {
		var current catalog.Artifact
		if e := json.Unmarshal(r.Data, &current); e != nil {
			return e
		}
		if current.SHA256 != a.SHA256 || current.Path != a.Path || current.Size != a.Size || current.Availability != a.Availability {
			return domain.ErrConflict
		}
		current.Availability = "removal_pending"
		var e error
		r.Data, e = json.Marshal(current)
		return e
	}); e != nil {
		return result, e
	}
	result.State = "removal_pending"
	if in.HadFile {
		st, e := os.Lstat(path)
		if e != nil {
			return result, e
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok || !st.Mode().IsRegular() || uint64(sys.Dev) != in.Device || sys.Ino != in.Inode || st.Size() != in.Size || st.ModTime().UnixNano() != in.Modified {
			return result, errors.New("material changed at removal boundary")
		}
		if e = os.Remove(path); e != nil {
			return result, e
		}
		result.BytesRemoved = in.Size
	}
	dir, e := os.Open(s.Artifacts().Root)
	if e != nil {
		return result, e
	}
	e = dir.Sync()
	closeErr := dir.Close()
	if e != nil {
		return result, e
	}
	if closeErr != nil {
		return result, closeErr
	}
	if e = s.Store.UpdateRecord(durable, "artifact", in.ID, func(r *domain.Record) error {
		var current catalog.Artifact
		if e := json.Unmarshal(r.Data, &current); e != nil {
			return e
		}
		if current.Availability != "removal_pending" {
			return domain.ErrConflict
		}
		current.Availability = "removed"
		var e error
		r.Data, e = json.Marshal(current)
		return e
	}); e != nil {
		return result, e
	}
	result.State = "removed"
	return result, nil
}
