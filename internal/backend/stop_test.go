package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/process"
)

func stoppedResponse(r InstallRequest, id string, running bool) string {
	status := "exited"
	pid := 0
	if running {
		status = "running"
		pid = 123
	}
	return fmt.Sprintf(`[{"Id":%q,"Name":%q,"Config":{"Labels":{"org.zah.instance":%q}},"State":{"Running":%t,"Paused":false,"Restarting":false,"Dead":false,"Status":%q,"Pid":%d}}]`, id, "/zah-"+r.InstanceID, r.InstanceID, running, status, pid)
}
func TestStopOnlyExactOwnedEnvironmentAndRechecks(t *testing.T) {
	r := request(t)
	id := strings.Repeat("a", 64)
	f := &scriptRunner{replies: []process.Result{{Stdout: stoppedResponse(r, id, true)}, {Stdout: id}, {Stdout: stoppedResponse(r, id, false)}}}
	proof, e := (Adapter{f}).Stop(context.Background(), r, id)
	if e != nil || proof.State != "stopped" || proof.ResourceID != id || len(f.calls) != 3 {
		t.Fatalf("%+v %v", proof, e)
	}
	cmd := f.calls[1]
	if strings.Join(cmd.Args, " ") != "container stop -t 10 "+id || cmd.SafeCancel || cmd.Timeout != 0 {
		t.Fatal("unexpected shutdown scope", cmd)
	}
}
func TestStopDoesNotAcceptForeignOrUnconfirmedResources(t *testing.T) {
	r := request(t)
	id := strings.Repeat("a", 64)
	for _, mode := range []string{"foreign", "short_id", "still_running", "missing_state", "renamed_after"} {
		t.Run(mode, func(t *testing.T) {
			before := stoppedResponse(r, id, true)
			after := stoppedResponse(r, id, false)
			target := id
			switch mode {
			case "foreign":
				before = strings.ReplaceAll(before, r.InstanceID, "someone-else")
			case "short_id":
				target = id[:12]
			case "still_running":
				after = before
			case "missing_state":
				after = strings.Replace(after, `"Pid":0`, `"unused":0`, 1)
			case "renamed_after":
				after = strings.ReplaceAll(after, r.InstanceID, "other-instance")
			}
			f := &scriptRunner{replies: []process.Result{{Stdout: before}, {}, {Stdout: after}}}
			proof, e := (Adapter{f}).Stop(context.Background(), r, target)
			if e == nil || proof.State == "stopped" {
				t.Fatalf("accepted unknown stop %+v %v", proof, e)
			}
			if (mode == "foreign" || mode == "short_id") && len(f.calls) > 1 {
				t.Fatal("stopped unowned resource")
			}
		})
	}
}
func TestStopCancelledOrUnsupportedDoesNotExecute(t *testing.T) {
	r := request(t)
	f := &fakeRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := (Adapter{f}).Stop(ctx, r, strings.Repeat("a", 64)); !errors.Is(e, context.Canceled) || len(f.calls) != 0 {
		t.Fatal(e)
	}
	r, _, _, _ = windowsRequest(t)
	if _, e := (Adapter{f}).Stop(context.Background(), r, "unsupported"); !errors.Is(e, ErrUnsupported) || len(f.calls) != 0 {
		t.Fatal(e)
	}
}

func TestStopCommandFailureDoesNotClaimStopped(t *testing.T) {
	r := request(t)
	id := strings.Repeat("a", 64)
	f := &scriptRunner{replies: []process.Result{{Stdout: stoppedResponse(r, id, true)}}}
	ev, e := (Adapter{f}).Stop(context.Background(), r, id)
	if e == nil || ev.State == "stopped" || len(f.calls) != 2 {
		t.Fatalf("%+v %v", ev, e)
	}
}
