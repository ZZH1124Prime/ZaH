package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/migration"
)

func TestBundleIdentityBindsEveryContentClass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.zahbundle")
	ctx := context.Background()
	if e := migration.Export(ctx, path, migration.Snapshot{Catalog: json.RawMessage(`{"records":[],"large_id":9007199254740993}`), Bindings: json.RawMessage(`{"state":"rebind"}`)}, nil, migration.Limits{}); e != nil {
		t.Fatal(e)
	}
	inspected, e := migration.Inspect(ctx, path, migration.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	identity, e := bundleIdentity(inspected)
	if e != nil {
		t.Fatal(e)
	}
	if e = verifyBundleIdentity(inspected, identity); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"manifest", "catalog", "bindings", "checksums"} {
		t.Run(name, func(t *testing.T) {
			b, _ := json.Marshal(inspected)
			var changed migration.Inspection
			if e := json.Unmarshal(b, &changed); e != nil {
				t.Fatal(e)
			}
			switch name {
			case "manifest":
				changed.Manifest.Requirements = append(changed.Manifest.Requirements, "new requirement")
			case "catalog":
				changed.Catalog = json.RawMessage(`{"records":[],"large_id":9007199254740992}`)
			case "bindings":
				changed.Bindings = json.RawMessage(`{"state":"different"}`)
			case "checksums":
				sum := changed.Checksums["catalog.json"]
				sum.Size++
				changed.Checksums["catalog.json"] = sum
			}
			if e := verifyBundleIdentity(changed, identity); !errors.Is(e, domain.ErrConflict) {
				t.Fatalf("changed %s accepted: %v", name, e)
			}
		})
	}
	if e = verifyBundleIdentity(inspected, ""); !errors.Is(e, domain.ErrInvalid) {
		t.Fatal("legacy unchecked plan accepted", e)
	}
	// Canonical metadata handling cannot lose integers above float64's exact range.
	reordered := inspected
	reordered.Catalog = json.RawMessage(`{"large_id":9007199254740993,"records":[]}`)
	if e = verifyBundleIdentity(reordered, identity); e != nil {
		t.Fatal("metadata order changed identity", e)
	}
}

func TestRestoreVerifiesActuallyStagedArchive(t *testing.T) {
	s := catalogService(t)
	ctx := context.Background()
	makeBundle := func(path, name string) migration.Inspection {
		snap := catalogSnapshot{Format: catalogBundleFormat, SchemaVersion: 1, Records: []domain.Record{record("family", "f", domain.AppFamily{ID: "f", Name: name})}}
		raw, e := json.Marshal(snap)
		if e != nil {
			t.Fatal(e)
		}
		if e = migration.Export(ctx, path, migration.Snapshot{Catalog: raw, Bindings: json.RawMessage(`{}`)}, nil, migration.Limits{}); e != nil {
			t.Fatal(e)
		}
		i, e := migration.Inspect(ctx, path, migration.Limits{})
		if e != nil {
			t.Fatal(e)
		}
		return i
	}
	first := makeBundle(filepath.Join(t.TempDir(), "first.zahbundle"), "reviewed")
	expected, e := bundleIdentity(first)
	if e != nil {
		t.Fatal(e)
	}
	replacement := filepath.Join(t.TempDir(), "replacement.zahbundle")
	makeBundle(replacement, "changed")
	if _, e = s.RestoreCatalogBundle(withBundleIdentity(ctx, expected), replacement); !errors.Is(e, domain.ErrConflict) {
		t.Fatalf("replacement archive accepted: %v", e)
	}
	rows, e := s.Store.List(ctx, "family")
	if e != nil || len(rows) != 0 {
		t.Fatalf("unreviewed records registered %+v %v", rows, e)
	}
}
