package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/host"
	"github.com/ZZH1124Prime/ZaH/internal/process"
)

type windowsBindingRunner struct {
	cfg, programs, access string
	missing               bool
	calls                 []process.Command
	launches              int
}

func (f *windowsBindingRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.calls = append(f.calls, c)
	args := strings.Join(c.Args, "|")
	switch {
	case strings.Contains(args, "--json|list|bottles"):
		return process.Result{Stdout: f.cfg}, nil
	case strings.Contains(args, "--json|programs|"):
		return process.Result{Stdout: f.programs}, nil
	case strings.HasPrefix(args, "info|--file-access="):
		if f.missing {
			return process.Result{}, errors.New("command unavailable")
		}
		return process.Result{Stdout: f.access}, nil
	case strings.Contains(args, "run|-b|"):
		f.launches++
		return process.Result{}, nil
	default:
		return process.Result{}, errors.New("unexpected command")
	}
}
func mappedWindowsFixture(t *testing.T) (InstallRequest, ManualBottlesInput, *windowsBindingRunner, string) {
	t.Helper()
	r, in, cfg, programs := windowsRequest(t)
	dir := t.TempDir()
	binding, e := host.InspectBinding(dir, true)
	if e != nil {
		t.Fatal(e)
	}
	r.Bindings = []Binding{{Host: binding, Target: "D:"}}
	devices := filepath.Join(in.BottlePath, "dosdevices")
	if e = os.Mkdir(devices, 0700); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(devices, "d:")
	if e = os.Symlink(dir, link); e != nil {
		t.Fatal(e)
	}
	return r, in, &windowsBindingRunner{cfg: cfg, programs: programs, access: "read-only"}, link
}
func TestWindowsManualMappingsRegisterAndReverifyBeforeLaunch(t *testing.T) {
	for _, readOnly := range []bool{true, false} {
		t.Run(map[bool]string{true: "ro", false: "rw"}[readOnly], func(t *testing.T) {
			r, in, f, _ := mappedWindowsFixture(t)
			r.Bindings[0].Host.ReadOnly = readOnly
			if !readOnly {
				f.access = "read-write"
			}
			a := Adapter{f}
			plan, e := a.PlanInstall(r)
			if e != nil || len(f.calls) != 0 || plan.Steps[0].Mode != "manual" {
				t.Fatalf("%+v %v", plan, e)
			}
			proof, e := a.VerifyManualBottles(context.Background(), r, in)
			if e != nil {
				t.Fatal(e)
			}
			if proof.Windows == nil || len(proof.Windows.Bindings) != 1 || proof.Windows.Bindings[0].Drive != "D:" {
				t.Fatalf("%+v", proof)
			}
			r.Windows = proof.Windows
			if _, e = a.Launch(context.Background(), r, proof.ResourceID); e != nil || f.launches != 1 {
				t.Fatal("verified mapping not launchable", e)
			}
			checks := 0
			for _, c := range f.calls {
				if strings.HasPrefix(strings.Join(c.Args, "|"), "info|--file-access=") {
					checks++
				}
			}
			if checks != 2 {
				t.Fatal("mapping not rechecked before launch")
			}
		})
	}
}
func TestWindowsMappingsFailClosedAndNeverChangePermissions(t *testing.T) {
	for _, mode := range []string{"missing", "wrong_target", "hidden", "more_access", "less_access", "missing_command", "missing_drive", "directory", "escape_parent"} {
		t.Run(mode, func(t *testing.T) {
			r, in, f, link := mappedWindowsFixture(t)
			switch mode {
			case "missing":
				if e := os.Remove(link); e != nil {
					t.Fatal(e)
				}
			case "wrong_target":
				_ = os.Remove(link)
				if e := os.Symlink(t.TempDir(), link); e != nil {
					t.Fatal(e)
				}
			case "hidden":
				f.access = "hidden"
			case "more_access":
				f.access = "read-write"
			case "less_access":
				r.Bindings[0].Host.ReadOnly = false
			case "missing_command":
				f.missing = true
			case "missing_drive":
				r.Bindings[0].Target = "E:"
			case "directory":
				_ = os.Remove(link)
				if e := os.Mkdir(link, 0700); e != nil {
					t.Fatal(e)
				}
			case "escape_parent":
				_ = os.Remove(link)
				_ = os.Remove(filepath.Dir(link))
				if e := os.Symlink(t.TempDir(), filepath.Dir(link)); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := (Adapter{f}).VerifyManualBottles(context.Background(), r, in); e == nil {
				t.Fatal("accepted unverified binding")
			}
			if f.launches != 0 {
				t.Fatal("registration launched application")
			}
			for _, c := range f.calls {
				if len(c.Args) > 0 && (c.Args[0] == "override" || c.Args[0] == "permission-set") {
					t.Fatal("modified real permission")
				}
			}
		})
	}
}
func TestWindowsBindingDriftRequiresNewRegistration(t *testing.T) {
	r, in, f, link := mappedWindowsFixture(t)
	a := Adapter{f}
	proof, e := a.VerifyManualBottles(context.Background(), r, in)
	if e != nil {
		t.Fatal(e)
	}
	r.Windows = proof.Windows
	_ = os.Remove(link)
	if e = os.Symlink(t.TempDir(), link); e != nil {
		t.Fatal(e)
	}
	if _, e = a.Launch(context.Background(), r, proof.ResourceID); e == nil || f.launches != 0 {
		t.Fatal("launched after changed mapping")
	}
}
func TestWindowsTargetSyntaxCannotUseLinuxPathsOrReservedDrives(t *testing.T) {
	for _, target := range []string{"/mnt/zah/files", "C:", "Z:", "d:", "D:/subdir", "D:;bad"} {
		t.Run(target, func(t *testing.T) {
			r, _, f, _ := mappedWindowsFixture(t)
			r.Bindings[0].Target = target
			if _, e := (Adapter{f}).PlanInstall(r); e == nil || len(f.calls) != 0 {
				t.Fatal("accepted invalid target")
			}
		})
	}
}
