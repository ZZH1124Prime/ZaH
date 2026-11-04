package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/migration"
)

func TestEncryptionRPCPlansAndExecutionRoundTrip(t *testing.T) {
	s := catalogService(t)
	ctx := context.Background()
	dir := t.TempDir()
	original := filepath.Join(dir, "original.zahbundle")
	if _, e := s.ExportCatalogBundle(ctx, original); e != nil {
		t.Fatal(e)
	}
	identity, e := age.GenerateX25519Identity()
	if e != nil {
		t.Fatal(e)
	}
	key := filepath.Join(dir, "identity.txt")
	if e = os.WriteFile(key, []byte(identity.String()+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	encrypted := filepath.Join(dir, "encrypted.zahbundle.age")
	p := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "bundle.encrypt", "input": map[string]any{"path": original, "destination": encrypted, "recipients": []string{identity.Recipient().String()}}})
	if _, e = s.Execute(ctx, p); e != nil {
		t.Fatal(e)
	}
	decrypted := filepath.Join(dir, "decrypted.zahbundle")
	p = rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "bundle.decrypt", "input": map[string]string{"path": encrypted, "destination": decrypted, "identity_path": key}})
	if strings.Contains(string(p.Input), identity.String()) {
		t.Fatal("private key stored in plan")
	}
	if _, e = s.Execute(ctx, p); e != nil {
		t.Fatal(e)
	}
	if _, e = migration.Inspect(ctx, decrypted, migration.Limits{}); e != nil {
		t.Fatal(e)
	}
}
