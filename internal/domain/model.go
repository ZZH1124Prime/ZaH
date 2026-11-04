// Package domain defines ZaH's persistent identities and state transitions.
package domain

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNotFound   = errors.New("record not found")
	ErrConflict   = errors.New("conflicting operation")
	ErrReferenced = errors.New("resource still referenced")
	ErrInvalid    = errors.New("invalid input")
	// Only returned after proving this setup request has no saved instance and
	// before attempting any catalog transaction. Network errors are never this.
	ErrConfigurationRejected = errors.New("installation configuration rejected before saving")
)

// NewID returns a cryptographically random, path-safe identity.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// Record is the versioned storage envelope shared by catalog and migration.
type Record struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type AppFamily struct {
	StableID          string `json:"stable_id,omitempty"`
	Platform          string `json:"platform,omitempty"`
	ID                string `json:"id"`
	Name              string `json:"name"`
	DefaultInstanceID string `json:"default_instance_id,omitempty"`
}
type AppRelease struct {
	IconSHA256 string   `json:"icon_sha256,omitempty"`
	ID         string   `json:"id"`
	FamilyID   string   `json:"family_id"`
	Platform   string   `json:"platform"`
	Version    string   `json:"version"`
	Arch       string   `json:"arch"`
	Artifacts  []string `json:"artifacts"`
}
type Installation struct {
	HideWineTray  bool   `json:"hide_wine_tray,omitempty"`
	DPI           int    `json:"dpi,omitempty"`
	RequestSHA256 string `json:"request_sha256"`
	Format        string `json:"format"`
	Mode          string `json:"mode"`
	SourceEntry   string `json:"source_entry"`
	Stage         string `json:"stage"`
}
type ManagedDataRoot struct {
	Path   string `json:"path"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}
type AppInstance struct {
	RestoreRoot              *ManagedDataRoot `json:"restore_root,omitempty"`
	Installation             *Installation    `json:"installation,omitempty"`
	ID                       string           `json:"id"`
	ReleaseID                string           `json:"release_id"`
	EnvironmentID            string           `json:"environment_id"`
	Name                     string           `json:"name"`
	RecipeRevision           string           `json:"recipe_revision"`
	Entry                    []string         `json:"entry"`
	PrivateData              string           `json:"private_data"`
	State                    string           `json:"state"`
	OriginalArtifact         string           `json:"original_artifact"`
	ObservedInstalledVersion string           `json:"observed_installed_version"`
	Drift                    string           `json:"drift"`
	Favorite                 bool             `json:"favorite"`
	LastUsed                 *time.Time       `json:"last_used,omitempty"`
	ConflictGroups           []string         `json:"conflict_groups,omitempty"`
}
type Environment struct {
	LaunchLease      bool            `json:"launch_lease,omitempty"`
	LaunchToken      string          `json:"launch_token,omitempty"`
	StopRequested    bool            `json:"stop_requested,omitempty"`
	ID               string          `json:"id"`
	Backend          string          `json:"backend"`
	Name             string          `json:"name"`
	Ownership        string          `json:"ownership"`
	State            string          `json:"state"`
	Identity         string          `json:"identity"`
	Runtime          string          `json:"runtime"`
	CreationEvidence json.RawMessage `json:"creation_evidence,omitempty"`
	Registration     json.RawMessage `json:"registration,omitempty"`
	PendingRestore   json.RawMessage `json:"pending_restore,omitempty"`
}
type FileBinding struct {
	RestoredData string `json:"restored_data,omitempty"`
	ID           string `json:"id"`
	InstanceID   string `json:"instance_id"`
	HostPath     string `json:"host_path"`
	TargetPath   string `json:"target_path"`
	Access       string `json:"access"`
	State        string `json:"state"`
	Device       uint64 `json:"device"`
	Inode        uint64 `json:"inode"`
}
type CapabilityResult struct {
	ID         string    `json:"id"`
	InstanceID string    `json:"instance_id"`
	Capability string    `json:"capability"`
	State      string    `json:"state"`
	Host       string    `json:"host"`
	Runtime    string    `json:"runtime"`
	Version    string    `json:"version"`
	Evidence   string    `json:"evidence"`
	CheckedAt  time.Time `json:"checked_at"`
}

// ResourceRef protects resources from removal while a catalog object uses them.
type ResourceRef struct {
	FromKind string `json:"from_kind"`
	FromID   string `json:"from_id"`
	ToKind   string `json:"to_kind"`
	ToID     string `json:"to_id"`
	Role     string `json:"role"`
}

// Plan records immutable, reviewable changes before a worker starts.
type Plan struct {
	ID              string          `json:"id"`
	Operation       string          `json:"operation"`
	Input           json.RawMessage `json:"input,omitempty"`
	InstanceID      string          `json:"instance_id,omitempty"`
	EnvironmentIDs  []string        `json:"environment_ids,omitempty"`
	ArtifactDigests []string        `json:"artifact_digests,omitempty"`
	RecipeRevision  string          `json:"recipe_revision,omitempty"`
	Permissions     []string        `json:"permissions,omitempty"`
	DataImpact      []string        `json:"data_impact,omitempty"`
	Preconditions   []string        `json:"preconditions,omitempty"`
	Steps           []Step          `json:"steps"`
	CreatedAt       time.Time       `json:"created_at"`
}

type Step struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Description string          `json:"description"`
	Params      json.RawMessage `json:"params,omitempty"`
	SafeCancel  bool            `json:"safe_cancel"`
}
type JobState string

const (
	Queued          JobState = "queued"
	Running         JobState = "running"
	WaitingUser     JobState = "waiting_user"
	CancelRequested JobState = "cancel_requested"
	Succeeded       JobState = "succeeded"
	Failed          JobState = "failed"
	Cancelled       JobState = "cancelled"
	Interrupted     JobState = "interrupted"
)

// CanTransition rejects terminal-state changes and unsafe implicit retries.
func CanTransition(from, to JobState) bool {
	switch from {
	case Queued:
		return to == Running || to == Cancelled || to == CancelRequested || to == Interrupted
	case Running:
		return to == WaitingUser || to == CancelRequested || to == Succeeded || to == Failed || to == Interrupted
	case WaitingUser:
		return to == Running || to == CancelRequested || to == Failed || to == Interrupted || to == Cancelled || to == Succeeded
	case CancelRequested:
		return to == Cancelled || to == Failed || to == Interrupted || to == Succeeded
	case Interrupted:
		return to == Failed || to == Cancelled
	}
	return false
}
func ValidateTransition(from, to JobState) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("%w: job transition %s -> %s", ErrConflict, from, to)
	}
	return nil
}
func (s JobState) Terminal() bool { return s == Succeeded || s == Failed || s == Cancelled }

type Job struct {
	HostChangesConfirmed bool            `json:"host_changes_confirmed,omitempty"`
	ID                   string          `json:"id"`
	PlanID               string          `json:"plan_id"`
	IdempotencyKey       string          `json:"idempotency_key"`
	State                JobState        `json:"state"`
	StepIndex            int             `json:"step_index"`
	WorkerPID            int             `json:"worker_pid,omitempty"`
	WorkerStart          string          `json:"worker_start,omitempty"`
	Error                string          `json:"error,omitempty"`
	Result               json.RawMessage `json:"result,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}
