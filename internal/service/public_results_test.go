package service

import (
	"encoding/json"
	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/migration"
	"strings"
	"testing"
)

func TestPublicBundleResultPreservesProofWithoutPrivateCatalogCopies(t *testing.T) {
	input := EnvironmentBundleResult{Linux: &backend.SnapshotEvidence{ImageCreated: true, ImageID: "retained-id", OwnershipToken: "retained-token"}, Inspection: migration.Inspection{Catalog: json.RawMessage(`{"format":"zah.instance-environment","private":"` + strings.Repeat("x", 2<<20) + `"}`), Checksums: map[string]migration.Checksum{"private-file": {SHA256: "a"}}}}
	output := compactExecutionResult(input).(EnvironmentBundleResult)
	raw, e := json.Marshal(output)
	if e != nil {
		t.Fatal(e)
	}
	if len(raw) > 4096 || output.Linux.OwnershipToken != "retained-token" || len(output.Inspection.Checksums) != 0 {
		t.Fatal("oversized result or lost ownership proof")
	}
	if input.Inspection.Checksums["private-file"].SHA256 != "a" {
		t.Fatal("mutated internal inspection")
	}
}
