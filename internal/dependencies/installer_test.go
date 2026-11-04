package dependencies

import (
	"context"
	"errors"
	"github.com/ZZH1124Prime/ZaH/internal/process"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls      []process.Command
	simulation string
	exit       int
	err        error
}

func (f *fakeRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.calls = append(f.calls, c)
	if c.Program == "/usr/bin/apt-get" {
		return process.Result{Stdout: f.simulation}, nil
	}
	if c.Output != nil {
		c.Output("Setting up distrobox\n")
	}
	return process.Result{ExitCode: f.exit}, f.err
}
func fixture(r *fakeRunner) Installer {
	return Installer{Runner: r, ReadOS: func() ([]byte, error) { return []byte("ID=ubuntu\nVERSION_ID=\"24.04\"\n"), nil }, LookPath: func(string) (string, error) { return "", errors.New("missing") }, CheckExecutable: func(string) error { return nil }}
}
func TestPlanAndConfirmedExecution(t *testing.T) {
	r := &fakeRunner{simulation: "Inst distrobox (1.7.0 Ubuntu:24.04/noble [all])\nInst docker.io (26.1 Ubuntu:24.04/noble [amd64])\n"}
	i := fixture(r)
	p, e := i.Plan(context.Background(), "linux")
	if e != nil {
		t.Fatal(e)
	}
	if len(r.calls) != 1 || !strings.Contains(strings.Join(r.calls[0].Args, " "), "--simulate") {
		t.Fatal("planning executed installation")
	}
	if _, e = i.Execute(context.Background(), p, false, nil); e == nil || len(r.calls) != 1 {
		t.Fatal("unconfirmed execution")
	}
	logs := ""
	if _, e = i.Execute(context.Background(), p, true, func(s string) { logs += s }); e != nil {
		t.Fatal(e)
	}
	last := r.calls[len(r.calls)-1]
	if last.Program != "/usr/bin/pkexec" || last.SafeCancel || !strings.Contains(strings.Join(last.Args, " "), "docker.io=26.1") {
		t.Fatalf("unsafe command %+v", last)
	}
	if logs == "" {
		t.Fatal("no live output")
	}
}
func TestUnsupportedAndUnsafePlans(t *testing.T) {
	for _, out := range []string{"Remv existing [1]\n", "Inst existing [1] (2 Ubuntu [amd64])\n", "Inst bad (1;sh Ubuntu [amd64])\n"} {
		if _, e := parseSimulation(process.Result{Stdout: out}); e == nil {
			t.Fatal("accepted unsafe plan", out)
		}
	}
	r := &fakeRunner{}
	i := fixture(r)
	i.ReadOS = func() ([]byte, error) { return []byte("ID=unknown\nVERSION_ID=1"), nil }
	if _, e := i.Plan(context.Background(), "linux"); e == nil || len(r.calls) != 0 {
		t.Fatal("unsupported system executed command")
	}
}
func TestStalePlanCancelAndAuthorizationFailure(t *testing.T) {
	r := &fakeRunner{simulation: "Inst flatpak (1.14 Debian:12 [amd64])\n"}
	i := fixture(r)
	p, e := i.Plan(context.Background(), "windows")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = i.Execute(ctx, p, true, nil); !errors.Is(e, context.Canceled) || len(r.calls) != 1 {
		t.Fatal("cancel did not prevent execution")
	}
	r.simulation = "Inst flatpak (1.15 Debian:12 [amd64])\n"
	if _, e = i.Execute(context.Background(), p, true, nil); e == nil {
		t.Fatal("stale plan accepted")
	}
	for _, c := range r.calls {
		if c.Program == "/usr/bin/pkexec" {
			t.Fatal("elevated stale plan")
		}
	}
	r.simulation = "Inst flatpak (1.14 Debian:12 [amd64])\n"
	r.exit = 126
	r.err = errors.New("cancelled")
	if _, e = i.Execute(context.Background(), p, true, nil); e == nil || !strings.Contains(e.Error(), "取消") {
		t.Fatal("authorization cancelled reported success", e)
	}
}

func TestUbuntu2004LinuxPlanAndObsoleteFlatpakFeedback(t *testing.T) {
	r := &fakeRunner{simulation: "Inst docker.io (20.10 Ubuntu:20.04 [amd64])\n"}
	i := fixture(r)
	i.ReadOS = func() ([]byte, error) { return []byte("ID=ubuntu\nVERSION_ID=20.04"), nil }
	i.LookPath = func(n string) (string, error) {
		if n == "distrobox" {
			return "/fixture/distrobox", nil
		}
		return "", errors.New("missing")
	}
	if p, e := i.Plan(context.Background(), "linux"); e != nil || p.Distribution != "ubuntu 20.04" {
		t.Fatal(p, e)
	}
	r.simulation = "Inst flatpak (1.6.5-0ubuntu0.5 Ubuntu:20.04 [amd64])\n"
	if _, e := i.Plan(context.Background(), "windows"); e == nil || !strings.Contains(e.Error(), "Ubuntu 20.04") {
		t.Fatal("old host compatibility not explained", e)
	}
	for _, c := range r.calls {
		if c.Program != "/usr/bin/apt-get" {
			t.Fatal("unexpected mutation")
		}
	}
	for _, version := range []string{"1.6.5", "1.10.8", "unknown"} {
		if supportsBottleFlatpak(version) {
			t.Fatal(version)
		}
	}
	for _, version := range []string{"1.12.0-1", "1.14", "1:1.16.2-1", "2.0.0"} {
		if !supportsBottleFlatpak(version) {
			t.Fatal(version)
		}
	}
}
