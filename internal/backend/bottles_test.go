package backend

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/process"
)

func windowsRequest(t *testing.T) (InstallRequest, ManualBottlesInput, string, string) {
	r := request(t)
	r.Recipe.Platform = "windows"
	r.Recipe.Strategy = "exe"
	r.Recipe.Runner = "soda-9.0-1"
	r.Recipe.Image = ""
	r.Recipe.Entry = "音乐播放器"
	dir := filepath.Join(t.TempDir(), "bottle")
	entry := filepath.Join(dir, "drive_c", "Program Files", "Music", "music.exe")
	if err := os.MkdirAll(filepath.Dir(entry), 0700); err != nil {
		t.Fatal(err)
	}
	pe := make([]byte, 128)
	copy(pe, "MZ")
	binary.LittleEndian.PutUint32(pe[60:], 64)
	copy(pe[64:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(pe[68:], 0x8664)
	if err := os.WriteFile(entry, pe, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{name(r.InstanceID): map[string]any{"Name": name(r.InstanceID), "Arch": "win64", "Runner": r.Recipe.Runner, "Path": dir, "Custom_Path": true, "Creation_Date": "2026-11-01", "Environment": "Application"}}
	b, _ := json.Marshal(cfg)
	programs := []map[string]any{{"name": r.Recipe.Entry, "path": `C:\Program Files\Music\music.exe`, "arguments": "", "pre_script": nil, "post_script": ""}}
	p, _ := json.Marshal(programs)
	return r, ManualBottlesInput{BottlePath: dir, ExecutablePath: entry, ProgramName: r.Recipe.Entry}, string(b), string(p)
}
func TestVerifyManualBottlesAndLaunch(t *testing.T) {
	r, in, cfg, programs := windowsRequest(t)
	f := &scriptRunner{replies: []process.Result{{Stdout: cfg}, {Stdout: programs}, {Stdout: cfg}, {Stdout: programs}, {}}}
	a := Adapter{f}
	ev, err := a.VerifyManualBottles(context.Background(), r, in)
	if err != nil {
		t.Fatal(err)
	}
	if ev.State != "entry_verified" || ev.Windows == nil || ev.ResourceID == "" {
		t.Fatalf("%+v", ev)
	}
	r.Windows = ev.Windows
	launched, err := a.Launch(context.Background(), r, ev.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if launched.State != "launcher_exited_application_unknown" {
		t.Fatal(launched.State)
	}
	last := f.calls[len(f.calls)-1]
	if last.SafeCancel || !strings.Contains(strings.Join(last.Args, "|"), "run|-b|zah-"+r.InstanceID+"|-p|"+r.Recipe.Entry) {
		t.Fatalf("%+v", last)
	}
}
func TestBottlesRevalidationDetectsDrift(t *testing.T) {
	r, in, cfg, programs := windowsRequest(t)
	a := Adapter{&scriptRunner{replies: []process.Result{{Stdout: cfg}, {Stdout: programs}}}}
	ev, err := a.VerifyManualBottles(context.Background(), r, in)
	if err != nil {
		t.Fatal(err)
	}
	r.Windows = ev.Windows
	f, err := os.OpenFile(in.ExecutablePath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write([]byte("changed"))
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	a.Runner = &scriptRunner{replies: []process.Result{{Stdout: cfg}, {Stdout: programs}}}
	out, err := a.InspectBottles(context.Background(), r, ev.ResourceID)
	if err == nil || out.State != "changed" {
		t.Fatalf("%+v %v", out, err)
	}
}
func TestBottlesRejectsScriptsAmbiguityAndEscapedEntry(t *testing.T) {
	for _, mode := range []string{"script", "ambiguous", "outside", "bad_runner", "missing"} {
		t.Run(mode, func(t *testing.T) {
			r, in, cfg, programs := windowsRequest(t)
			var rows []map[string]any
			_ = json.Unmarshal([]byte(programs), &rows)
			switch mode {
			case "script":
				rows[0]["pre_script"] = "sh -c evil"
			case "ambiguous":
				rows = append(rows, rows[0])
			case "outside":
				rows[0]["path"] = `C:\..\outside.exe`
			case "bad_runner":
				cfg = strings.ReplaceAll(cfg, r.Recipe.Runner, "another-runner")
			case "missing":
				cfg = `{}`
			}
			b, _ := json.Marshal(rows)
			f := &scriptRunner{replies: []process.Result{{Stdout: cfg}, {Stdout: string(b)}}}
			if _, err := (Adapter{f}).VerifyManualBottles(context.Background(), r, in); err == nil {
				t.Fatal("accepted unsafe registration")
			}
			for _, call := range f.calls {
				if strings.Contains(strings.Join(call.Args, "|"), "run|-b") {
					t.Fatal("verification launched application")
				}
			}
		})
	}
}
