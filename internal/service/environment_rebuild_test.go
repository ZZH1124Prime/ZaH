package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/process"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type serviceRestoreRunner struct {
	r                                     backend.InstallRequest
	image, token, root                    string
	created, stopped, failVerify, foreign bool
	calls                                 []process.Command
}

func (f *serviceRestoreRunner) Run(ctx context.Context, c process.Command) (process.Result, error) {
	f.calls = append(f.calls, c)
	args := strings.Join(c.Args, "|")
	out := process.Result{}
	id := strings.Repeat("b", 64)
	switch {
	case c.Program == "distrobox" && c.Args[0] == "create":
		f.created = true
		for _, arg := range c.Args {
			if _, rest, ok := strings.Cut(arg, "org.zah.restore="); ok {
				f.token = strings.Fields(rest)[0]
			}
		}
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
		b, _ := json.Marshal([]any{map[string]any{"Id": id, "Name": "/" + ("zah-" + f.r.InstanceID), "Image": f.image, "Config": map[string]any{"Labels": map[string]string{"org.zah.instance": f.r.InstanceID, "org.zah.restore": token}, "Env": []string{"HOME=" + f.r.PrivateHome}}, "Mounts": []any{map[string]any{"Type": "bind", "Source": f.r.PrivateHome, "Destination": f.r.PrivateHome, "RW": true}}, "State": map[string]any{"Running": !f.stopped, "Paused": false, "Restarting": false, "Dead": false, "Pid": 0, "Status": "exited"}}})
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

func TestImportedEnvironmentRebuildPersistsFailureThenAtomicallyBecomesReady(t *testing.T) {
	ctx := context.Background()
	src, old := fullSource(t)
	archive := filepath.Join(t.TempDir(), "source.zahbundle")
	if _, e := src.ExportEnvironmentBundle(ctx, EnvironmentBundleInput{ID: old.ID, Path: archive, ColdConfirmed: true}); e != nil {
		t.Fatal(e)
	}
	dst := catalogService(t)
	parent := t.TempDir()
	if e := os.Chmod(parent, 0700); e != nil {
		t.Fatal(e)
	}
	imported, e := dst.RestoreEnvironmentBundle(ctx, archive, RestoreOptions{Merge: true, Directory: parent})
	if e != nil {
		t.Fatal(e)
	}
	id := imported.IDMap["instance:"+old.ID]
	v, e := get[domain.AppInstance](ctx, dst.Store, "instance", id)
	if e != nil {
		t.Fatal(e)
	}
	r, env, e := dst.request(ctx, v)
	if e != nil {
		t.Fatal(e)
	}
	var pending PendingEnvironmentRestore
	_ = json.Unmarshal(env.PendingRestore, &pending)
	f := &serviceRestoreRunner{r: r, image: pending.ImageID, root: t.TempDir(), failVerify: true}
	dst.Backend = backend.Adapter{Runner: f}
	input, _ := json.Marshal(instanceInput{ID: id})
	p, e := dst.planRebuild(ctx, planRequest{Operation: "environment.rebuild", Input: input})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = dst.Execute(ctx, p); e == nil {
		t.Fatal("expected entry verification failure")
	}
	v, _ = get[domain.AppInstance](ctx, dst.Store, "instance", id)
	env, _ = get[domain.Environment](ctx, dst.Store, "environment", v.EnvironmentID)
	if v.State != "needs_runtime_restore" || env.Identity == "" || env.State != "restore_created" {
		t.Fatal(v.State, env)
	}
	f.failVerify = false
	p, e = dst.planRebuild(ctx, planRequest{Operation: "environment.rebuild", Input: input})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = dst.Execute(ctx, p); e != nil {
		t.Fatal(e)
	}
	v, _ = get[domain.AppInstance](ctx, dst.Store, "instance", id)
	env, _ = get[domain.Environment](ctx, dst.Store, "environment", v.EnvironmentID)
	if v.State != "ready" || env.State != "stopped" || len(v.Entry) != 1 || !f.stopped {
		t.Fatal(v, env)
	}
	b, e := os.ReadFile(filepath.Join(v.PrivateData, "笔记.txt"))
	if e != nil || string(b) != "persisted notes" {
		t.Fatal("imported user data changed", e)
	}
	before := len(f.calls)
	if _, e = dst.Execute(ctx, p); e == nil || len(f.calls) != before {
		t.Fatal("stale plan executed on ready environment", e)
	}
	if _, e = dst.planRebuild(ctx, planRequest{Operation: "environment.rebuild", Input: input}); e == nil {
		t.Fatal("ready environment rebuilt")
	}
}

func TestStopAllReportsUnregisteredRestoreInsteadOfClaimingSuccess(t *testing.T) {
	ctx := context.Background()
	s, v := fullSource(t)
	env, e := get[domain.Environment](ctx, s.Store, "environment", v.EnvironmentID)
	if e != nil {
		t.Fatal(e)
	}
	env.Identity = ""
	env.State = "restore_creating"
	v.State = "needs_runtime_restore"
	if e = s.Store.Put(ctx, record("instance", v.ID, v)); e != nil {
		t.Fatal(e)
	}
	if e = s.Store.Put(ctx, record("environment", env.ID, env)); e != nil {
		t.Fatal(e)
	}
	p, e := s.planStopAll(ctx, planRequest{Operation: "applications.stopAll", Input: json.RawMessage(`{"confirmed":true}`)})
	if e != nil {
		t.Fatal(e)
	}
	before := s.Backend.Runner.(*fullBundleRunner).calls
	result, e := s.executeStopAll(ctx, p)
	if e == nil || result.Failed != 1 || len(result.Results) != 1 || result.Results[0].InstanceID != v.ID {
		t.Fatal(result, e)
	}
	if s.Backend.Runner.(*fullBundleRunner).calls != before {
		t.Fatal("executed backend without verified identity")
	}
	env.State = "restore_pending"
	env.PendingRestore = json.RawMessage(`{"bottle_state":{"phase":"creating"}}`)
	if !incompleteRestore(env) {
		t.Fatal("unclaimed Bottle omitted")
	}
	env.PendingRestore = json.RawMessage(`{"attempt_token":"persisted","backend":"bottles-flatpak"}`)
	if incompleteRestore(env) {
		t.Fatal("read-only prerequisite failure blocks close")
	}
}
