package service

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/process"
)

func rpcValue[T any](t *testing.T, s *Service, method string, in any) T {
	t.Helper()
	raw, _ := json.Marshal(in)
	v, e := s.Handle(context.Background(), method, raw)
	if e != nil {
		t.Fatalf("%s: %v", method, e)
	}
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	var out T
	if e = json.Unmarshal(b, &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func TestServiceArchivedMaterialAndImmutablePlan(t *testing.T) {
	s := catalogService(t)
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "安装材料.bin")
	if e := os.WriteFile(source, []byte("material"), 0600); e != nil {
		t.Fatal(e)
	}
	p := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "artifact.import", "input": map[string]string{"path": source}})
	if _, e := s.Execute(ctx, p); e != nil {
		t.Fatal(e)
	}
	rows, e := s.Store.List(ctx, "artifact")
	if e != nil || len(rows) != 1 {
		t.Fatalf("artifacts %v %v", rows, e)
	}
	family := rpcValue[domain.AppFamily](t, s, "family.create", map[string]string{"name": "测试应用"})
	release := rpcValue[domain.AppRelease](t, s, "release.create", map[string]any{"family_id": family.ID, "platform": "linux", "arch": "amd64", "version": "1", "artifacts": []string{rows[0].ID}})
	recipe := map[string]any{"schema_version": 1, "id": "app", "revision": "1", "platform": "linux", "lifecycle": "process-tree", "architecture": "amd64", "strategy": "appimage", "image": "docker.io/library/ubuntu@sha256:" + strings.Repeat("a", 64), "entry": "squashfs-root/AppRun"}
	r := rpcValue[struct {
		ID string `json:"id"`
	}](t, s, "recipe.add", recipe)
	instance := rpcValue[domain.AppInstance](t, s, "instance.create", map[string]string{"release_id": release.ID, "recipe_id": r.ID, "name": "日常"})
	plan := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.install", "input": map[string]string{"id": instance.ID}})
	if len(plan.Steps) != 4 {
		t.Fatalf("expected reviewed concrete steps, got %d", len(plan.Steps))
	}
	// A changed host binding must invalidate the reviewed request before executing
	// any external tool. No Docker/Distrobox command is run by this test.
	directory := t.TempDir()
	rpcValue[domain.FileBinding](t, s, "binding.register", map[string]string{"instance_id": instance.ID, "host_path": directory, "target_path": "/mnt/zah/files", "access": "read"})
	if _, e = s.Execute(ctx, plan); e == nil || !strings.Contains(e.Error(), "plan changed") {
		t.Fatalf("stale installation plan: %v", e)
	}
	raw, _ := json.Marshal(map[string]string{"kind": "family", "id": family.ID})
	if _, e = s.Handle(ctx, "catalog.delete", raw); e == nil {
		t.Fatal("deleted referenced family")
	}
	if e = os.Remove(source); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Artifacts().Verify(ctx, rows[0].ID); e != nil {
		t.Fatal(e)
	}
}
func TestMethodAndParameterValidation(t *testing.T) {
	s := catalogService(t)
	for _, tc := range []struct{ method, params string }{{"family.create", `{"name":"ok","shell":"bad"}`}, {"family.create", `{"name":""}`}, {"plan.create", `{"operation":"shell","input":{}}`}, {"catalog.delete", `{"kind":"artifact","id":"x"}`}, {"release.create", `{"family_id":"missing","platform":"mac","arch":"amd64","artifacts":[]}`}} {
		if _, e := s.Handle(context.Background(), tc.method, json.RawMessage(tc.params)); e == nil {
			t.Errorf("accepted %s %s", tc.method, tc.params)
		}
	}
}

type lifecycleRunner struct {
	storage  string
	instance string
	home     string
	onLaunch func()
	commands []process.Command
}

func (f *lifecycleRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.commands = append(f.commands, c)
	r := process.Result{ExitCode: 0}
	args := strings.Join(c.Args, " ")
	switch {
	case c.Program == "docker" && len(c.Args) > 0 && c.Args[0] == "context":
		r.Stdout = `{"Host":"unix:///var/run/docker.sock"}`
	case c.Program == "docker" && len(c.Args) > 0 && c.Args[0] == "info":
		if strings.Contains(args, "DriverStatus") {
			r.Stdout = `{"driver":"overlay2","status":[]}`
		} else {
			b, _ := json.Marshal(f.storage)
			r.Stdout = string(b)
		}
	case c.Program == "docker" && len(c.Args) > 1 && c.Args[0] == "image" && c.Args[1] == "inspect":
		r.Stdout = `[{"Id":"sha256:` + strings.Repeat("d", 64) + `","Os":"linux","Architecture":"amd64","Size":1024}]`
	case c.Program == "docker" && strings.Contains(args, "container ls"):
		r.Stdout = ""
	case c.Program == "docker" && strings.Contains(args, "--format {{.Id}}"):
		r.Stdout = strings.Repeat("b", 64)
	case c.Program == "docker" && len(c.Args) > 1 && c.Args[1] == "inspect":
		body, _ := json.Marshal([]any{map[string]any{"Id": strings.Repeat("b", 64), "Name": "/zah-" + f.instance, "Image": "sha256:" + strings.Repeat("d", 64), "Config": map[string]any{"Env": []string{"HOME=" + f.home}, "Labels": map[string]string{"org.zah.instance": f.instance}}, "Mounts": []any{map[string]any{"Type": "bind", "Source": f.home, "Destination": f.home, "RW": true}}, "State": map[string]any{"Running": false, "Status": "exited", "Paused": false, "Restarting": false, "Dead": false, "Pid": 0}}})
		r.Stdout = string(body)
	case c.Program == "distrobox" && strings.HasSuffix(args, "id -u"):
		r.Stdout = strconv.Itoa(os.Geteuid())
	case c.Program == "distrobox" && len(c.Args) > 0 && c.Args[0] == "enter" && !strings.Contains(args, " test ") && !strings.Contains(args, "--appimage-extract"):
		if f.onLaunch != nil {
			f.onLaunch()
		}
		for i, arg := range c.Args {
			if arg == "supervise" {
				r.Stdout = fmt.Sprintf(`{"protocol":"zah.supervise.v1","nonce":%q,"completed":true,"entry_exit_code":0,"reaped":1}`, c.Args[i+1])
			}
		}
	}
	return r, nil
}
func TestLinuxLifecycleWithMockBackend(t *testing.T) {
	s := catalogService(t)
	s.supervisorBinary = serviceSupervisorFixture(t)
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "installer.AppImage")
	if e := os.WriteFile(source, []byte("mock executable data"), 0600); e != nil {
		t.Fatal(e)
	}
	a, e := s.Artifacts().Import(ctx, source)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.save(ctx, "artifact", a.SHA256, a); e != nil {
		t.Fatal(e)
	}
	fam := rpcValue[domain.AppFamily](t, s, "family.create", map[string]string{"name": "应用"})
	rel := rpcValue[domain.AppRelease](t, s, "release.create", map[string]any{"family_id": fam.ID, "platform": "linux", "arch": "amd64", "version": "1", "artifacts": []string{a.SHA256}})
	r := rpcValue[struct {
		ID string `json:"id"`
	}](t, s, "recipe.add", map[string]any{"schema_version": 1, "id": "app", "revision": "1", "platform": "linux", "lifecycle": "process-tree", "architecture": "amd64", "strategy": "appimage", "image": "docker.io/library/ubuntu@sha256:" + strings.Repeat("a", 64), "entry": "squashfs-root/AppRun"})
	inst := rpcValue[domain.AppInstance](t, s, "instance.create", map[string]string{"release_id": rel.ID, "recipe_id": r.ID, "name": "旧名称"})
	fake := &lifecycleRunner{instance: inst.ID, home: inst.PrivateData, storage: t.TempDir()}
	s.Backend = backend.Adapter{Runner: fake}
	makePlan := func(op string) domain.Plan {
		return rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": op, "input": map[string]string{"id": inst.ID}})
	}
	if _, e = s.Execute(ctx, makePlan("instance.install")); e != nil {
		t.Fatal(e)
	}
	ready, e := get[domain.AppInstance](ctx, s.Store, "instance", inst.ID)
	if e != nil || ready.State != "ready" || len(ready.Entry) != 1 {
		t.Fatalf("not ready %+v %v", ready, e)
	}
	fake.onLaunch = func() {
		rpcValue[domain.AppInstance](t, s, "catalog.rename", map[string]string{"kind": "instance", "id": inst.ID, "name": "新名称"})
	}
	if _, e = s.Execute(ctx, makePlan("instance.launch")); e != nil {
		t.Fatal(e)
	}
	after, e := get[domain.AppInstance](ctx, s.Store, "instance", inst.ID)
	if e != nil || after.Name != "新名称" || after.LastUsed == nil {
		t.Fatalf("lost concurrent metadata %+v %v", after, e)
	}
	if _, e = s.Execute(ctx, makePlan("instance.remove")); e != nil {
		t.Fatal(e)
	}
	after, e = get[domain.AppInstance](ctx, s.Store, "instance", inst.ID)
	if e != nil || after.State != "archived" {
		t.Fatalf("remove %+v %v", after, e)
	}
	if _, e = s.Artifacts().Verify(ctx, a.SHA256); e != nil {
		t.Fatal("removal destroyed material", e)
	}
	if _, e = os.Stat(inst.PrivateData); e != nil {
		t.Fatal("removal destroyed home", e)
	}
	for _, c := range fake.commands {
		if c.Program != "docker" && c.Program != "distrobox" {
			t.Fatalf("unexpected backend program %s", c.Program)
		}
	}
}

// A worker can disappear while a detached application survives. Persist intent
// before calling upstream, and refuse another launch until explicitly reconciled.
type crashingLaunchRunner struct{}

func (crashingLaunchRunner) Run(context.Context, process.Command) (process.Result, error) {
	panic("simulated worker crash")
}
func TestLaunchCrashLeavesNonReplayableIntent(t *testing.T) {
	s, v, _, _ := instanceCleanupFixture(t)
	v.State = "ready"
	ctx := context.Background()
	env, _ := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	env.Identity = strings.Repeat("b", 64)
	if e := s.Store.Put(ctx, record("environment", env.ID, env)); e != nil {
		t.Fatal(e)
	}
	if e := s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
		t.Fatal(e)
	}
	s.supervisorBinary = serviceSupervisorFixture(t)
	configureSupervised(t, s, &v)
	p := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.launch", "input": map[string]string{"id": v.ID}})
	s.Backend = backend.Adapter{Runner: crashingLaunchRunner{}}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected simulated worker crash")
			}
		}()
		if _, err := s.Execute(ctx, p); err != nil {
			t.Logf("launch preparation: %v", err)
		}
	}()
	after, e := get[domain.AppInstance](ctx, s.Store, "instance", v.ID)
	if e != nil || after.State != "launching" {
		t.Fatalf("lost durable launch intent: %+v %v", after, e)
	}
	if _, e = s.Execute(ctx, p); e == nil {
		t.Fatal("replayed uncertain launch")
	}
}

type unavailablePreflightRunner struct{ calls int }

func (r *unavailablePreflightRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	r.calls++
	return process.Result{}, fmt.Errorf("isolated unavailable backend")
}
func TestInstallPreflightWaitsWithoutCreatingPartialInstallation(t *testing.T) {
	s, v := fullSource(t)
	ctx := context.Background()
	v.State = "unprepared"
	if e := s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
		t.Fatal(e)
	}
	p := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "instance.install", "input": map[string]string{"id": v.ID}})
	fake := &unavailablePreflightRunner{}
	s.Backend = backend.Adapter{Runner: fake}
	result, e := s.Execute(ctx, p)
	if !errors.Is(e, backend.ErrManual) || result == nil {
		t.Fatalf("expected manual prerequisite evidence: %+v %v", result, e)
	}
	after, e := get[domain.AppInstance](ctx, s.Store, "instance", v.ID)
	if e != nil || after.State != "unprepared" {
		t.Fatalf("preflight incorrectly began installation: %+v %v", after, e)
	}
	if _, e = os.Stat(filepath.Join(v.PrivateData, ".zah-install")); !os.IsNotExist(e) {
		t.Fatal("preflight created installation material", e)
	}
	if fake.calls != 1 {
		t.Fatal("preflight continued after unavailable daemon", fake.calls)
	}
}

// A nonexecuted static ELF fixture tests copy/digest gates with mocked backends.
func serviceSupervisorFixture(t *testing.T) string {
	t.Helper()
	header := make([]byte, 64)
	copy(header, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(header[16:], 2)
	binary.LittleEndian.PutUint16(header[18:], 62)
	binary.LittleEndian.PutUint32(header[20:], 1)
	binary.LittleEndian.PutUint16(header[52:], 64)
	p := filepath.Join(t.TempDir(), "supervisor")
	if e := os.WriteFile(p, header, 0700); e != nil {
		t.Fatal(e)
	}
	return p
}
