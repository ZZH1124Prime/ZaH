package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/ZZH1124Prime/ZaH/internal/host"
	"github.com/ZZH1124Prime/ZaH/internal/process"
	"github.com/ZZH1124Prime/ZaH/internal/recipe"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls  []process.Command
	result process.Result
}

func (f *fakeRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.calls = append(f.calls, c)
	return f.result, nil
}
func request(t *testing.T) InstallRequest {
	t.Helper()
	d := t.TempDir()
	b := []byte("archived installer")
	p := filepath.Join(d, "material")
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return InstallRequest{InstanceID: "01234567-89ab", ArtifactPath: p, ArtifactSHA256: hex.EncodeToString(h[:]), PrivateHome: filepath.Join(d, "private home"), Recipe: recipe.Recipe{SchemaVersion: 1, ID: "demo", Revision: "1", Platform: "linux", Architecture: "amd64", Strategy: "appimage", Image: "docker.io/library/ubuntu@sha256:" + strings.Repeat("a", 64), Entry: "squashfs-root/AppRun"}}
}
func TestPlanNoExecutionAndIndependentMaterial(t *testing.T) {
	r := request(t)
	f := &fakeRunner{}
	a := Adapter{f}
	p, err := a.PlanInstall(r)
	if err != nil || len(p.Steps) != 4 || len(f.calls) != 0 {
		t.Fatalf("plan: %+v %v", p, err)
	}
	if _, err = a.ExecuteStep(context.Background(), p.Steps[0]); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(r.ArtifactPath, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = verifyFile(context.Background(), filepath.Join(r.PrivateHome, ".zah-install/installer.appimage"), r.ArtifactSHA256); err != nil {
		t.Fatal(err)
	}
	if _, err = a.ExecuteStep(context.Background(), p.Steps[0]); err == nil {
		t.Fatal("accepted changed source/overwrite")
	}
}
func TestResourceOwnershipAndNonForceRemoval(t *testing.T) {
	r := request(t)
	id := strings.Repeat("a", 64)
	f := &fakeRunner{result: process.Result{Stdout: fmt.Sprintf(`[{"Id":%q,"Name":%q,"Config":{"Labels":{"org.zah.instance":%q}},"State":{"Running":true}}]`, id, "/zah-"+r.InstanceID, r.InstanceID)}}
	a := Adapter{f}
	if _, err := a.ExecuteStep(context.Background(), Step{Kind: "remove", Request: r, ResourceID: id, Mode: "automatic"}); err == nil {
		t.Fatal("removed running resource")
	}
	if len(f.calls) != 1 {
		t.Fatal("executed removal")
	}
	f.result.Stdout = strings.Replace(f.result.Stdout, r.InstanceID, "other", -1)
	if _, err := a.Inspect(context.Background(), r, id); err == nil {
		t.Fatal("accepted foreign resource")
	}
}
func TestManualWindowsAndInjection(t *testing.T) {
	r := request(t)
	r.InstanceID = "x; rm -rf /"
	if _, err := (Adapter{}).PlanInstall(r); err == nil {
		t.Fatal("invalid ID")
	}
	r = request(t)
	r.Recipe.Platform = "windows"
	r.Recipe.Strategy = "msi"
	r.Recipe.Runner = "soda-9.0-1"
	r.Recipe.Image = ""
	r.Recipe.Entry = "Example"
	p, err := (Adapter{}).PlanInstall(r)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{}
	for _, s := range p.Steps {
		if _, err = (Adapter{f}).ExecuteStep(context.Background(), s); err != ErrManual {
			t.Fatal("manual workflow not enforced")
		}
	}
	if len(f.calls) != 0 {
		t.Fatal("executed manual workflow")
	}
}
func TestRejectSymlinkHome(t *testing.T) {
	r := request(t)
	if err := os.Symlink(t.TempDir(), r.PrivateHome); err != nil {
		t.Fatal(err)
	}
	if _, err := prepare(context.Background(), r); err == nil {
		t.Fatal("accepted symlink home")
	}
}

type scriptRunner struct {
	calls   []process.Command
	replies []process.Result
}

func (f *scriptRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.calls = append(f.calls, c)
	i := len(f.calls) - 1
	if i >= len(f.replies) {
		return process.Result{}, fmt.Errorf("unexpected command")
	}
	return f.replies[i], nil
}
func TestCreateBindingsUseReviewedQuotedGrammar(t *testing.T) {
	r := request(t)
	d := filepath.Join(t.TempDir(), "音乐 folder")
	if err := os.Mkdir(d, 0700); err != nil {
		t.Fatal(err)
	}
	b, err := host.InspectBinding(d, true)
	if err != nil {
		t.Fatal(err)
	}
	r.Bindings = []Binding{{Host: b, Target: "/mnt/zah/music folder"}}
	id := strings.Repeat("a", 64)
	inspect := fmt.Sprintf(`[{"Id":%q,"Name":%q,"Config":{"Labels":{"org.zah.instance":%q}},"State":{"Running":false}}]`, id, "/zah-"+r.InstanceID, r.InstanceID)
	rootJSON := fmt.Sprintf("%q", t.TempDir())
	imageJSON := fmt.Sprintf(`[{"Id":%q,"Os":"linux","Architecture":"amd64","Size":1024}]`, "sha256:"+strings.Repeat("b", 64))
	f := &scriptRunner{replies: []process.Result{{Stdout: `{"Host":"unix:///var/run/docker.sock"}`}, {Stdout: rootJSON}, {Stdout: `{"driver":"overlay2","status":[]}`}, {Stdout: imageJSON}, {}, {}, {Stdout: id}, {Stdout: inspect}}}
	ev, err := (Adapter{f}).ExecuteStep(context.Background(), Step{Kind: "create", Request: r, Mode: "automatic"})
	if err != nil || ev.ResourceID != id {
		t.Fatalf("%+v %v", ev, err)
	}
	args := f.calls[5].Args
	want := "--label org.zah.instance=" + r.InstanceID + " --volume \"" + d + ":/mnt/zah/music folder:ro\""
	if args[len(args)-1] != want {
		t.Fatalf("unsafe flags: %q", args)
	}
	r.PrivateHome = filepath.Join(t.TempDir(), "$(touch victim)")
	if _, err := (Adapter{}).PlanInstall(r); err == nil {
		t.Fatal("accepted eval metacharacters")
	}
}
