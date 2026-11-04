package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/ZZH1124Prime/ZaH/internal/migration"
)

type EncryptBundleInput struct {
	Path        string   `json:"path"`
	Destination string   `json:"destination"`
	Recipients  []string `json:"recipients"`
}
type DecryptBundleInput struct {
	Path         string `json:"path"`
	Destination  string `json:"destination"`
	IdentityPath string `json:"identity_path"`
}

// BundleEncryptionPlan contains public encryption evidence and a selected key
// file identity only. The private key is opened only in the executing worker.
type BundleEncryptionPlan struct {
	Operation   string                           `json:"operation"`
	Path        string                           `json:"path"`
	Destination string                           `json:"destination"`
	SHA256      string                           `json:"sha256"`
	Size        int64                            `json:"size"`
	Recipients  []string                         `json:"recipients,omitempty"`
	Identity    *migration.IdentityFileSelection `json:"identity,omitempty"`
	Warnings    []string                         `json:"warnings"`
}

func encryptionDestination(path, suffix string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !strings.HasSuffix(path, suffix) || strings.ContainsAny(path, "\x00\r\n") {
		return errors.New("normalized absolute destination with correct bundle suffix required")
	}
	if _, e := os.Lstat(path); !os.IsNotExist(e) {
		return errors.New("destination exists or is inaccessible")
	}
	parent := filepath.Dir(path)
	for parent != "/" {
		st, e := os.Lstat(parent)
		if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("destination ancestors must be existing real directories")
		}
		parent = filepath.Dir(parent)
	}
	return nil
}
func (s *Service) PlanEncryptBundle(ctx context.Context, in EncryptBundleInput) (BundleEncryptionPlan, error) {
	var plan BundleEncryptionPlan
	if e := migration.ValidateRecipients(in.Recipients); e != nil {
		return plan, e
	}
	if !strings.HasSuffix(in.Path, ".zahbundle") {
		return plan, errors.New("plaintext source must be .zahbundle")
	}
	if e := encryptionDestination(in.Destination, ".zahbundle.age"); e != nil {
		return plan, e
	}
	sum, size, e := migration.HashEncryptionSource(ctx, in.Path, 64<<30)
	if e != nil {
		return plan, e
	}
	if _, e = migration.Inspect(ctx, in.Path, migration.Limits{}); e != nil {
		return plan, e
	}
	// The worker independently snapshots, hashes and validates the exact bytes.
	return BundleEncryptionPlan{Operation: "bundle.encrypt", Path: in.Path, Destination: in.Destination, SHA256: sum, Size: size, Recipients: append([]string(nil), in.Recipients...), Warnings: []string{"X25519 age encryption provides recipient confidentiality and ciphertext integrity, not sender identity or a source signature.", "The existing plaintext source is preserved; handle it separately if it contains sensitive data.", "The destination is published without replacement with owner-only permissions."}}, nil
}
func (s *Service) PlanDecryptBundle(ctx context.Context, in DecryptBundleInput) (BundleEncryptionPlan, error) {
	var plan BundleEncryptionPlan
	if !strings.HasSuffix(in.Path, ".zahbundle.age") || in.Path == in.IdentityPath {
		return plan, errors.New("encrypted source must be a separate .zahbundle.age file")
	}
	if e := encryptionDestination(in.Destination, ".zahbundle"); e != nil {
		return plan, e
	}
	identity, e := migration.SelectIdentityFile(in.IdentityPath)
	if e != nil {
		return plan, e
	}
	sum, size, e := migration.HashEncryptionSource(ctx, in.Path, (64<<30)+(64<<20))
	if e != nil {
		return plan, e
	}
	return BundleEncryptionPlan{Operation: "bundle.decrypt", Path: in.Path, Destination: in.Destination, SHA256: sum, Size: size, Identity: &identity, Warnings: []string{"The executing worker reads only the selected owner-only X25519 identity file; private key material is not persisted.", "Decryption authenticates ciphertext but does not authenticate its author or source.", "A plaintext bundle is published with owner-only permissions only after authentication and bundle inspection; it is not automatically imported."}}, nil
}
func (s *Service) ExecuteBundleEncryption(ctx context.Context, plan BundleEncryptionPlan) (migration.EncryptionResult, error) {
	switch plan.Operation {
	case "bundle.encrypt":
		if plan.Identity != nil {
			return migration.EncryptionResult{}, errors.New("encryption plan cannot include identity selection")
		}
		return migration.EncryptBundle(ctx, migration.EncryptOptions{Path: plan.Path, Destination: plan.Destination, SHA256: plan.SHA256, Recipients: plan.Recipients}, migration.EncryptionLimits{})
	case "bundle.decrypt":
		if plan.Identity == nil || len(plan.Recipients) != 0 {
			return migration.EncryptionResult{}, errors.New("decryption plan requires only selected identity metadata")
		}
		return migration.DecryptBundle(ctx, migration.DecryptOptions{Path: plan.Path, Destination: plan.Destination, SHA256: plan.SHA256, Identity: *plan.Identity}, migration.EncryptionLimits{})
	default:
		return migration.EncryptionResult{}, errors.New("unknown bundle encryption operation")
	}
}
