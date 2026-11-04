package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

func TestEncryptionPlansKeepKeysOutAndRejectChangedSelection(t *testing.T) {
	s := catalogService(t)
	ctx := context.Background()
	dir := t.TempDir()
	plain := filepath.Join(dir, "catalog.zahbundle")
	if _, e := s.ExportCatalogBundle(ctx, plain); e != nil {
		t.Fatal(e)
	}
	id, e := age.GenerateX25519Identity()
	if e != nil {
		t.Fatal(e)
	}
	cipher := filepath.Join(dir, "encrypted.zahbundle.age")
	plan, e := s.PlanEncryptBundle(ctx, EncryptBundleInput{Path: plain, Destination: cipher, Recipients: []string{id.Recipient().String()}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ExecuteBundleEncryption(ctx, plan); e != nil {
		t.Fatal(e)
	}
	keypath := filepath.Join(dir, "key.txt")
	if e = os.WriteFile(keypath, []byte(id.String()), 0600); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(dir, "restored.zahbundle")
	decrypt, e := s.PlanDecryptBundle(ctx, DecryptBundleInput{Path: cipher, Destination: target, IdentityPath: keypath})
	if e != nil {
		t.Fatal(e)
	}
	encoded, e := json.Marshal(decrypt)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(encoded), id.String()) || strings.Contains(string(encoded), "AGE-SECRET-KEY-") || decrypt.Identity == nil {
		t.Fatal("plan stores key contents")
	}
	// A persisted JSON roundtrip includes only public evidence and key metadata.
	var saved BundleEncryptionPlan
	if e = json.Unmarshal(encoded, &saved); e != nil {
		t.Fatal(e)
	}
	result, e := s.ExecuteBundleEncryption(ctx, saved)
	if e != nil || result.State != "published" {
		t.Fatalf("%+v %v", result, e)
	}
	saved.Destination = filepath.Join(dir, "another.zahbundle")
	if e = os.Rename(keypath, keypath+".old"); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(keypath, []byte(id.String()), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ExecuteBundleEncryption(ctx, saved); e == nil {
		t.Fatal("replaced key file was silently accepted")
	}
}
