package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/host"
	"github.com/ZZH1124Prime/ZaH/internal/process"
	"github.com/ZZH1124Prime/ZaH/internal/recipe"
)

type windowsLaunchRunner struct {
	mu                      sync.Mutex
	cfg, programs, control  string
	entered                 chan struct{}
	delayed                 <-chan struct{}
	drained                 bool
	stops                   int
	badCompletion, failStop bool
}

func (f *windowsLaunchRunner) Run(ctx context.Context, c process.Command) (process.Result, error) {
	if len(c.Args) > 0 && c.Args[0] == "supervise-controlled" {
		nonce, marker := c.Args[1], c.Args[2]
		close(f.entered)
		if f.delayed != nil {
			select {
			case <-f.delayed:
			case <-ctx.Done():
				return process.Result{}, ctx.Err()
			}
		}
		started, _ := json.Marshal(process.SuperviseStarted{Protocol: process.SuperviseProtocol, Nonce: nonce, PID: 42})
		if e := os.WriteFile(marker, started, 0600); e != nil {
			return process.Result{}, e
		}
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return process.Result{}, ctx.Err()
			case <-ticker.C:
			}
			b, e := os.ReadFile(strings.TrimSuffix(marker, ".started") + ".stop")
			if e == nil && string(b) == nonce {
				break
			}
		}
		f.mu.Lock()
		f.drained = true
		bad := f.badCompletion
		f.mu.Unlock()
		if bad {
			return process.Result{Stderr: "wine: fixture launch diagnostic\n"}, errors.New("launcher proof lost")
		}
		proof, _ := json.Marshal(process.SuperviseCompletion{Protocol: process.SuperviseProtocol, Nonce: nonce, Completed: true, EntryExitCode: 137, Reaped: 3})
		return process.Result{Stdout: string(proof), Stderr: "wine: fixture launch diagnostic\n"}, nil
	}
	joined := strings.Join(c.Args, "|")
	switch {
	case strings.Contains(joined, "--json|list|bottles"):
		return process.Result{Stdout: f.cfg}, nil
	case strings.Contains(joined, "--json|programs"):
		return process.Result{Stdout: f.programs}, nil
	case strings.Contains(joined, "info|bottles-path"):
		return process.Result{Stdout: "/test/bottles/bottles"}, nil
	case len(c.Args) > 6 && c.Args[1] == "--command=python3":
		if c.Args[6] == "stop" {
			f.mu.Lock()
			defer f.mu.Unlock()
			if !f.drained {
				return process.Result{}, errors.New("Wine stop raced undrained launcher")
			}
			if f.failStop {
				return process.Result{}, errors.New("Wine stop failed")
			}
			f.stops++
		}
		return process.Result{Stdout: f.control}, nil
	}
	return process.Result{}, errors.New("unexpected Windows mock command")
}
func windowsLifecycleService(t *testing.T) (*Service, domain.AppInstance, *windowsLaunchRunner) {
	t.Helper()
	s, v, _, _ := instanceCleanupFixture(t)
	ctx := context.Background()
	row, e := s.Store.Get(ctx, "recipe", v.RecipeRevision)
	if e != nil {
		t.Fatal(e)
	}
	var rec recipe.Recipe
	json.Unmarshal(row.Data, &rec)
	rec.Platform = "windows"
	rec.Strategy = "exe"
	rec.Image = ""
	rec.Runner = "soda-9.0-1"
	rec.Entry = "Player"
	rec.Lifecycle = ""
	rec.Dependencies = nil
	v.RecipeRevision = rec.Digest()
	v.State = "ready"
	s.Store.Put(ctx, record("recipe", v.RecipeRevision, rec))
	s.Store.Put(ctx, record("instance", v.ID, v))
	rel, _ := get[domain.AppRelease](ctx, s.Store, "release", v.ReleaseID)
	rel.Platform = "windows"
	s.Store.Put(ctx, record("release", rel.ID, rel))
	dir := filepath.Join(t.TempDir(), "bottle")
	entry := filepath.Join(dir, "drive_c", "app.exe")
	if e = os.MkdirAll(filepath.Dir(entry), 0700); e != nil {
		t.Fatal(e)
	}
	pe := make([]byte, 128)
	copy(pe, "MZ")
	binary.LittleEndian.PutUint32(pe[60:], 64)
	copy(pe[64:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(pe[68:], 0x8664)
	if e = os.WriteFile(entry, pe, 0600); e != nil {
		t.Fatal(e)
	}
	binding, e := host.InspectBinding(dir, true)
	if e != nil {
		t.Fatal(e)
	}
	cfg, _ := json.Marshal(map[string]any{"zah-" + v.ID: map[string]any{"Name": "zah-" + v.ID, "Path": dir, "Custom_Path": true, "Arch": "win64", "Runner": rec.Runner, "Creation_Date": "test", "Environment": "Application", "Parameters": map[string]any{"sandbox": false, "winebridge": false}}})
	programs, _ := json.Marshal([]any{map[string]any{"name": "Player", "path": `C:\app.exe`, "winebridge": false}})
	control, _ := json.Marshal(backend.WineControl{Server: "/test/bottles/runners/soda-9.0-1/bin/wineserver", SHA256: strings.Repeat("a", 64), WineSHA256: strings.Repeat("b", 64), Prefix: dir, Device: binding.Device, Inode: binding.Inode, UID: uint64(os.Getuid()), TmpDevice: 3, TmpInode: 4})
	f := &windowsLaunchRunner{cfg: string(cfg), programs: string(programs), control: string(control), entered: make(chan struct{})}
	s.Backend = backend.Adapter{Runner: f}
	s.supervisorBinary = serviceSupervisorFixture(t)
	r, env, e := s.request(ctx, v)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := s.Backend.VerifyManualBottles(ctx, r, backend.ManualBottlesInput{BottlePath: dir, ExecutablePath: entry, ProgramName: "Player"})
	if e != nil {
		t.Fatal(e)
	}
	if !proof.Windows.ControlledLaunch {
		t.Fatal("direct Wine control not registered")
	}
	env.Registration, _ = json.Marshal(proof.Windows)
	env.Backend = "bottles-flatpak"
	env.Identity = proof.ResourceID
	env.State = "registered_running_state_unknown"
	if e = s.Store.Put(ctx, record("environment", env.ID, env)); e != nil {
		t.Fatal(e)
	}
	return s, v, f
}
func TestBlockingWindowsLaunchStopsOnlyAfterLauncherDrain(t *testing.T) {
	for _, mode := range []string{"blocking", "restored_root", "delayed", "stop_all_queued", "lost_completion", "stop_failure_retry"} {
		t.Run(mode, func(t *testing.T) {
			s, v, f := windowsLifecycleService(t)
			var diagnostic bytes.Buffer
			s.LaunchDiagnostics = &diagnostic
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			if mode == "restored_root" {
				parent := t.TempDir()
				if e := os.Chmod(parent, 0700); e != nil {
					t.Fatal(e)
				}
				root := filepath.Join(parent, "zah-import-"+domain.NewID())
				v.PrivateData = filepath.Join(root, "instances", v.ID, "home")
				if e := os.MkdirAll(v.PrivateData, 0700); e != nil {
					t.Fatal(e)
				}
				var e error
				v.RestoreRoot, e = inspectRestoreParent(root)
				if e != nil {
					t.Fatal(e)
				}
				if e = s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
					t.Fatal(e)
				}
			}
			delayed := make(chan struct{})
			if mode == "delayed" {
				f.delayed = delayed
			}
			if mode == "lost_completion" {
				f.badCompletion = true
			}
			if mode == "stop_failure_retry" {
				f.failStop = true
			}
			p := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.launch", "input": map[string]string{"id": v.ID}})
			var queued domain.Job
			if mode == "stop_all_queued" {
				var e error
				queued, _, e = s.Store.SubmitJob(ctx, p.ID, "queued-windows")
				if e != nil {
					t.Fatal(e)
				}
			}
			ended := make(chan error, 1)
			go func() {
				result, e := s.Execute(ctx, p)
				raw, _ := json.Marshal(result)
				if strings.Contains(string(raw), "fixture launch diagnostic") {
					e = errors.New("private diagnostic leaked into catalog result")
				}
				ended <- e
			}()
			select {
			case <-f.entered:
			case <-ctx.Done():
				t.Fatal("launcher did not start")
			}
			if mode != "delayed" {
				deadline := time.Now().Add(2 * time.Second)
				for {
					env, _ := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
					if env.State == "running" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("blocking launcher never became stoppable")
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			operation := "instance.stop"
			var input any = map[string]string{"id": v.ID}
			if mode == "stop_all_queued" {
				operation = "applications.stopAll"
				input = map[string]bool{"confirmed": true}
			}
			stop := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": operation, "input": input})
			stopped := make(chan error, 1)
			go func() { _, e := s.Execute(ctx, stop); stopped <- e }()
			if mode == "delayed" {
				deadline := time.Now().Add(2 * time.Second)
				for {
					env, _ := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
					if env.StopRequested {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("stop request not recorded")
					}
					time.Sleep(10 * time.Millisecond)
				}
				f.mu.Lock()
				count := f.stops
				f.mu.Unlock()
				if count != 0 {
					t.Fatal("stopped before delayed launch was controlled")
				}
				close(delayed)
			}
			stopErr := <-stopped
			launchErr := <-ended
			if !strings.Contains(diagnostic.String(), "wine: fixture launch diagnostic") {
				t.Fatal("launch diagnostic lost on completion or failure")
			}
			if mode == "lost_completion" || mode == "stop_failure_retry" {
				if stopErr == nil || launchErr == nil {
					t.Fatal("uncertain shutdown reported success")
				}
			} else if stopErr != nil || launchErr != nil {
				t.Fatalf("stop %v launch %v", stopErr, launchErr)
			}
			if mode == "stop_failure_retry" {
				f.mu.Lock()
				f.failStop = false
				f.mu.Unlock()
				retry := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.stop", "input": map[string]string{"id": v.ID}})
				if _, e := s.Execute(ctx, retry); e != nil {
					t.Fatal("safe drained-launch retry failed", e)
				}
			}
			env, _ := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
			f.mu.Lock()
			count := f.stops
			f.mu.Unlock()
			if mode == "lost_completion" {
				if count != 0 || !env.LaunchLease {
					t.Fatal("lost proof cleared occupancy")
				}
			} else if count != 1 || env.LaunchLease || env.State != "stopped" {
				t.Fatalf("incorrect final state %+v stops=%d", env, count)
			}
			if mode == "stop_all_queued" {
				j, _ := get[domain.Job](ctx, s.Store, "job", queued.ID)
				if j.State != domain.Cancelled {
					t.Fatal("queued start survived stop-all")
				}
			}
		})
	}
}
