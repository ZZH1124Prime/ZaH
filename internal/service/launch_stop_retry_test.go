package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/process"
)

func TestPublishLaunchStopAtomicAndIdempotent(t *testing.T) {
	for _, mode := range []string{"new", "same", "different", "symlink", "hardlink", "short", "mode"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Dir(dir), 0700); err != nil {
				t.Fatal(err)
			}
			nonce := strings.Repeat("a", 32)
			marker := filepath.Join(dir, "run-"+nonce+".started")
			stop := strings.TrimSuffix(marker, ".started") + ".stop"
			target := filepath.Join(dir, "unrelated")
			if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "same":
				os.WriteFile(stop, []byte(nonce), 0600)
			case "different":
				os.WriteFile(stop, []byte(strings.Repeat("b", 32)), 0600)
			case "symlink":
				os.Symlink(target, stop)
			case "hardlink":
				os.Link(target, stop)
			case "short":
				os.WriteFile(stop, []byte("partial"), 0600)
			case "mode":
				os.WriteFile(stop, []byte(nonce), 0644)
			}
			err := publishLaunchStop(marker, nonce)
			if mode == "new" || mode == "same" {
				if err != nil || !validLaunchStop(stop, nonce) {
					t.Fatalf("%v", err)
				}
				if err = publishLaunchStop(marker, nonce); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("accepted conflicting stop file")
			}
			b, err := os.ReadFile(target)
			if err != nil || string(b) != "untouched" {
				t.Fatal("unrelated content changed")
			}
			leftovers, err := filepath.Glob(filepath.Join(dir, ".launch-stop-*"))
			if err != nil || len(leftovers) != 0 {
				t.Fatal("staging files leaked", leftovers, err)
			}
		})
	}
}

func TestWindowsStopPublisherFailureKeepsWatcherAndRetries(t *testing.T) {
	s, v, _ := windowsLifecycleService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	r, env, err := s.request(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	nonce := strings.Repeat("a", 32)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(dir), 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "run-"+nonce+".started")
	raw, _ := json.Marshal(process.SuperviseStarted{Protocol: process.SuperviseProtocol, Nonce: nonce, PID: 42})
	if err = os.WriteFile(marker, raw, 0600); err != nil {
		t.Fatal(err)
	}
	stop := strings.TrimSuffix(marker, ".started") + ".stop"
	if err = os.Mkdir(stop, 0700); err != nil {
		t.Fatal(err)
	}
	env.LaunchToken = nonce
	env.StopRequested = true
	env.LaunchLease = true
	if err = s.Store.Put(ctx, record("environment", env.ID, env)); err != nil {
		t.Fatal(err)
	}
	done := make(chan launchWatchResult, 1)
	go func() { done <- s.watchLaunch(ctx, v, r, env.ID, env.Identity, nonce, marker) }()
	// Wait for the watcher to encounter the obstructed destination.
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		current, e := get[domain.Environment](ctx, s.Store, "environment", env.ID)
		if e != nil {
			t.Fatal(e)
		}
		if current.State == "stopping" {
			break
		}
		select {
		case result := <-done:
			t.Fatalf("watcher abandoned control: %+v", result)
		case <-deadline.C:
			t.Fatal("watcher never attempted stop")
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case result := <-done:
		t.Fatalf("watcher abandoned control: %+v", result)
	case <-time.After(200 * time.Millisecond):
	}
	if err = os.Remove(stop); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.Err != nil || result.Stopped {
			t.Fatalf("unexpected result %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("watcher did not recover", ctx.Err())
	}
	if !validLaunchStop(stop, nonce) {
		t.Fatal("retry did not publish valid request")
	}
	current, err := get[domain.Environment](ctx, s.Store, "environment", env.ID)
	if err != nil || !current.LaunchLease || !current.StopRequested {
		t.Fatal("publisher cleared occupancy before launcher drain")
	}
}

// The real Docker 26 acceptance run exposed a stop CLI failure. A failed stop
// must not abandon the only watcher able to stop a still-running owned launch.
type transientLinuxStop struct {
	inner    *supervisedRunner
	attempts atomic.Int32
}

func (f *transientLinuxStop) Run(ctx context.Context, c process.Command) (process.Result, error) {
	if c.Program == "docker" && len(c.Args) > 1 && c.Args[1] == "stop" && f.attempts.Add(1) == 1 {
		return process.Result{}, errors.New("transient stop failure")
	}
	return f.inner.Run(ctx, c)
}
func TestLinuxStopFailureRetainsWatcherAndRetries(t *testing.T) {
	s, v, f := supervisedService(t)
	f.release = make(chan struct{})
	f.noProof = true
	retry := &transientLinuxStop{inner: f}
	s.Backend = backend.Adapter{Runner: retry}
	p := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.launch", "input": map[string]string{"id": v.ID}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ended := make(chan error, 1)
	go func() { _, e := s.Execute(ctx, p); ended <- e }()
	select {
	case <-f.entered:
	case <-ctx.Done():
		t.Fatal("did not enter")
	}
	stop := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.stop", "input": map[string]string{"id": v.ID}})
	if result, e := s.Execute(ctx, stop); e != nil {
		t.Fatal(result, e)
	}
	if e := <-ended; e != nil {
		t.Fatal(e)
	}
	env, _ := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	if retry.attempts.Load() != 2 || env.LaunchLease || env.StopRequested || env.State != "stopped" {
		t.Fatal("lost stop control", env, retry.attempts.Load())
	}
}
