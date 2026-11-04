package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/infra"
)

// supervisorCopy installs an independent, digest-verified copy of this exact
// executable. It is not a downloaded script or arbitrary recipe-provided helper.
func (s *Service) supervisorCopy(ctx context.Context, v domain.AppInstance) (string, error) {
	dataRoot, rootErr := s.instanceDataRoot(v)
	if rootErr != nil {
		return "", rootErr
	}
	if !safeInstanceID(v.ID) || v.PrivateData != filepath.Join(dataRoot, "instances", v.ID, "home") {
		return "", errors.New("supervisor requires the exact private instance HOME")
	}
	for _, p := range []string{dataRoot, filepath.Join(dataRoot, "instances"), filepath.Join(dataRoot, "instances", v.ID), v.PrivateData} {
		if err := infra.ValidatePrivateDir(p); err != nil {
			return "", err
		}
	}
	dir := filepath.Join(v.PrivateData, ".zah-runtime")
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if err := infra.ValidatePrivateDir(dir); err != nil {
		return "", err
	}
	exe := s.supervisorBinary
	var err error
	if exe == "" {
		exe, err = os.Executable()
	}
	if err != nil {
		return "", err
	}
	source, err := os.OpenFile(exe, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer source.Close()
	st, err := source.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() || st.Size() > 128<<20 {
		return "", errors.New("invalid supervisor source binary")
	}
	temp, err := os.CreateTemp(dir, "copy-")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(temp, h), source)
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	path := filepath.Join(dir, "zah-"+digest)
	if err = os.Chmod(tempPath, 0700); err != nil {
		return "", err
	}
	if err = os.Link(tempPath, path); err != nil && !os.IsExist(err) {
		return "", err
	}
	if err = os.Remove(tempPath); err != nil {
		return "", err
	}
	target, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer target.Close()
	info, err := target.Stat()
	if err != nil {
		return "", err
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0700 || sys.Nlink != 1 || sys.Uid != uint32(os.Geteuid()) {
		return "", errors.New("unsafe supervisor copy")
	}
	verify := sha256.New()
	if _, err = io.Copy(verify, target); err != nil {
		return "", err
	}
	if hex.EncodeToString(verify.Sum(nil)) != digest {
		return "", errors.New("supervisor copy digest mismatch")
	}
	directory, err := os.Open(dir)
	if err != nil {
		return "", err
	}
	defer directory.Close()
	if err = directory.Sync(); err != nil {
		return "", err
	}
	return path, nil
}
func startedProof(path, nonce string) bool {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 4096 || st.Mode().Perm() != 0600 {
		return false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys.Uid != uint32(os.Geteuid()) || sys.Nlink != 1 {
		return false
	}
	var proof struct {
		Protocol string `json:"protocol"`
		Nonce    string `json:"nonce"`
		PID      int    `json:"pid"`
	}
	d := json.NewDecoder(io.LimitReader(f, 4097))
	d.DisallowUnknownFields()
	return d.Decode(&proof) == nil && d.Decode(new(any)) == io.EOF && proof.Protocol == "zah.supervise.v1" && proof.Nonce == nonce && proof.PID > 0
}

type launchWatchResult struct {
	Stopped bool
	Err     error
}

func (s *Service) watchLaunch(ctx context.Context, v domain.AppInstance, r backend.InstallRequest, envID, identity, nonce, marker string) launchWatchResult {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	started := false
	var retryAt time.Time
	var controlErr error
	for {
		select {
		case <-ctx.Done():
			return launchWatchResult{Err: controlErr}
		case <-ticker.C:
		}
		if !started {
			started = startedProof(marker, nonce)
			if !started {
				continue
			}
		}
		gate, err := s.lifecycleGate(ctx, envID)
		if err != nil {
			return launchWatchResult{Err: err}
		}
		env, err := get[domain.Environment](ctx, s.Store, "environment", envID)
		if err != nil {
			gate.Close()
			return launchWatchResult{Err: err}
		}
		if env.Identity != identity || env.LaunchToken != nonce {
			gate.Close()
			return launchWatchResult{Err: errors.New("supervised launch identity changed")}
		}
		if env.StopRequested {
			if time.Now().Before(retryAt) {
				gate.Close()
				continue
			}
			env.State = "stopping"
			err = s.Store.Put(ctx, record("environment", env.ID, env))
			if err == nil {
				if r.Recipe.Platform == "windows" {
					// First drain this launch's owned subtree. Wine shutdown happens only
					// after the helper has proven ECHILD, so no pending launcher can restart it.
					err = publishLaunchStop(marker, nonce)
					gate.Close()
					if err != nil {
						// Keep ownership and the watcher alive after transient storage
						// failures. Individual Stop requests still have a bounded wait.
						controlErr = err
						retryAt = time.Now().Add(time.Second)
						continue
					}
					return launchWatchResult{}
				}
				_, err = s.Backend.Stop(context.WithoutCancel(ctx), r, identity)
			}
			gate.Close()
			if err != nil {
				// Keep the owned launch controllable after a failed Docker stop.
				// Each retry revalidates the full identity; occupancy stays held.
				controlErr = err
				retryAt = time.Now().Add(time.Second)
				continue
			}
			return launchWatchResult{Stopped: true}
		}
		if env.State != "running" {
			env.State = "running"
			err = s.Store.Put(ctx, record("environment", env.ID, env))
		}
		gate.Close()
		if err != nil {
			return launchWatchResult{Err: err}
		}
	}
}

// executeSupervisedLaunch owns the environment lock for the whole application
// lifetime. A valid helper proves all descendants ended; a launcher exit cannot.
func (s *Service) executeSupervisedLaunch(ctx context.Context, v domain.AppInstance, r backend.InstallRequest, env domain.Environment) (any, error) {
	if r.Recipe.Platform == "windows" {
		dataRoot, err := s.instanceDataRoot(v)
		if err != nil {
			return nil, err
		}
		expected := filepath.Join(dataRoot, "instances", v.ID, "home")
		if v.PrivateData != expected {
			return nil, errors.New("unexpected private launch control directory")
		}
		if err := infra.EnsurePrivateDir(expected); err != nil {
			return nil, err
		}
	}
	helper, err := s.supervisorCopy(ctx, v)
	if err != nil {
		return nil, err
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return nil, err
	}
	nonce := hex.EncodeToString(random)
	marker := filepath.Join(filepath.Dir(helper), "run-"+nonce+".started")
	gate, err := s.lifecycleGate(ctx, env.ID)
	if err != nil {
		return nil, err
	}
	env.LaunchToken = nonce
	env.StopRequested = false
	env.State = "launching"
	if v.Installation != nil && v.Installation.Stage == "installer_ready" {
		v.Installation.Stage = "awaiting_completion"
	}
	v.State = "launching"
	v.LastUsed = func() *time.Time { now := time.Now().UTC(); return &now }()
	err = s.Store.Put(ctx, record("environment", env.ID, env))
	if err == nil {
		err = s.persistInstanceState(ctx, v)
	}
	gate.Close()
	if err != nil {
		return nil, err
	}
	watchCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan launchWatchResult, 1)
	go func() { done <- s.watchLaunch(watchCtx, v, r, env.ID, env.Identity, nonce, marker) }()
	received := false
	defer func() {
		cancel()
		if !received {
			<-done
		}
	}()
	proof, launchErr := s.Backend.LaunchSupervised(ctx, r, env.Identity, helper, nonce)
	cancel()
	watch := <-done
	received = true
	if r.Recipe.Platform == "windows" && s.LaunchDiagnostics != nil {
		// The supervisor routes child stdout and stderr into Result.Stderr. Keep
		// diagnostics only in the private bounded worker log, not catalog/RPC data.
		_, _ = io.WriteString(s.LaunchDiagnostics, "Windows launch diagnostics (child stdout/stderr):\n")
		_, _ = io.WriteString(s.LaunchDiagnostics, proof.Result.Stderr)
		_, _ = io.WriteString(s.LaunchDiagnostics, "\n[end Windows launch diagnostics]\n")
	}
	proof.Result.Stdout = ""
	proof.Result.Stderr = ""
	finalCtx := context.WithoutCancel(ctx)
	gate, err = s.lifecycleGate(finalCtx, env.ID)
	if err != nil {
		return proof, err
	}
	defer gate.Close()
	current, err := get[domain.Environment](finalCtx, s.Store, "environment", env.ID)
	if err != nil {
		return proof, err
	}
	if current.Identity != env.Identity || current.LaunchToken != nonce {
		return proof, errors.New("launch identity changed before completion")
	}
	if r.Recipe.Platform == "linux" && v.Installation != nil && proof.State == "entry_unavailable" && errors.Is(launchErr, backend.ErrLinuxEntryChanged) {
		// The backend proved it rejected the entry before invoking the launcher.
		// The container can be running, but this attempt owns no application tree.
		current.State = "environment_prepared"
		current.LaunchLease = false
		current.LaunchToken = ""
		current.StopRequested = false
		v.State = "ready"
		err = s.invalidateLinuxInstalledEntry(finalCtx, v, r, current)
		return proof, errors.Join(launchErr, err)
	}
	if launchErr != nil && proof.State == "launch_not_started" {
		// No supervisor/application command was issued. Still verify the owned
		// environment stopped before releasing its lease.
		if _, stopErr := s.Backend.Stop(finalCtx, r, env.Identity); stopErr != nil {
			return proof, errors.Join(launchErr, stopErr)
		}
		current.State = "stopped"
		current.LaunchLease = false
		current.LaunchToken = ""
		current.StopRequested = false
		v.State = "ready"
		err = s.Store.Put(finalCtx, record("environment", current.ID, current))
		if err == nil {
			err = s.persistInstanceState(finalCtx, v)
		}
		return proof, errors.Join(launchErr, err)
	}
	completed := launchErr == nil && proof.State == "application_exited" && proof.Lifecycle != nil
	if completed && !watch.Stopped && !current.StopRequested && (r.Recipe.Platform == "windows" || r.Recipe.Lifecycle != "process-tree") {
		// All Linux launches use a startup handshake so explicit Stop is safe. A
		// default recipe still does not promise that its descendants cover the app.
		current.State = "launcher_exited_application_unknown"
		current.LaunchToken = ""
		v.State = "ready"
		err = s.Store.Put(finalCtx, record("environment", current.ID, current))
		if err == nil {
			err = s.persistInstanceState(finalCtx, v)
		}
		return proof, err
	}
	if completed || watch.Stopped {
		var stopped backend.Evidence
		if watch.Stopped {
			stopped, err = s.Backend.ConfirmStopped(finalCtx, r, env.Identity)
		} else {
			stopped, err = s.Backend.Stop(finalCtx, r, env.Identity)
		}
		if err == nil {
			current.State = "stopped"
			current.LaunchLease = false
			current.StopRequested = false
			current.LaunchToken = ""
			v.State = "ready"
			err = s.Store.Put(finalCtx, record("environment", current.ID, current))
			if err == nil {
				err = s.persistInstanceState(finalCtx, v)
			}
			return map[string]any{"application": proof, "environment": stopped, "requested_stop": watch.Stopped}, err
		}
		launchErr = err
	}
	if completed && current.StopRequested {
		// The owned launcher tree is proven drained, so a failed Wine stop can be
		// retried without allowing a late launch. Keep occupancy until that succeeds.
		current.State = "launcher_exited_application_unknown"
		current.LaunchToken = ""
		current.StopRequested = false
		v.State = "ready"
		_ = s.Store.Put(finalCtx, record("environment", current.ID, current))
		_ = s.persistInstanceState(finalCtx, v)
		return proof, launchErr
	}
	current.State = "needs_inspection"
	v.State = "needs_inspection"
	_ = s.Store.Put(finalCtx, record("environment", current.ID, current))
	_ = s.persistInstanceState(finalCtx, v)
	if launchErr == nil {
		launchErr = watch.Err
	}
	if launchErr == nil {
		launchErr = errors.New("application completion was not proven")
	}
	return proof, launchErr
}

// validLaunchStop accepts only our exact nonce and private, singly linked file.
// In particular, an unrelated existing file is never overwritten or removed.
func validLaunchStop(path, nonce string) bool {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() != 32 || st.Mode().Perm() != 0600 {
		return false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys.Uid != uint32(os.Geteuid()) || sys.Nlink != 1 {
		return false
	}
	b, err := io.ReadAll(io.LimitReader(f, 33))
	return err == nil && string(b) == nonce
}

func publishLaunchStop(marker, nonce string) error {
	raw, err := hex.DecodeString(nonce)
	if err != nil || len(raw) != 16 || hex.EncodeToString(raw) != nonce || filepath.Base(marker) != "run-"+nonce+".started" {
		return errors.New("invalid owned launcher stop request")
	}
	dir := filepath.Dir(marker)
	if err := infra.ValidatePrivateDir(dir); err != nil {
		return err
	}
	path := strings.TrimSuffix(marker, ".started") + ".stop"
	syncDir := func() error {
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer d.Close()
		return d.Sync()
	}
	if validLaunchStop(path, nonce) {
		return syncDir()
	}
	// Publish only complete content, leaving no partial destination after ENOSPC
	// or a short write. Cleanup is restricted to the temporary file we created.
	f, err := os.CreateTemp(dir, ".launch-stop-*")
	if err != nil {
		return errors.New("cannot prepare owned launcher stop request")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.WriteString(nonce); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Link(tmp, path); err != nil {
		if errors.Is(err, os.ErrExist) && validLaunchStop(path, nonce) {
			return syncDir()
		}
		return errors.New("cannot publish owned launcher stop request")
	}
	// The helper rejects multiple links: remove our staging link before success.
	if err = os.Remove(tmp); err != nil {
		return err
	}
	return syncDir()
}
