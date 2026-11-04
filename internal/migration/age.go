package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"filippo.io/age"
)

// IdentityFileSelection contains filesystem metadata, never private key bytes
// or a private-key digest. It pins the user's authorized file selection.
type IdentityFileSelection struct {
	Path             string `json:"path"`
	Device           uint64 `json:"device"`
	Inode            uint64 `json:"inode"`
	Size             int64  `json:"size"`
	ModifiedUnixNano int64  `json:"modified_unix_nano"`
}
type EncryptionLimits struct {
	MaxPlaintextBytes  int64
	MaxCiphertextBytes int64
	Bundle             Limits
}

func (l EncryptionLimits) defaults() EncryptionLimits {
	if l.MaxPlaintextBytes <= 0 {
		l.MaxPlaintextBytes = 64 << 30
	}
	if l.MaxCiphertextBytes <= 0 {
		l.MaxCiphertextBytes = (64 << 30) + (64 << 20)
	}
	l.Bundle = l.Bundle.defaults()
	return l
}

type EncryptionResult struct {
	Path   string `json:"path,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size"`
	State  string `json:"state"`
}
type EncryptOptions struct {
	Path        string
	Destination string
	SHA256      string
	Recipients  []string
}
type DecryptOptions struct {
	Path        string
	Destination string
	SHA256      string
	Identity    IdentityFileSelection
}

func encryptionPath(p string, missingLeaf bool) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" || len(p) > 4096 || strings.ContainsAny(p, "\x00\r\n") {
		return errors.New("normalized absolute encryption path required")
	}
	leaf := p
	for p != "/" {
		st, e := os.Lstat(p)
		if e != nil {
			if p == leaf && missingLeaf && os.IsNotExist(e) {
				p = filepath.Dir(p)
				continue
			}
			return errors.New("encryption path is inaccessible")
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return errors.New("encryption path must not traverse symlinks")
		}
		if p != leaf && !st.IsDir() {
			return errors.New("encryption path ancestor is not a directory")
		}
		p = filepath.Dir(p)
	}
	return nil
}
func identityMetadata(f *os.File, p string) (IdentityFileSelection, error) {
	st, e := f.Stat()
	if e != nil {
		return IdentityFileSelection{}, errors.New("cannot inspect identity file")
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || s.Uid != uint32(os.Geteuid()) || st.Size() < 1 || st.Size() > 64<<10 {
		return IdentityFileSelection{}, errors.New("identity must be an owner-only regular file of at most 64 KiB")
	}
	return IdentityFileSelection{Path: p, Device: uint64(s.Dev), Inode: s.Ino, Size: st.Size(), ModifiedUnixNano: st.ModTime().UnixNano()}, nil
}
func SelectIdentityFile(p string) (IdentityFileSelection, error) {
	if e := encryptionPath(p, false); e != nil {
		return IdentityFileSelection{}, e
	}
	f, e := regular(p)
	if e != nil {
		return IdentityFileSelection{}, errors.New("cannot open selected identity file")
	}
	defer f.Close()
	return identityMetadata(f, p)
}
func readSelectedIdentities(selection IdentityFileSelection) ([]age.Identity, error) {
	if e := encryptionPath(selection.Path, false); e != nil {
		return nil, errors.New("identity selection changed or is inaccessible")
	}
	f, e := regular(selection.Path)
	if e != nil {
		return nil, errors.New("cannot open selected identity file")
	}
	defer f.Close()
	before, e := identityMetadata(f, selection.Path)
	if e != nil || before != selection {
		return nil, errors.New("identity file differs from reviewed selection")
	}
	b, e := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if e != nil || len(b) > 64<<10 {
		return nil, errors.New("cannot read bounded identity file")
	}
	defer clear(b)
	after, e := identityMetadata(f, selection.Path)
	if e != nil || after != before || int64(len(b)) != before.Size {
		return nil, errors.New("identity file changed while reading")
	}
	var ids []age.Identity
	// Only plain X25519 identity lines are supported: never resolve plugins,
	// recipient files, SSH commands, passwords or encrypted key files implicitly.
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		if len(ids) >= 32 {
			return nil, errors.New("identity file has too many keys")
		}
		id, e := age.ParseX25519Identity(string(line))
		if e != nil {
			return nil, errors.New("identity file must contain only valid X25519 identities")
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, errors.New("identity file contains no X25519 identities")
	}
	return ids, nil
}
func ValidateRecipients(recipients []string) error {
	_, e := parseRecipients(recipients)
	return e
}
func parseRecipients(recipients []string) ([]age.Recipient, error) {
	if len(recipients) < 1 || len(recipients) > 32 {
		return nil, errors.New("select 1..32 X25519 recipients")
	}
	seen := map[string]bool{}
	out := make([]age.Recipient, 0, len(recipients))
	for _, s := range recipients {
		if len(s) > 128 || seen[s] {
			return nil, errors.New("invalid or duplicate age recipient")
		}
		r, e := age.ParseX25519Recipient(s)
		if e != nil {
			return nil, errors.New("valid X25519 public recipient required")
		}
		seen[s] = true
		out = append(out, r)
	}
	return out, nil
}
func validEnvelopeDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && s == strings.ToLower(s)
}

// HashEncryptionSource never hashes an identity file by design: the service
// calls this only for selected bundle/ciphertext input, which is public evidence.
func HashEncryptionSource(ctx context.Context, p string, max int64) (string, int64, error) {
	if max <= 0 {
		return "", 0, errors.New("positive encryption source size limit required")
	}
	if e := encryptionPath(p, false); e != nil {
		return "", 0, e
	}
	f, e := regular(p)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	before, e := f.Stat()
	if e != nil || before.Size() > max {
		return "", 0, errors.New("encryption source exceeds limit")
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(contextReader{ctx, f}, max+1))
	if e != nil {
		return "", 0, e
	}
	after, e := f.Stat()
	if e != nil || n > max || n != before.Size() || after.Size() != n || !before.ModTime().Equal(after.ModTime()) {
		return "", 0, errors.New("encryption source changed or exceeds limit")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
func envelopeStage(destination, suffix string) (string, error) {
	if !strings.HasSuffix(destination, suffix) {
		return "", errors.New("incorrect encryption destination suffix")
	}
	if e := encryptionPath(destination, true); e != nil {
		return "", e
	}
	if _, e := os.Lstat(destination); !os.IsNotExist(e) {
		return "", errors.New("encryption destination already exists or is inaccessible")
	}
	return os.MkdirTemp(filepath.Dir(destination), ".zah-crypto-")
}
func syncEnvelopeDirectory(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func publishEnvelope(ctx context.Context, staged, destination string) (EncryptionResult, error) {
	result := EncryptionResult{State: "not_published"}
	sum, n, e := HashEncryptionSource(ctx, staged, (64<<30)+(64<<20))
	if e != nil {
		return result, e
	}
	f, e := regular(staged)
	if e != nil {
		return result, e
	}
	e = f.Sync()
	closeErr := f.Close()
	if e != nil {
		return result, e
	}
	if closeErr != nil {
		return result, closeErr
	}
	if e = ctx.Err(); e != nil {
		return result, e
	}
	if e = os.Link(staged, destination); e != nil {
		return result, e
	}
	result = EncryptionResult{Path: destination, SHA256: sum, Size: n, State: "published_durability_unknown"}
	if e = syncEnvelopeDirectory(filepath.Dir(destination)); e != nil {
		return result, e
	}
	result.State = "published"
	return result, nil
}
func copyEnvelopeSource(ctx context.Context, p, dest, expected string, max int64) error {
	if !validEnvelopeDigest(expected) {
		return errors.New("reviewed source SHA256 required")
	}
	if e := encryptionPath(p, false); e != nil {
		return e
	}
	source, e := regular(p)
	if e != nil {
		return e
	}
	defer source.Close()
	out, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer out.Close()
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(out, h), io.LimitReader(contextReader{ctx, source}, max+1))
	if e != nil {
		return e
	}
	if n > max || hex.EncodeToString(h.Sum(nil)) != expected {
		return errors.New("encryption source changed or exceeds limit")
	}
	return out.Sync()
}

type cappedEnvelopeWriter struct {
	w         io.Writer
	remaining int64
}

func (w *cappedEnvelopeWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("encryption output exceeds limit")
	}
	n, e := w.w.Write(p)
	w.remaining -= int64(n)
	return n, e
}

func EncryptBundle(ctx context.Context, opts EncryptOptions, limits EncryptionLimits) (EncryptionResult, error) {
	limits = limits.defaults()
	empty := EncryptionResult{State: "not_published"}
	recipients, e := parseRecipients(opts.Recipients)
	if e != nil {
		return empty, e
	}
	if !strings.HasSuffix(opts.Path, ".zahbundle") {
		return empty, errors.New("plaintext source must be .zahbundle")
	}
	stage, e := envelopeStage(opts.Destination, ".zahbundle.age")
	if e != nil {
		return empty, e
	}
	defer os.RemoveAll(stage)
	plain := filepath.Join(stage, "source.zahbundle")
	if e = copyEnvelopeSource(ctx, opts.Path, plain, opts.SHA256, limits.MaxPlaintextBytes); e != nil {
		return empty, e
	}
	if _, e = Inspect(ctx, plain, limits.Bundle); e != nil {
		return empty, e
	}
	source, e := regular(plain)
	if e != nil {
		return empty, e
	}
	defer source.Close()
	encrypted := filepath.Join(stage, "encrypted")
	out, e := os.OpenFile(encrypted, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return empty, e
	}
	defer out.Close()
	writer, e := age.Encrypt(&cappedEnvelopeWriter{out, limits.MaxCiphertextBytes}, recipients...)
	if e != nil {
		return empty, errors.New("cannot initialize age encryption")
	}
	if _, e = io.Copy(writer, contextReader{ctx, source}); e != nil {
		return empty, e
	}
	if e = writer.Close(); e != nil {
		return empty, e
	}
	if e = out.Sync(); e != nil {
		return empty, e
	}
	if e = out.Close(); e != nil {
		return empty, e
	}
	return publishEnvelope(ctx, encrypted, opts.Destination)
}
func DecryptBundle(ctx context.Context, opts DecryptOptions, limits EncryptionLimits) (EncryptionResult, error) {
	limits = limits.defaults()
	empty := EncryptionResult{State: "not_published"}
	if !strings.HasSuffix(opts.Path, ".zahbundle.age") || !validEnvelopeDigest(opts.SHA256) {
		return empty, errors.New("encrypted source and reviewed SHA256 required")
	}
	if e := ctx.Err(); e != nil {
		return empty, e
	}
	ids, e := readSelectedIdentities(opts.Identity)
	if e != nil {
		return empty, e
	}
	stage, e := envelopeStage(opts.Destination, ".zahbundle")
	if e != nil {
		return empty, e
	}
	defer os.RemoveAll(stage)
	if e = encryptionPath(opts.Path, false); e != nil {
		return empty, e
	}
	source, e := regular(opts.Path)
	if e != nil {
		return empty, e
	}
	defer source.Close()
	h := sha256.New()
	cipherLimit := &io.LimitedReader{R: contextReader{ctx, source}, N: limits.MaxCiphertextBytes + 1}
	cipher := io.TeeReader(cipherLimit, h)
	reader, e := age.Decrypt(cipher, ids...)
	if e != nil {
		if ctx.Err() != nil {
			return empty, ctx.Err()
		}
		return empty, errors.New("age decryption failed; identity or ciphertext is invalid")
	}
	plain := filepath.Join(stage, "plaintext.zahbundle")
	out, e := os.OpenFile(plain, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return empty, e
	}
	defer out.Close()
	n, e := io.Copy(out, io.LimitReader(contextReader{ctx, reader}, limits.MaxPlaintextBytes+1))
	if e != nil {
		if ctx.Err() != nil {
			return empty, ctx.Err()
		}
		return empty, errors.New("age ciphertext authentication or reading failed")
	}
	if n > limits.MaxPlaintextBytes || cipherLimit.N == 0 {
		return empty, errors.New("decrypted bundle exceeds limit")
	}
	// Reading to EOF authenticates the final age chunk; no plaintext is published
	// if ciphertext differs from the exact reviewed bytes.
	if hex.EncodeToString(h.Sum(nil)) != opts.SHA256 {
		return empty, errors.New("encrypted source changed or exceeds limit")
	}
	if e = out.Sync(); e != nil {
		return empty, e
	}
	if e = out.Close(); e != nil {
		return empty, e
	}
	if _, e = Inspect(ctx, plain, limits.Bundle); e != nil {
		return empty, e
	}
	return publishEnvelope(ctx, plain, opts.Destination)
}
