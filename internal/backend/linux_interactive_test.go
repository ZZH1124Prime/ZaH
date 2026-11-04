package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/process"
)

type linuxFixtureRunner struct {
	calls   []process.Command
	replies []process.Result
	failAt  int
}

func (f *linuxFixtureRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.calls = append(f.calls, c)
	i := len(f.calls) - 1
	if f.failAt > 0 && len(f.calls) == f.failAt {
		return process.Result{ExitCode: 1}, errors.New("fixture unavailable")
	}
	if i >= len(f.replies) {
		return process.Result{}, errors.New("unexpected command")
	}
	return f.replies[i], nil
}
func linuxFixture(t *testing.T) (InstallRequest, string, []process.Result) {
	t.Helper()
	r := request(t)
	if err := os.Mkdir(r.PrivateHome, 0700); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("b", 64)
	rows := []map[string]any{{"Id": id, "Name": "/zah-" + r.InstanceID, "State": map[string]bool{"Running": true}, "Config": map[string]any{"Labels": map[string]string{"org.zah.instance": r.InstanceID}, "Env": []string{"HOME=" + r.PrivateHome}}, "Mounts": []map[string]any{{"Type": "bind", "Source": r.PrivateHome, "Destination": r.PrivateHome, "RW": true}}}}
	b, _ := json.Marshal(rows)
	return r, id, []process.Result{{Stdout: string(b)}, {Stdout: string(b)}, {Stdout: strconv.Itoa(os.Getuid())}}
}
func TestLinuxInventoryHelperPreservesDesktopAndSkipsOutsideLinks(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 unavailable")
	}
	root := t.TempDir()
	desktop := []byte("[Desktop Entry]\nType=Application\nName=Original Player\nExec=player --profile \"work here\" %U\nPath=/tmp\nIcon=player\n")
	path := filepath.Join(root, "player.desktop")
	if err = os.WriteFile(path, desktop, 0600); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "hidden.desktop"), []byte("[Desktop Entry]\nType=Application\nName=Hidden\nExec=hidden\nNoDisplay=true\n"), 0600)
	outside := filepath.Join(t.TempDir(), "outside.desktop")
	os.WriteFile(outside, desktop, 0600)
	if err = os.Symlink(outside, filepath.Join(root, "outside.desktop")); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"roots": []string{root}, "appimage": ""})
	out, err := exec.Command(python, "-I", "-c", linuxInventoryScript, string(cfg)).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	var rows []LinuxProgramCandidate
	if err = json.Unmarshal(out, &rows); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(desktop)
	if len(rows) != 1 || rows[0].Name != "Original Player" || rows[0].Kind != "desktop" || rows[0].Path != path || rows[0].CWD != "/tmp" || rows[0].SHA256 != hex.EncodeToString(sum[:]) || len(rows[0].Args) != 0 {
		t.Fatalf("%+v", rows)
	}
	current, _ := os.ReadFile(path)
	if string(current) != string(desktop) {
		t.Fatal("rewrote desktop file")
	}
}
func TestLinuxDiscoveryAndChangedEntry(t *testing.T) {
	for _, mode := range []string{"valid", "changed", "missing", "outside"} {
		t.Run(mode, func(t *testing.T) {
			r, id, replies := linuxFixture(t)
			selected := LinuxProgramCandidate{Name: "Player", Kind: "desktop", Path: "/usr/share/applications/player.desktop", SHA256: strings.Repeat("a", 64)}
			candidate := selected
			if mode == "changed" {
				candidate.SHA256 = strings.Repeat("c", 64)
			}
			if mode == "outside" {
				candidate.Path = "/run/host/usr/share/applications/player.desktop"
			}
			rows := []LinuxProgramCandidate{candidate}
			if mode == "missing" {
				rows = nil
			}
			b, _ := json.Marshal(rows)
			f := &linuxFixtureRunner{replies: append(replies, process.Result{Stdout: string(b)})}
			got, err := (Adapter{f}).VerifyLinuxEntry(context.Background(), r, id, selected)
			if mode == "valid" {
				if err != nil || got.Path != selected.Path {
					t.Fatalf("%+v %v", got, err)
				}
			} else if err == nil {
				t.Fatal("accepted changed/missing/outside entry")
			}
			if len(f.calls) != 4 || f.calls[3].Program != "distrobox" || !strings.Contains(strings.Join(f.calls[3].Args, " "), "/usr/bin/python3 -I -c") {
				t.Fatalf("%+v", f.calls)
			}
		})
	}
}
func TestLinuxStoppedOrForeignContainerDoesNotEnter(t *testing.T) {
	for _, mode := range []string{"stopped", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			r, id, replies := linuxFixture(t)
			if mode == "stopped" {
				replies[0].Stdout = strings.ReplaceAll(replies[0].Stdout, `"Running":true`, `"Running":false`)
			} else {
				replies[0].Stdout = strings.ReplaceAll(replies[0].Stdout, "zah-"+r.InstanceID, "other-container")
			}
			f := &linuxFixtureRunner{replies: replies}
			if _, err := (Adapter{f}).LinuxProgramCandidates(context.Background(), r, id); err == nil {
				t.Fatal("accepted unavailable container")
			}
			if len(f.calls) != 1 || f.calls[0].Program != "docker" {
				t.Fatal("entered unavailable container")
			}
		})
	}
}
func TestLinuxGUICommandsStayInsideContainerAndReportMissingTool(t *testing.T) {
	for _, action := range []string{"files", "packages", "install", "unknown"} {
		t.Run(action, func(t *testing.T) {
			r, id, replies := linuxFixture(t)
			if action == "install" {
				r.Recipe.Strategy = "deb"
				r.Recipe.Entry = "/usr/bin/player"
				if _, err := prepare(context.Background(), r); err != nil {
					t.Fatal(err)
				}
			}
			f := &linuxFixtureRunner{replies: append(replies, process.Result{}, process.Result{})}
			proof, err := (Adapter{f}).LaunchLinuxInstallationGUI(context.Background(), r, id, action)
			if action == "unknown" {
				if err == nil || len(f.calls) != 0 {
					t.Fatal("accepted arbitrary GUI action")
				}
				return
			}
			if err != nil || proof.State != "maintenance_tool_exited" {
				t.Fatalf("%+v %v", proof, err)
			}
			c := f.calls[len(f.calls)-1]
			if c.Program != "distrobox" || c.SafeCancel || c.Timeout != 0 {
				t.Fatalf("unsafe GUI launch: %+v", c)
			}
			joined := strings.Join(c.Args, " ")
			if action != "files" && !strings.Contains(joined, "/usr/bin/sudo -n -- ") {
				t.Fatal("admin command not inside container")
			}
			if strings.Contains(joined, "apt-get") || strings.Contains(joined, "sh -c") {
				t.Fatal("installed tools or used shell")
			}
			f = &linuxFixtureRunner{replies: replies, failAt: 4}
			proof, err = (Adapter{f}).LaunchLinuxInstallationGUI(context.Background(), r, id, action)
			if !errors.Is(err, ErrManual) || proof.State != "waiting_user" || len(f.calls) != 4 {
				t.Fatalf("%+v %v", proof, err)
			}
		})
	}
}
