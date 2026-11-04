package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/process"
)

type preflightRunner struct {
	r                     InstallRequest
	root, mode, id, image string
	calls                 []process.Command
	executed              bool
}

func (f *preflightRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.calls = append(f.calls, c)
	args := strings.Join(c.Args, "|")
	if c.Program == "docker" {
		switch {
		case strings.HasPrefix(args, "context|inspect|"):
			host := "unix:///var/run/docker.sock"
			if f.mode == "remote" {
				host = "tcp://remote:2375"
			}
			b, _ := json.Marshal(map[string]string{"Host": host})
			return process.Result{Stdout: string(b)}, nil
		case strings.HasPrefix(args, "info|") && strings.Contains(args, ".DriverStatus"):
			if f.mode == "driver" {
				return process.Result{Stdout: `{"driver":"btrfs","status":[]}`}, nil
			}
			if f.mode == "containerd" {
				return process.Result{Stdout: `{"driver":"overlay2","status":[["driver-type","io.containerd.snapshotter.v1"]]}`}, nil
			}
			if f.mode == "missing_driver_status" {
				return process.Result{Stdout: `{"driver":"overlay2"}`}, nil
			}
			return process.Result{Stdout: `{"driver":"overlay2","status":[]}`}, nil
		case strings.HasPrefix(args, "info|"):
			b, _ := json.Marshal(f.root)
			return process.Result{Stdout: string(b)}, nil
		case strings.HasPrefix(args, "image|inspect|"):
			if f.mode == "missing_image" {
				return process.Result{}, errors.New("not found")
			}
			arch := "amd64"
			if f.mode == "arm" {
				arch = "arm64"
			}
			return process.Result{Stdout: fmt.Sprintf(`[{"Id":%q,"Os":"linux","Architecture":%q,"Size":1024}]`, f.image, arch)}, nil
		case strings.HasPrefix(args, "container|inspect|"):
			image := f.image
			if f.mode == "wrong_image" {
				image = "sha256:" + strings.Repeat("c", 64)
			}
			b, _ := json.Marshal([]any{map[string]any{"Id": f.id, "Image": image, "Name": "/zah-" + f.r.InstanceID, "Config": map[string]any{"Labels": map[string]string{"org.zah.instance": f.r.InstanceID}, "Env": []string{"HOME=" + f.r.PrivateHome}}, "Mounts": []any{map[string]any{"Type": "bind", "Source": f.r.PrivateHome, "Destination": f.r.PrivateHome, "RW": true}}, "State": map[string]any{"Running": f.mode == "deb_gui"}}})
			return process.Result{Stdout: string(b)}, nil
		}
	}
	if c.Program == "distrobox" && strings.HasSuffix(args, "id|-u") {
		uid := strconv.Itoa(os.Geteuid())
		if f.mode == "root" {
			uid = "0"
		}
		return process.Result{Stdout: uid}, nil
	}
	if f.mode == "deb_gui" && c.Program == "distrobox" {
		if strings.HasSuffix(args, "--|/usr/bin/true") || strings.HasSuffix(args, "/usr/bin/test|-x|/usr/bin/gdebi-gtk") {
			return process.Result{}, nil
		}
		if strings.Contains(args, "--|/usr/bin/sudo|-n|--|/usr/bin/gdebi-gtk|") {
			f.executed = true
			return process.Result{}, nil
		}
	}
	if c.Program == "distrobox" && strings.HasSuffix(args, "--appimage-extract") {
		f.executed = true
		return process.Result{}, nil
	}
	return process.Result{}, errors.New("unexpected command")
}
func preflightFixture(t *testing.T) (InstallRequest, *preflightRunner) {
	t.Helper()
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	r := request(t)
	f := &preflightRunner{r: r, root: t.TempDir(), id: strings.Repeat("a", 64), image: "sha256:" + strings.Repeat("b", 64)}
	return r, f
}
func TestInstallPreflightRecordsActualFilesystemsAndDoesNotWrite(t *testing.T) {
	r, f := preflightFixture(t)
	proof, e := (Adapter{f}).PreflightInstall(context.Background(), r)
	if e != nil || proof.State != "estimated_sufficient" || proof.Image == nil || len(proof.Capacity) != 3 {
		t.Fatalf("%+v %v", proof, e)
	}
	if _, e = os.Stat(r.PrivateHome); !os.IsNotExist(e) {
		t.Fatal("preflight created HOME")
	}
	for _, c := range proof.Capacity {
		if c.ObservedPath == "" || c.AvailableBytes == 0 || c.State != "estimated_sufficient" || c.FilesystemRequiredBytes < c.EstimatedAdditionalBytes {
			t.Fatalf("%+v", c)
		}
	}
	if len(f.calls) != 4 {
		t.Fatal("unexpected preparation effect", f.calls)
	}
}
func TestInstallPreflightRejectsUnknownRemoteImageAndMissingStorage(t *testing.T) {
	for _, mode := range []string{"remote", "missing_image", "arm", "storage", "driver", "containerd", "missing_driver_status"} {
		t.Run(mode, func(t *testing.T) {
			r, f := preflightFixture(t)
			f.mode = mode
			if mode == "storage" {
				f.root = filepath.Join(f.root, "missing-daemon-storage")
			}
			proof, e := (Adapter{f}).PreflightInstall(context.Background(), r)
			if !errors.Is(e, ErrManual) || proof.State != "unknown" {
				t.Fatalf("%+v %v", proof, e)
			}
			if _, e = os.Stat(r.PrivateHome); !os.IsNotExist(e) {
				t.Fatal("failed prerequisite changed disk")
			}
		})
	}
	checks := []CapacityCheck{{Device: 1, AvailableBytes: 100, EstimatedAdditionalBytes: 60}, {Device: 1, AvailableBytes: 100, EstimatedAdditionalBytes: 50}}
	if e := capacityTotals(checks); e == nil || checks[0].State != "insufficient" || checks[1].FilesystemRequiredBytes != 110 {
		t.Fatal("same-FS requirements were not aggregated")
	}
}
func TestInstallerDoesNotRunBeforeImageAndOrdinaryUserVerification(t *testing.T) {
	for _, mode := range []string{"valid", "wrong_image", "root", "arm"} {
		t.Run(mode, func(t *testing.T) {
			r, f := preflightFixture(t)
			f.mode = mode
			if _, e := prepare(context.Background(), r); e != nil {
				t.Fatal(e)
			}
			ev, e := (Adapter{f}).ExecuteStep(context.Background(), Step{Kind: "install", Request: r, ResourceID: f.id, Mode: "automatic"})
			if mode == "valid" {
				if e != nil || !f.executed || ev.State != "installed_unverified" {
					t.Fatalf("%+v %v", ev, e)
				}
			} else if e == nil || f.executed {
				t.Fatal("executed installer before checks", e)
			}
		})
	}
}

func TestInstallPreflightCancelledBeforeCommands(t *testing.T) {
	r, f := preflightFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := (Adapter{f}).PreflightInstall(ctx, r); !errors.Is(e, context.Canceled) || len(f.calls) != 0 {
		t.Fatal("cancellation lost or command ran", e)
	}
}

func TestDebInstallOpensNativeGUIInsteadOfInstallingSilently(t *testing.T) {
	r, f := preflightFixture(t)
	r.Recipe.Strategy = "deb"
	r.Recipe.Entry = "/usr/bin/app"
	r.Recipe.PendingEntry = true
	f.r = r
	f.mode = "deb_gui"
	if _, err := prepare(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	ev, err := (Adapter{f}).ExecuteStep(context.Background(), Step{Kind: "install", Request: r, ResourceID: f.id, Mode: "automatic"})
	if err != nil || !f.executed || ev.State != "maintenance_tool_exited" {
		t.Fatalf("%+v %v", ev, err)
	}
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c.Args, " "), "apt-get") {
			t.Fatal("target silently installed", c)
		}
	}
}
