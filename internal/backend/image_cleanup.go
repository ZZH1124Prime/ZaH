package backend

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// SnapshotRemovalPlan can only originate from a persisted export result. A raw
// image ID or load result is not ownership evidence. Services must hold all
// reference locks, reject live catalog references, and review this exact plan.
// Docker offers no conditional delete-if-still-untagged transaction: external
// daemon mutations must be excluded during execution, not merely ZaH jobs.
type SnapshotRemovalPlan struct {
	Proof                  SnapshotEvidence `json:"proof"`
	ExternalQuietConfirmed bool             `json:"external_quiet_confirmed"`
	State                  string           `json:"state"`
	Requirements           []string         `json:"requirements"`
}

func validOwnedSnapshot(p SnapshotEvidence) error {
	if !p.ImageCreated || p.ImageLoaded || !idPattern.MatchString(p.SourceInstanceID) || !digestPattern.MatchString(p.OwnershipToken) || !strings.HasPrefix(p.ImageID, "sha256:") || !digestPattern.MatchString(strings.TrimPrefix(p.ImageID, "sha256:")) {
		return errors.New("persisted locally-created snapshot ownership evidence required")
	}
	return nil
}

func (a Adapter) inspectDisposableSnapshot(ctx context.Context, p SnapshotEvidence) error {
	if err := validOwnedSnapshot(p); err != nil {
		return err
	}
	out, err := a.runner().Run(ctx, command("docker", "image", "inspect", p.ImageID))
	if err != nil {
		return err
	}
	if out.Truncated {
		return errors.New("snapshot image inspection exceeds limit")
	}
	var rows []struct {
		ID          string `json:"Id"`
		RepoTags    []string
		RepoDigests []string
		Config      *struct{ Labels map[string]string }
	}
	if json.Unmarshal([]byte(out.Stdout), &rows) != nil || len(rows) != 1 {
		return errors.New("invalid snapshot image inspection")
	}
	var fields []map[string]json.RawMessage
	if json.Unmarshal([]byte(out.Stdout), &fields) != nil || len(fields) != 1 {
		return errors.New("incomplete snapshot inspection")
	}
	if _, ok := fields[0]["RepoTags"]; !ok {
		return errors.New("snapshot tag evidence missing")
	}
	if _, ok := fields[0]["RepoDigests"]; !ok {
		return errors.New("snapshot digest reference evidence missing")
	}
	v := rows[0]
	if v.ID != p.ImageID || v.Config == nil || v.Config.Labels["org.zah.snapshot"] != p.OwnershipToken || v.Config.Labels["org.zah.snapshot.instance"] != p.SourceInstanceID {
		return errors.New("snapshot image ownership differs from persisted creation evidence")
	}
	if len(v.RepoTags) != 0 || len(v.RepoDigests) != 0 {
		return errors.New("snapshot image has named references; removal prohibited")
	}
	out, err = a.runner().Run(ctx, command("docker", "container", "ls", "--all", "--no-trunc", "--quiet", "--filter", "ancestor="+p.ImageID))
	if err != nil {
		return err
	}
	if out.Truncated || strings.TrimSpace(out.Stdout) != "" {
		return errors.New("snapshot image is referenced by containers or evidence is incomplete")
	}
	return nil
}

// PlanSnapshotImageRemoval is read-only. The caller resolves Proof from trusted
// local job evidence; imported bundle data must never supply local ownership.
func (a Adapter) PlanSnapshotImageRemoval(ctx context.Context, proof SnapshotEvidence) (SnapshotRemovalPlan, error) {
	if err := a.inspectDisposableSnapshot(ctx, proof); err != nil {
		return SnapshotRemovalPlan{}, err
	}
	return SnapshotRemovalPlan{Proof: proof, State: "review_required", Requirements: []string{"Verify no catalog, pending restore or active job references this exact image.", "Hold global and reference locks through execution.", "Exclude concurrent external Docker mutations; Docker has no atomic untagged-only delete precondition.", "Delete only the owned image ID without force and without pruning parent images."}}, nil
}

// RemoveSnapshotImage rechecks daemon ownership/references at execution, then
// makes exactly one non-force, no-prune removal. Any error remains unknown; it
// does not retry, interpret absence as success, or touch other runtime objects.
func (a Adapter) RemoveSnapshotImage(ctx context.Context, plan SnapshotRemovalPlan) (Evidence, error) {
	ev := Evidence{ResourceID: plan.Proof.ImageID, State: "unknown"}
	if !plan.ExternalQuietConfirmed {
		return ev, errors.New("explicit confirmation of no external Docker mutations is required; ZaH locks do not constrain external clients")
	}
	if plan.State != "review_required" {
		return ev, errors.New("reviewed snapshot removal plan required")
	}
	if err := a.inspectDisposableSnapshot(ctx, plan.Proof); err != nil {
		return ev, err
	}
	if err := a.inspectDisposableSnapshot(ctx, plan.Proof); err != nil {
		return ev, err
	}
	c := command("docker", "image", "rm", "--no-prune", plan.Proof.ImageID)
	c.SafeCancel = false
	c.Timeout = 0
	_, err := a.runner().Run(ctx, c)
	if err != nil {
		return ev, err
	}
	ev.State = "removed"
	return ev, nil
}
