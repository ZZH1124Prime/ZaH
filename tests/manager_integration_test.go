// Package tests exercises the real CLI/manager/worker process boundary using
// isolated XDG directories and ordinary installation material only. No external
// runtime, container, GUI application, or authorization tool is invoked.
package tests

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/process"
	"github.com/ZZH1124Prime/ZaH/internal/storage"
	"github.com/ZZH1124Prime/ZaH/internal/transport"
)

type managerProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func startManager(t *testing.T, binary string, env []string, logPath string) *managerProcess {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		r.Close()
		w.Close()
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "manager", "--ready-fd", "3")
	cmd.Env = env
	cmd.ExtraFiles = []*os.File{w}
	cmd.Stdout, cmd.Stderr = log, log
	if err = cmd.Start(); err != nil {
		r.Close()
		w.Close()
		log.Close()
		t.Fatal(err)
	}
	w.Close()
	log.Close()
	m := &managerProcess{cmd: cmd, done: make(chan struct{})}
	go func() { m.err = cmd.Wait(); close(m.done) }()
	t.Cleanup(func() { stopManager(t, m) })
	ready := make(chan error, 1)
	go func() {
		defer r.Close()
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 1024), 4096)
		if !scanner.Scan() {
			ready <- fmt.Errorf("manager did not report ready: %v", scanner.Err())
			return
		}
		var message struct {
			Ready    bool   `json:"ready"`
			Protocol int    `json:"protocol"`
			Socket   string `json:"socket"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			ready <- err
			return
		}
		if !message.Ready || message.Protocol != 1 || !filepath.IsAbs(message.Socket) {
			ready <- fmt.Errorf("invalid ready message: %s", scanner.Bytes())
			return
		}
		ready <- nil
	}()
	select {
	case err = <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		r.Close()
		t.Fatal("manager readiness timed out")
	}
	return m
}
func stopManager(t *testing.T, m *managerProcess) {
	t.Helper()
	select {
	case <-m.done:
		return
	default:
	}
	_ = m.cmd.Process.Kill()
	select {
	case <-m.done:
	case <-time.After(5 * time.Second):
		t.Error("test manager did not stop")
	}
}
func rpc(t *testing.T, socket, method string, params, result any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := transport.Call(ctx, socket, method, params, result); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
}
func isolatedEnvironment(root string) []string {
	replacements := map[string]string{"HOME": filepath.Join(root, "home"), "XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_DATA_HOME": filepath.Join(root, "data"), "XDG_STATE_HOME": filepath.Join(root, "state"), "XDG_CACHE_HOME": filepath.Join(root, "cache"), "XDG_RUNTIME_DIR": filepath.Join(root, "run")}
	env := make([]string, 0, len(os.Environ())+len(replacements))
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if _, replace := replacements[key]; !replace {
			env = append(env, v)
		}
	}
	for k, v := range replacements {
		env = append(env, k+"="+v)
	}
	return env
}
func ownedWorker(managerPID int, jobID string) (process.Identity, bool) {
	// Go may fork from any OS thread. Read only this test manager's task children.
	entries, _ := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", managerPID))
	for _, entry := range entries {
		b, err := os.ReadFile(entry)
		if err != nil {
			continue
		}
		for _, raw := range strings.Fields(string(b)) {
			pid, err := strconv.Atoi(raw)
			if err != nil {
				continue
			}
			args, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
			if err != nil {
				continue
			}
			parts := bytes.Split(args, []byte{0})
			if len(parts) >= 3 && string(parts[1]) == "worker" && string(parts[2]) == jobID {
				id, err := process.Capture(pid)
				if err == nil {
					return id, true
				}
			}
		}
	}
	return process.Identity{}, false
}
func waitJob(t *testing.T, s *storage.Store, id string) domain.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, err := s.Get(context.Background(), "job", id)
		if err != nil {
			t.Fatal(err)
		}
		var j domain.Job
		if err = json.Unmarshal(r.Data, &j); err != nil {
			t.Fatal(err)
		}
		if j.State.Terminal() || j.State == domain.Interrupted || j.State == domain.WaitingUser {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not finish: %+v", j)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestManagerWorkerLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("real binary lifecycle test includes the 30-second idle exit")
	}
	root, err := os.MkdirTemp("/tmp", "zah-manager-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	for _, name := range []string{"home", "config", "data", "state", "cache", "run"} {
		if err = os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(root, "zah")
	buildCtx, buildCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer buildCancel()
	build := exec.CommandContext(buildCtx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, "./cmd/zah")
	build.Dir = ".."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build test binary: %v\n%s", err, output)
	}
	env := isolatedEnvironment(root)
	socket := filepath.Join(root, "run", "zah", "manager.sock")
	manager := startManager(t, binary, env, filepath.Join(root, "manager-1.log"))
	var info struct {
		Protocol int `json:"protocol"`
	}
	rpc(t, socket, "system.info", struct{}{}, &info)
	if info.Protocol != 1 {
		t.Fatal("protocol mismatch")
	}
	// A second manager must fail without disrupting the first listener.
	duplicateCtx, duplicateCancel := context.WithTimeout(context.Background(), 3*time.Second)
	duplicate := exec.CommandContext(duplicateCtx, binary, "manager")
	duplicate.Env = env
	if _, err = duplicate.CombinedOutput(); err == nil {
		duplicateCancel()
		t.Fatal("second manager acquired same identity")
	}
	duplicateCancel()
	rpc(t, socket, "system.info", struct{}{}, &info)

	// Open a separate connection before submitting work. A short SQLite write
	// transaction delays worker initialization so manager-crash survival can be
	// checked deterministically without large files or slow external operations.
	store, err := storage.Open(filepath.Join(root, "data", "zah", "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	material := []byte("ZaH lifecycle test: archived installation material, not executable.\n")
	source := filepath.Join(root, "安装材料 with spaces.bin")
	if err = os.WriteFile(source, material, 0600); err != nil {
		t.Fatal(err)
	}
	var plan domain.Plan
	rpc(t, socket, "plan.create", map[string]any{"operation": "artifact.import", "input": map[string]string{"path": source}}, &plan)
	var job domain.Job
	rpc(t, socket, "job.submit", map[string]string{"plan_id": plan.ID, "idempotency_key": "one-user-click"}, &job)
	if _, err = store.DB.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			_, _ = store.DB.Exec("ROLLBACK")
		}
	}()
	var worker process.Identity
	t.Cleanup(func() {
		// Failure cleanup must not leave the isolated worker operating on a removed
		// test directory. Signal only the exact test-owned identity, never a name.
		identity := worker
		if identity.PID == 0 {
			if id, ok := ownedWorker(manager.cmd.Process.Pid, job.ID); ok {
				identity = id
			}
		}
		if process.Alive(identity) {
			if err := process.Signal(identity, syscall.SIGKILL); err != nil {
				t.Errorf("test worker cleanup: %v", err)
			}
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if id, ok := ownedWorker(manager.cmd.Process.Pid, job.ID); ok {
			worker = id
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("manager did not spawn worker while catalog writer was held")
		}
		time.Sleep(2 * time.Millisecond)
	}
	stopManager(t, manager)
	if !process.Alive(worker) {
		t.Fatal("worker exited with manager")
	}
	if _, err = store.DB.Exec("COMMIT"); err != nil {
		t.Fatal(err)
	}
	locked = false
	complete := waitJob(t, store, job.ID)
	if complete.State != domain.Succeeded {
		t.Fatalf("detached worker failed: %+v", complete)
	}
	sum := sha256.Sum256(material)
	digest := hex.EncodeToString(sum[:])
	archived := filepath.Join(root, "data", "zah", "artifacts", "sha256", digest)
	got, err := os.ReadFile(archived)
	if err != nil || !bytes.Equal(got, material) {
		t.Fatalf("archive content: %v", err)
	}
	if err = os.Remove(source); err != nil {
		t.Fatal(err)
	}
	// Reconnect through a new manager; the same durable request must resolve to
	// its previous task and preserved object even after the original source is gone.
	manager2 := startManager(t, binary, env, filepath.Join(root, "manager-2.log"))
	var duplicateJob domain.Job
	rpc(t, socket, "job.submit", map[string]string{"plan_id": plan.ID, "idempotency_key": "one-user-click"}, &duplicateJob)
	if duplicateJob.ID != job.ID || duplicateJob.State != domain.Succeeded {
		t.Fatalf("idempotent submission changed: %+v", duplicateJob)
	}
	var artifacts []domain.Record
	rpc(t, socket, "catalog.list", map[string]string{"kind": "artifact"}, &artifacts)
	if len(artifacts) != 1 || artifacts[0].ID != digest {
		t.Fatalf("archive catalog: %+v", artifacts)
	}
	var verified any
	rpc(t, socket, "artifact.verify", map[string]string{"sha256": digest}, &verified)
	var events []storage.Event
	rpc(t, socket, "events.read", map[string]any{"after": 0, "limit": 100}, &events)
	if len(events) == 0 {
		t.Fatal("no durable events")
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Fatal("events not monotonic")
		}
	}
	cursor := events[len(events)-1].Seq
	var later []storage.Event
	rpc(t, socket, "events.read", map[string]any{"after": cursor, "limit": 100}, &later)
	for _, event := range later {
		if event.Seq <= cursor {
			t.Fatal("event cursor replayed old data")
		}
	}
	// Last RPC disconnected. With no jobs, manager must exit by its actual idle
	// policy, without explicit shutdown from this test.
	select {
	case <-manager2.done:
		if manager2.err != nil {
			t.Fatalf("idle exit: %v", manager2.err)
		}
	case <-time.After(38 * time.Second):
		t.Fatal("idle manager failed to exit")
	}
}

// Reproduce the user's default HOME layout instead of only testing explicitly
// overridden XDG roots. The shared state parent must remain untouched.
func TestDefaultStateParentStartup(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "zah-default-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home := filepath.Join(root, "home")
	parent := filepath.Join(home, ".local", "state")
	if err = os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(parent, 0775); err != nil {
		t.Fatal(err)
	}
	run := filepath.Join(root, "run")
	if err = os.Mkdir(run, 0700); err != nil {
		t.Fatal(err)
	}
	env := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if key != "HOME" && !strings.HasPrefix(key, "XDG_") {
			env = append(env, value)
		}
	}
	env = append(env, "HOME="+home, "XDG_RUNTIME_DIR="+run)
	binary := filepath.Join(root, "zah")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, "./cmd/zah")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	manager := startManager(t, binary, env, filepath.Join(root, "manager.log"))
	var info struct {
		Protocol int `json:"protocol"`
	}
	rpc(t, filepath.Join(run, "zah", "manager.sock"), "system.info", struct{}{}, &info)
	if info.Protocol != 1 {
		t.Fatal("protocol mismatch")
	}
	fallback := filepath.Join(home, ".local", "share", "zah", "state", "zah")
	if st, err := os.Stat(fallback); err != nil || st.Mode().Perm() != 0700 {
		t.Fatalf("fallback: %v %v", st, err)
	}
	if st, err := os.Stat(parent); err != nil || st.Mode().Perm() != 0775 {
		t.Fatalf("shared state parent changed: %v %v", st, err)
	}
	if _, err := os.Lstat(filepath.Join(parent, "zah")); !os.IsNotExist(err) {
		t.Fatalf("default state unexpectedly created: %v", err)
	}
	stopManager(t, manager)
}
