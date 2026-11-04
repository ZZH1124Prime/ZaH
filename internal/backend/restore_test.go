package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ZZH1124Prime/ZaH/internal/process"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type rebuildLinuxRunner struct {
	r                                     InstallRequest
	image, token, root                    string
	created, stopped, failVerify, foreign bool
	calls                                 []process.Command
}

func (f *rebuildLinuxRunner) Run(ctx context.Context, c process.Command) (process.Result, error) {
	f.calls = append(f.calls, c)
	args := strings.Join(c.Args, "|")
	out := process.Result{}
	id := strings.Repeat("b", 64)
	switch {
	case c.Program == "distrobox" && c.Args[0] == "create":
		f.created = true
	case c.Program == "distrobox" && strings.HasSuffix(args, "id|-u"):
		out.Stdout = strconv.Itoa(os.Geteuid())
	case c.Program == "distrobox" && strings.Contains(args, "|test|-x|"):
		if f.failVerify {
			return out, errors.New("missing entry")
		}
	case strings.HasPrefix(args, "container|ls|"):
		if f.created {
			out.Stdout = id
		}
	case strings.HasPrefix(args, "container|inspect|--format|"):
		out.Stdout = f.image
	case strings.HasPrefix(args, "container|inspect|"):
		token := f.token
		if f.foreign {
			token = "foreign"
		}
		b, _ := json.Marshal([]any{map[string]any{"Id": id, "Name": "/" + name(f.r.InstanceID), "Image": f.image, "Config": map[string]any{"Labels": map[string]string{"org.zah.instance": f.r.InstanceID, "org.zah.restore": token}, "Env": []string{"HOME=" + f.r.PrivateHome}}, "Mounts": []any{map[string]any{"Type": "bind", "Source": f.r.PrivateHome, "Destination": f.r.PrivateHome, "RW": true}}, "State": map[string]any{"Running": !f.stopped, "Paused": false, "Restarting": false, "Dead": false, "Pid": 0, "Status": "exited"}}})
		out.Stdout = string(b)
	case strings.HasPrefix(args, "container|stop|"):
		f.stopped = true
	case strings.HasPrefix(args, "context|inspect|"):
		out.Stdout = `{"Host":"unix:///fixture/docker.sock"}`
	case strings.HasPrefix(args, "info|") && strings.Contains(args, "DockerRootDir"):
		b, _ := json.Marshal(f.root)
		out.Stdout = string(b)
	case strings.HasPrefix(args, "info|"):
		out.Stdout = `{"driver":"overlay2","status":[]}`
	case args == "image|ls|--all|--no-trunc|--quiet":
		out.Stdout = f.image
	case strings.HasPrefix(args, "image|inspect|"):
		out.Stdout = fmt.Sprintf(`[{"Id":%q,"Architecture":"amd64","Os":"linux"}]`, f.image)
	default:
		return out, fmt.Errorf("unexpected restore command: %s %s", c.Program, args)
	}
	return out, nil
}
func TestRebuildLinuxRetryKeepsOwnedTargetAndNeverReplaysInstaller(t *testing.T) {
	r := request(t)
	if e := os.Mkdir(r.PrivateHome, 0700); e != nil {
		t.Fatal(e)
	}
	path := writeSnapshot(t, snapshotFixture(t, nil, nil).Bytes())
	info, e := InspectDockerArchive(context.Background(), path, MaxSnapshotBytes)
	if e != nil {
		t.Fatal(e)
	}
	f := &rebuildLinuxRunner{r: r, image: info.ImageID, token: strings.Repeat("a", 32), root: t.TempDir(), failVerify: true}
	a := Adapter{f}
	persisted := ""
	stage := t.TempDir()
	if e = os.Chmod(stage, 0700); e != nil {
		t.Fatal(e)
	}
	persist := func(ev Evidence) error { persisted = ev.ResourceID; return nil }
	_, e = a.RebuildLinux(context.Background(), r, path, info.SHA256, f.token, stage, persist)
	if e == nil || persisted == "" {
		t.Fatal("failed verification lost resource evidence", e, persisted)
	}
	f.failVerify = false
	ev, e := a.RebuildLinux(context.Background(), r, path, info.SHA256, f.token, stage, persist)
	if e != nil || ev.State != "entry_verified" || !f.stopped {
		t.Fatal(ev, e)
	}
	creates := 0
	for _, c := range f.calls {
		if c.Program == "distrobox" && c.Args[0] == "create" {
			creates++
		}
		if strings.Contains(strings.Join(c.Args, "|"), "apt-get") {
			t.Fatal("installer replayed")
		}
	}
	if creates != 1 {
		t.Fatal("duplicate creation", creates)
	}
	f.foreign = true
	before := len(f.calls)
	if _, e = a.RebuildLinux(context.Background(), r, path, info.SHA256, f.token, stage, persist); e == nil {
		t.Fatal("adopted foreign target")
	}
	for _, c := range f.calls[before:] {
		if c.Program == "distrobox" {
			t.Fatal("mutated foreign target")
		}
	}
}

type rebuildBottleRunner struct {
	*guidedBottleRunner
	missingRunner bool
}

func (f *rebuildBottleRunner) Run(ctx context.Context, c process.Command) (process.Result, error) {
	if strings.Contains(strings.Join(c.Args, "|"), "category:runners") {
		out := `{"runners":[]}`
		if !f.missingRunner {
			b, _ := json.Marshal(map[string]any{"runners": []string{f.r.Recipe.Runner}})
			out = string(b)
		}
		return process.Result{Stdout: out}, nil
	}
	out, e := f.guidedBottleRunner.Run(ctx, c)
	if e == nil && len(c.Args) > 3 && c.Args[3] == "new" {
		e = os.WriteFile(filepath.Join(f.root, name(f.r.InstanceID), "bottle.yml"), []byte("new target configuration"), 0600)
	}
	return out, e
}
func bottleRestoreFixture(t *testing.T) (InstallRequest, *rebuildBottleRunner, string, string, PortableBottle) {
	t.Helper()
	r, in, _, _ := windowsRequest(t)
	for _, name := range []string{"bottle.yml", "system.reg", "user.reg", "userdef.reg"} {
		if e := os.WriteFile(filepath.Join(in.BottlePath, name), []byte("source fixture"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	archive, digest := fullBottleArchive(t, in, "")
	entry, e := filepath.Rel(in.BottlePath, in.ExecutablePath)
	if e != nil {
		t.Fatal(e)
	}
	sum, e := hashRegular(context.Background(), in.ExecutablePath, 4<<30)
	if e != nil {
		t.Fatal(e)
	}
	u, e := user.Current()
	if e != nil {
		t.Fatal(e)
	}
	p := PortableBottle{SchemaVersion: 1, Username: u.Username, Entry: filepath.ToSlash(entry), SHA256: sum, Runner: r.Recipe.Runner, Parameters: map[string]json.RawMessage{"sandbox": json.RawMessage("false"), "winebridge": json.RawMessage("false")}}
	root := filepath.Join(t.TempDir(), "bottles")
	if e = os.Mkdir(root, 0700); e != nil {
		t.Fatal(e)
	}
	return r, &rebuildBottleRunner{guidedBottleRunner: &guidedBottleRunner{r: r, root: root, programs: map[string]string{}}}, archive, digest, p
}
func TestRebuildBottleUsesNewConfigExactEntryAndCrashResume(t *testing.T) {
	r, f, archive, digest, p := bottleRestoreFixture(t)
	a := Adapter{f}
	state := BottleRebuildState{}
	token := strings.Repeat("c", 32)
	fail := true
	save := func() error {
		if fail && state.Phase == "published" {
			fail = false
			return errors.New("lost database reply")
		}
		return nil
	}
	if _, e := a.RebuildBottle(context.Background(), r, archive, digest, token, p, &state, save); e == nil {
		t.Fatal("expected checkpoint failure")
	}
	if state.Phase != "published" {
		t.Fatal(state)
	}
	ev, e := a.RebuildBottle(context.Background(), r, archive, digest, token, p, &state, save)
	if e != nil || ev.State != "entry_verified" || ev.Windows == nil || !ev.Windows.ControlledLaunch {
		t.Fatal(ev, e)
	}
	got, e := os.ReadFile(filepath.Join(ev.Windows.Bottle.Path, "bottle.yml"))
	if e != nil || string(got) != "new target configuration" {
		t.Fatal("source metadata installed", string(got), e)
	}
	count := 0
	for _, c := range f.calls {
		if len(c.Args) > 3 && c.Args[3] == "new" {
			count++
		}
		if len(c.Args) > 3 && c.Args[3] == "run" {
			t.Fatal("application launched during restoration")
		}
	}
	if count != 1 {
		t.Fatal("duplicate prefix creation", count)
	}
	if _, e = a.PortableBottle(context.Background(), func() InstallRequest { r.Windows = ev.Windows; return r }()); e != nil {
		t.Fatal(e)
	}
}
func TestBottleRestorePrerequisitesAndExtractionFailuresPreserveExistingData(t *testing.T) {
	r, f, archive, digest, p := bottleRestoreFixture(t)
	a := Adapter{f}
	state := BottleRebuildState{}
	save := func() error { return nil }
	token := strings.Repeat("c", 32)
	f.missingRunner = true
	if _, e := a.RebuildBottle(context.Background(), r, archive, digest, token, p, &state, save); e == nil || f.created {
		t.Fatal("created without runner", e)
	}
	f.missingRunner = false
	p.Username += "-different"
	if _, e := a.RebuildBottle(context.Background(), r, archive, digest, token, p, &state, save); e == nil || f.created {
		t.Fatal("rewrote username", e)
	}
	target := filepath.Join(t.TempDir(), "stage")
	if e := extractBottlePayload(context.Background(), archive, target, strings.Repeat("0", 64)); e == nil {
		t.Fatal("accepted altered archive digest")
	}
	if _, e := os.Lstat(target); !os.IsNotExist(e) {
		t.Fatal("failed partial stage not cleaned", e)
	}
	if e := os.Mkdir(target, 0700); e != nil {
		t.Fatal(e)
	}
	sentinel := filepath.Join(target, "existing")
	_ = os.WriteFile(sentinel, []byte("keep"), 0600)
	if e := extractBottlePayload(context.Background(), archive, target, digest); e == nil {
		t.Fatal("overwrote existing stage")
	}
	if b, e := os.ReadFile(sentinel); e != nil || string(b) != "keep" {
		t.Fatal("existing data changed")
	}
}
