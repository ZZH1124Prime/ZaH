package backend

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/process"
)

func TestConfirmStoppedFailsClosed(t *testing.T) {
	r := request(t)
	id := strings.Repeat("c", 64)
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any, map[string]any)
		good   bool
	}{
		{"exited", nil, true},
		{"created", func(_ map[string]any, s map[string]any) { s["Status"] = "created" }, true},
		{"running", func(_ map[string]any, s map[string]any) { s["Running"] = true }, false},
		{"paused", func(_ map[string]any, s map[string]any) { s["Paused"] = true }, false},
		{"restarting", func(_ map[string]any, s map[string]any) { s["Restarting"] = true }, false},
		{"dead", func(_ map[string]any, s map[string]any) { s["Dead"] = true }, false},
		{"pid", func(_ map[string]any, s map[string]any) { s["Pid"] = 123 }, false},
		{"unknown_status", func(_ map[string]any, s map[string]any) { s["Status"] = "removing" }, false},
		{"missing_pid", func(_ map[string]any, s map[string]any) { delete(s, "Pid") }, false},
		{"missing_running", func(_ map[string]any, s map[string]any) { delete(s, "Running") }, false},
		{"null_state", func(v map[string]any, _ map[string]any) { v["State"] = nil }, false},
		{"foreign_name", func(v map[string]any, _ map[string]any) { v["Name"] = "/other" }, false},
		{"foreign_label", func(v map[string]any, _ map[string]any) { v["Config"] = map[string]any{} }, false},
		{"foreign_id", func(v map[string]any, _ map[string]any) { v["Id"] = strings.Repeat("d", 64) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := map[string]any{"Status": "exited", "Running": false, "Paused": false, "Restarting": false, "Dead": false, "Pid": 0}
			row := map[string]any{"Id": id, "Name": "/zah-" + r.InstanceID, "Config": map[string]any{"Labels": map[string]string{"org.zah.instance": r.InstanceID}}, "State": state}
			if tc.mutate != nil {
				tc.mutate(row, state)
			}
			raw, _ := json.Marshal([]any{row})
			runner := &fakeRunner{result: process.Result{Stdout: string(raw)}}
			ev, e := (Adapter{runner}).ConfirmStopped(context.Background(), r, id)
			if (e == nil) != tc.good || (ev.State == "stopped") != tc.good {
				t.Fatalf("%+v %v", ev, e)
			}
			if len(runner.calls) != 1 || strings.Join(runner.calls[0].Args, " ") != "container inspect "+id {
				t.Fatal("unexpected side effect")
			}
		})
	}
}
func TestConfirmStoppedWindowsUnsupportedAndTruncation(t *testing.T) {
	r := request(t)
	f := &fakeRunner{result: process.Result{Truncated: true}}
	if _, e := (Adapter{f}).ConfirmStopped(context.Background(), r, strings.Repeat("a", 64)); e == nil {
		t.Fatal("accepted truncated evidence")
	}
	r.Recipe.Platform = "windows"
	r.Recipe.Strategy = "exe"
	r.Recipe.Image = ""
	r.Recipe.Runner = "soda-9.0-1"
	r.Recipe.Entry = "Example"
	f.calls = nil
	if _, e := (Adapter{f}).ConfirmStopped(context.Background(), r, "unused"); !errors.Is(e, ErrUnsupported) {
		t.Fatal(e)
	}
	if len(f.calls) != 0 {
		t.Fatal("probed unsupported Windows processes")
	}
}

func TestVerifyAbsentChecksNameAndOldIdentity(t *testing.T) {
	id := strings.Repeat("a", 64)
	f := &fakeRunner{}
	ev, e := (Adapter{f}).VerifyAbsent(context.Background(), "instance-one", "linux", id)
	if e != nil || ev.State != "absent" || len(f.calls) != 2 {
		t.Fatalf("%+v %v", ev, e)
	}
	if f.calls[0].Args[6] != "name=zah-instance-one" || f.calls[1].Args[6] != "id="+id {
		t.Fatal(f.calls)
	}
	for _, result := range []process.Result{{Stdout: id}, {Truncated: true}, {Stdout: "daemon warning"}} {
		f := &fakeRunner{result: result}
		if _, e := (Adapter{f}).VerifyAbsent(context.Background(), "instance-one", "linux", id); e == nil {
			t.Fatal("accepted ambiguous absence")
		}
	}
	f.calls = nil
	if _, e := (Adapter{f}).VerifyAbsent(context.Background(), "instance-one", "windows", ""); !errors.Is(e, ErrUnsupported) || len(f.calls) != 0 {
		t.Fatal(e)
	}
}
