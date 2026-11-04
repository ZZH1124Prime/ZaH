package migration

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

func ageIdentity(t *testing.T) (*age.X25519Identity, IdentityFileSelection) {
	t.Helper()
	id, e := age.GenerateX25519Identity()
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "identity.txt")
	if e = os.WriteFile(path, []byte("# test identity\n"+id.String()+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	selection, e := SelectIdentityFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return id, selection
}
func envelopeDigest(t *testing.T, path string) string {
	t.Helper()
	sum, _, e := HashEncryptionSource(context.Background(), path, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	return sum
}
func encryptFixture(t *testing.T) (string, *age.X25519Identity, IdentityFileSelection, string) {
	t.Helper()
	dir, plain := makeBundle(t)
	id, selection := ageIdentity(t)
	encrypted := filepath.Join(dir, "encrypted.zahbundle.age")
	if _, e := EncryptBundle(context.Background(), EncryptOptions{Path: plain, Destination: encrypted, SHA256: envelopeDigest(t, plain), Recipients: []string{id.Recipient().String()}}, EncryptionLimits{}); e != nil {
		t.Fatal(e)
	}
	return encrypted, id, selection, plain
}
func noEnvelopeStaging(t *testing.T, dir string) {
	t.Helper()
	items, e := os.ReadDir(dir)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range items {
		if strings.HasPrefix(v.Name(), ".zah-crypto-") {
			t.Fatal("sensitive staging retained")
		}
	}
}
func TestAgeBundleRoundtripAndOfficialLibraryInteroperation(t *testing.T) {
	encrypted, id, selection, plain := encryptFixture(t)
	cipher, e := os.Open(encrypted)
	if e != nil {
		t.Fatal(e)
	}
	defer cipher.Close()
	decrypted, e := age.Decrypt(cipher, id)
	if e != nil {
		t.Fatal(e)
	}
	bytesPlain, e := io.ReadAll(decrypted)
	if e != nil {
		t.Fatal(e)
	}
	expected, e := os.ReadFile(plain)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(expected, bytesPlain) {
		t.Fatal("age interoperability lost plaintext")
	}
	target := filepath.Join(filepath.Dir(encrypted), "restored.zahbundle")
	result, e := DecryptBundle(context.Background(), DecryptOptions{Path: encrypted, Destination: target, SHA256: envelopeDigest(t, encrypted), Identity: selection}, EncryptionLimits{})
	if e != nil || result.State != "published" || result.SHA256 != envelopeDigest(t, plain) {
		t.Fatalf("%+v %v", result, e)
	}
	st, e := os.Stat(target)
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatal("plaintext not private", e)
	}
	if _, e = Inspect(context.Background(), target, Limits{}); e != nil {
		t.Fatal(e)
	}
	if _, e = DecryptBundle(context.Background(), DecryptOptions{Path: encrypted, Destination: target, SHA256: envelopeDigest(t, encrypted), Identity: selection}, EncryptionLimits{}); e == nil {
		t.Fatal("overwrote plaintext")
	}
	noEnvelopeStaging(t, filepath.Dir(encrypted))
}
func TestAgeRejectsWrongKeyTamperTruncationLimitsAndCancellation(t *testing.T) {
	encrypted, _, selection, _ := encryptFixture(t)
	original, e := os.ReadFile(encrypted)
	if e != nil {
		t.Fatal(e)
	}
	_, wrong := ageIdentity(t)
	for _, tc := range []struct {
		name        string
		body        []byte
		key         IdentityFileSelection
		limits      EncryptionLimits
		cancel      bool
		wrongDigest bool
	}{
		{name: "wrong-key", body: original, key: wrong},
		{name: "truncated", body: original[:len(original)-1], key: selection},
		{name: "tampered", body: append(append([]byte{}, original[:len(original)-1]...), original[len(original)-1]^1), key: selection},
		{name: "trailing", body: append(append([]byte{}, original...), []byte("trailer")...), key: selection},
		{name: "cipher-limit", body: original, key: selection, limits: EncryptionLimits{MaxCiphertextBytes: int64(len(original) - 1)}},
		{name: "plain-limit", body: original, key: selection, limits: EncryptionLimits{MaxPlaintextBytes: 10}},
		{name: "bundle-limit", body: original, key: selection, limits: EncryptionLimits{Bundle: Limits{MaxFiles: 1}}},
		{name: "cancel", body: original, key: selection, cancel: true},
		{name: "changed-input", body: original, key: selection, wrongDigest: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.zahbundle.age")
			if e := os.WriteFile(source, tc.body, 0600); e != nil {
				t.Fatal(e)
			}
			sum := envelopeDigest(t, source)
			if tc.wrongDigest {
				sum = strings.Repeat("a", 64)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			target := filepath.Join(dir, "result.zahbundle")
			if _, e := DecryptBundle(ctx, DecryptOptions{Path: source, Destination: target, SHA256: sum, Identity: tc.key}, tc.limits); e == nil {
				t.Fatal("accepted invalid ciphertext")
			} else if tc.cancel && !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
			if _, e := os.Stat(target); !os.IsNotExist(e) {
				t.Fatal("failed decrypt published plaintext")
			}
			noEnvelopeStaging(t, dir)
		})
	}
}
func TestAgeAuthenticCiphertextMustContainValidBundle(t *testing.T) {
	id, selection := ageIdentity(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "wrong.zahbundle.age")
	f, e := os.OpenFile(source, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	w, e := age.Encrypt(f, id.Recipient())
	if e != nil {
		t.Fatal(e)
	}
	_, _ = w.Write([]byte("valid encryption of invalid bundle"))
	if e = w.Close(); e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(dir, "output.zahbundle")
	if _, e = DecryptBundle(context.Background(), DecryptOptions{Path: source, Destination: target, SHA256: envelopeDigest(t, source), Identity: selection}, EncryptionLimits{}); e == nil {
		t.Fatal("published non-bundle")
	}
	if _, e = os.Stat(target); !os.IsNotExist(e) {
		t.Fatal("invalid plaintext published")
	}
	noEnvelopeStaging(t, dir)
}
func TestAgeIdentityFileSelectionAndErrorRedaction(t *testing.T) {
	encrypted, _, selection, _ := encryptFixture(t)
	target := filepath.Join(t.TempDir(), "output.zahbundle")
	if e := os.Rename(selection.Path, selection.Path+".original"); e != nil {
		t.Fatal(e)
	}
	invalid := "AGE-SECRET-KEY-PRIVATE-MUST-NEVER-APPEAR-IN-ERROR"
	if e := os.WriteFile(selection.Path, []byte(invalid), 0600); e != nil {
		t.Fatal(e)
	}
	opts := DecryptOptions{Path: encrypted, Destination: target, SHA256: envelopeDigest(t, encrypted), Identity: selection}
	if _, e := DecryptBundle(context.Background(), opts, EncryptionLimits{}); e == nil || strings.Contains(e.Error(), invalid) {
		t.Fatal("replacement or secret leak", e)
	}
	fresh, e := SelectIdentityFile(selection.Path)
	if e != nil {
		t.Fatal(e)
	}
	opts.Identity = fresh
	if _, e = DecryptBundle(context.Background(), opts, EncryptionLimits{}); e == nil || strings.Contains(e.Error(), invalid) {
		t.Fatal("malformed key or secret leak", e)
	}
	if e = os.Chmod(selection.Path, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = SelectIdentityFile(selection.Path); e == nil {
		t.Fatal("accepted public key-file permissions")
	}
	link := filepath.Join(t.TempDir(), "key-link")
	if e = os.Symlink(selection.Path+".original", link); e != nil {
		t.Fatal(e)
	}
	if _, e = SelectIdentityFile(link); e == nil {
		t.Fatal("accepted symlink identity")
	}
}
func TestAgeEncryptValidatesBundleRecipientLimitsAndSourceIdentity(t *testing.T) {
	dir, plain := makeBundle(t)
	id, _ := ageIdentity(t)
	sum := envelopeDigest(t, plain)
	for _, tc := range []struct {
		name       string
		recipients []string
		limits     EncryptionLimits
		sum        string
	}{
		{"badrecipient", []string{"AGE-SECRET-KEY-NOT-PUBLIC"}, EncryptionLimits{}, sum},
		{"duplicate", []string{id.Recipient().String(), id.Recipient().String()}, EncryptionLimits{}, sum},
		{"changed", []string{id.Recipient().String()}, EncryptionLimits{}, strings.Repeat("b", 64)},
		{"plainlimit", []string{id.Recipient().String()}, EncryptionLimits{MaxPlaintextBytes: 10}, sum},
		{"cipherlimit", []string{id.Recipient().String()}, EncryptionLimits{MaxCiphertextBytes: 10}, sum},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := filepath.Join(dir, tc.name+".zahbundle.age")
			if _, e := EncryptBundle(context.Background(), EncryptOptions{Path: plain, Destination: target, SHA256: tc.sum, Recipients: tc.recipients}, tc.limits); e == nil {
				t.Fatal("accepted invalid encryption request")
			}
			if _, e := os.Stat(target); !os.IsNotExist(e) {
				t.Fatal("failed encryption published")
			}
			noEnvelopeStaging(t, dir)
		})
	}
}
