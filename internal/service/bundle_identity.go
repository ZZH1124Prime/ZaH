package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/ZZH1124Prime/ZaH/internal/catalog"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/migration"
)

// bundleIdentity binds a reviewed plan to all validated archive content. It must
// receive the result of migration.Inspect/Import, which verifies member bytes.
// Go's JSON encoder sorts map keys. UseNumber avoids changing integer identities
// through float64 rounding while normalizing metadata object key order.
func bundleIdentity(inspection migration.Inspection) (string, error) {
	canonical := func(raw json.RawMessage) (json.RawMessage, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if e := decoder.Decode(&value); e != nil {
			return nil, e
		}
		var extra any
		if e := decoder.Decode(&extra); e != io.EOF {
			return nil, fmt.Errorf("%w: trailing archive metadata", domain.ErrInvalid)
		}
		return json.Marshal(value)
	}
	for _, name := range []string{"manifest.json", "catalog.json", "bindings.json"} {
		if _, ok := inspection.Checksums[name]; !ok {
			return "", fmt.Errorf("%w: incomplete bundle inspection", domain.ErrInvalid)
		}
	}
	for _, sum := range inspection.Checksums {
		if !catalog.ValidDigest(sum.SHA256) || sum.Size < 0 {
			return "", fmt.Errorf("%w: invalid inspected checksum", domain.ErrInvalid)
		}
	}
	cat, e := canonical(inspection.Catalog)
	if e != nil {
		return "", e
	}
	bindings, e := canonical(inspection.Bindings)
	if e != nil {
		return "", e
	}
	identity := struct {
		Manifest  migration.Manifest            `json:"manifest"`
		Catalog   json.RawMessage               `json:"catalog"`
		Bindings  json.RawMessage               `json:"bindings"`
		Checksums map[string]migration.Checksum `json:"checksums"`
	}{inspection.Manifest, cat, bindings, inspection.Checksums}
	data, e := json.Marshal(identity)
	if e != nil {
		return "", e
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// verifyBundleIdentity rejects path replacement or archive changes after a plan
// was reviewed. An absent identity fails closed, including old unsafely loose plans.
func verifyBundleIdentity(inspection migration.Inspection, expected string) error {
	if !catalog.ValidDigest(expected) {
		return fmt.Errorf("%w: plan has no valid bundle identity", domain.ErrInvalid)
	}
	got, e := bundleIdentity(inspection)
	if e != nil {
		return e
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
		return fmt.Errorf("%w: archive content differs from the reviewed plan", domain.ErrConflict)
	}
	return nil
}

type bundleIdentityContextKey struct{}

// withBundleIdentity carries a saved plan's expected archive through nested
// restore helpers so the exact bytes staged by Import are checked again.
func withBundleIdentity(ctx context.Context, expected string) context.Context {
	return context.WithValue(ctx, bundleIdentityContextKey{}, expected)
}

func checkedBundleIdentity(ctx context.Context, inspection migration.Inspection) error {
	expected, ok := ctx.Value(bundleIdentityContextKey{}).(string)
	if !ok {
		return nil
	} // direct internal library calls have no reviewed-plan contract
	return verifyBundleIdentity(inspection, expected)
}
