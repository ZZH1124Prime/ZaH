package backend

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/host"
	"github.com/ZZH1124Prime/ZaH/internal/process"
)

func wineControlFixture(t *testing.T) (InstallRequest, ManualBottlesInput, string, string, string) {
	r, in, cfg, programs := windowsRequest(t)
	var all map[string]map[string]any
	if e := json.Unmarshal([]byte(cfg), &all); e != nil {
		t.Fatal(e)
	}
	all[name(r.InstanceID)]["Parameters"] = map[string]any{"sandbox": false}
	all[name(r.InstanceID)]["Environment_Variables"] = map[string]any{}
	raw, _ := json.Marshal(all)
	dir, e := host.InspectBinding(in.BottlePath, true)
	if e != nil {
		t.Fatal(e)
	}
	p := WineControl{Server: "/test/bottles/runners/soda-9.0-1/bin/wineserver", SHA256: strings.Repeat("a", 64), WineSHA256: strings.Repeat("b", 64), Prefix: dir.Path, Device: dir.Device, Inode: dir.Inode, UID: 1000, TmpDevice: 3, TmpInode: 4}
	proof, _ := json.Marshal(p)
	return r, in, string(raw), programs, string(proof)
}
func TestWineStopUsesExactPinnedPrefixAndRunner(t *testing.T) {
	r, in, cfg, programs, proof := wineControlFixture(t)
	registration := []process.Result{{Stdout: cfg}, {Stdout: programs}, {Stdout: "/test/bottles/bottles"}, {Stdout: proof}}
	f := &scriptRunner{replies: append(append(registration, registration...), process.Result{Stdout: proof})}
	a := Adapter{f}
	ev, e := a.VerifyManualBottles(context.Background(), r, in)
	if e != nil || ev.Windows.Control == nil {
		t.Fatalf("%+v %v", ev, e)
	}
	r.Windows = ev.Windows
	out, e := a.Stop(context.Background(), r, ev.ResourceID)
	if e != nil || out.State != "stopped" {
		t.Fatalf("%+v %v", out, e)
	}
	c := f.calls[len(f.calls)-1]
	if c.Program != "flatpak" || c.SafeCancel || c.Timeout != 0 || c.Args[6] != "stop" || c.Args[8] != in.BottlePath || c.Args[9] != r.Recipe.Runner {
		t.Fatal("bad scoped command", c.Args)
	}
	for _, c := range f.calls {
		joined := strings.Join(c.Args, "|")
		if strings.Contains(joined, "--command=bottles-cli|com.usebottles.bottles|stop") || strings.Contains(joined, "pkill") || strings.Contains(joined, "killall") {
			t.Fatal("ambiguous stop")
		}
	}
}
func TestWineControlRejectsDriftAndTruncatedProof(t *testing.T) {
	for _, mode := range []string{"inode", "server", "namespace", "truncated", "command_error"} {
		t.Run(mode, func(t *testing.T) {
			r, in, cfg, programs, proof := wineControlFixture(t)
			f := &scriptRunner{replies: []process.Result{{Stdout: cfg}, {Stdout: programs}, {Stdout: "/test/bottles/bottles"}, {Stdout: proof}}}
			a := Adapter{f}
			ev, e := a.VerifyManualBottles(context.Background(), r, in)
			if e != nil {
				t.Fatal(e)
			}
			r.Windows = ev.Windows
			var p WineControl
			json.Unmarshal([]byte(proof), &p)
			switch mode {
			case "inode":
				p.Inode++
			case "server":
				p.SHA256 = strings.Repeat("c", 64)
			case "namespace":
				p.TmpInode++
			}
			raw, _ := json.Marshal(p)
			f = &scriptRunner{replies: []process.Result{{Stdout: string(raw), Truncated: mode == "truncated"}}}
			if mode == "command_error" {
				f.replies = nil
			}
			a.Runner = f
			if _, e = a.wineControl(context.Background(), r, "/test/bottles/bottles", "stop", r.Windows.Control); e == nil {
				t.Fatal("accepted unknown completion")
			}
		})
	}
}

func TestSharedWineTmpLifetimeDoesNotWeakenPersistentIdentity(t *testing.T) {
	r, in, cfg, programs, raw := wineControlFixture(t)
	var pinned WineControl
	if err := json.Unmarshal([]byte(raw), &pinned); err != nil {
		t.Fatal(err)
	}
	pinned.SharedTmpScope = strings.Repeat("d", 64)
	b, _ := json.Marshal(pinned)
	f := &scriptRunner{replies: []process.Result{{Stdout: cfg}, {Stdout: programs}, {Stdout: "/test/bottles/bottles"}, {Stdout: string(b)}}}
	a := Adapter{f}
	ev, err := a.VerifyManualBottles(context.Background(), r, in)
	if err != nil {
		t.Fatal(err)
	}
	r.Windows = ev.Windows
	for _, mode := range []string{"idle_gc", "scope", "prefix", "runner", "legacy", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			p := pinned
			p.TmpInode++
			switch mode {
			case "scope":
				p.SharedTmpScope = strings.Repeat("e", 64)
			case "prefix":
				p.Inode++
			case "runner":
				p.WineSHA256 = strings.Repeat("f", 64)
			case "legacy":
				p.SharedTmpScope = ""
			case "malformed":
				p.SharedTmpScope = "unverified"
			}
			b, _ := json.Marshal(p)
			a.Runner = &scriptRunner{replies: []process.Result{{Stdout: string(b)}}}
			_, err := a.wineControl(context.Background(), r, "/test/bottles/bottles", "wait", r.Windows.Control)
			if (err == nil) != (mode == "idle_gc") {
				t.Fatalf("%s: %v", mode, err)
			}
			copy := *ev.Windows
			copy.Control = &p
			if (copy.ResourceIdentity() == ev.ResourceID) != (mode == "idle_gc") {
				t.Fatal("persistent identity ignored durable drift")
			}
		})
	}
	if *ev.Windows.Control != pinned {
		t.Fatal("hashing mutated stored evidence")
	}
}
func TestUnsupportedWineModesDoNotInvokeControl(t *testing.T) {
	for _, mode := range []string{"sandbox", "proton", "system", "env"} {
		t.Run(mode, func(t *testing.T) {
			r, _, _, _ := windowsRequest(t)
			cfg := bottleConfig{Runner: r.Recipe.Runner, Parameters: json.RawMessage(`{"sandbox":false}`)}
			switch mode {
			case "sandbox":
				cfg.Parameters = json.RawMessage(`{"sandbox":true}`)
			case "proton":
				cfg.Runner = "ge-proton"
			case "system":
				cfg.Runner = "sys-wine"
			case "env":
				cfg.EnvironmentVariables = json.RawMessage(`{"WINEPREFIX":"/other"}`)
			}
			f := &fakeRunner{}
			_, _, e := (Adapter{f}).probeWineControl(context.Background(), r, cfg)
			if !errors.Is(e, ErrUnsupported) || len(f.calls) != 0 {
				t.Fatal(e)
			}
		})
	}
}

// Run only the control script against inert ELF fixtures in a private directory.
// No Flatpak, Wine, application process or host service is involved.
func TestWineControlHelperWithInertFixture(t *testing.T) {
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("Python unavailable")
	}
	root := t.TempDir()
	base := filepath.Join(root, "runners", "fixture")
	prefix := filepath.Join(root, "bottles", "zah-test")
	for _, dir := range []string{filepath.Join(base, "bin"), prefix} {
		if e = os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	binary, e := os.ReadFile("/bin/true")
	if e != nil {
		t.Skip("inert ELF fixture unavailable")
	}
	for _, n := range []string{"wine", "wineserver"} {
		if e = os.WriteFile(filepath.Join(base, "bin", n), binary, 0700); e != nil {
			t.Fatal(e)
		}
	}
	run := func(mode, proof string) ([]byte, error) {
		return exec.Command(python, "-I", "-c", wineControlHelper, mode, filepath.Join(root, "bottles"), prefix, "fixture", proof).CombinedOutput()
	}
	proof, e := run("probe", "null")
	if e != nil {
		t.Fatalf("probe: %s %v", proof, e)
	}
	if out, e := run("stop", strings.TrimSpace(string(proof))); e != nil {
		t.Fatalf("inert control: %s %v", out, e)
	}
	// A no-server -k result is accepted only after -w confirms completion.
	for _, waitFails := range []bool{false, true} {
		prelude := "import subprocess\noriginal_run=subprocess.run\ndef controlled(*a,**kw):\n if a[0][-1]=='-k':return subprocess.CompletedProcess(a[0],1)\n"
		if waitFails {
			prelude += " if a[0][-1]=='-w':raise subprocess.TimeoutExpired(a[0],20)\n"
		}
		prelude += " return original_run(*a,**kw)\nsubprocess.run=controlled\n"
		out, err := exec.Command(python, "-I", "-c", prelude+wineControlHelper, "stop", filepath.Join(root, "bottles"), prefix, "fixture", strings.TrimSpace(string(proof))).CombinedOutput()
		if (err != nil) != waitFails {
			t.Fatalf("waitFails=%v: %s %v", waitFails, out, err)
		}
	}
	// Simulate an in-flight namespace replacement, not normal idle Flatpak GC.
	prelude := "import os,types\noriginal=os.stat\ncalls=0\ndef changed(path,*a,**kw):\n global calls\n s=original(path,*a,**kw)\n if path=='/tmp':\n  calls+=1\n  if calls>1:return types.SimpleNamespace(st_dev=s.st_dev,st_ino=s.st_ino+1)\n return s\nos.stat=changed\n"
	out, err := exec.Command(python, "-I", "-c", prelude+wineControlHelper, "stop", filepath.Join(root, "bottles"), prefix, "fixture", strings.TrimSpace(string(proof))).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "namespace changed") {
		t.Fatalf("in-flight replacement accepted: %s %v", out, err)
	}
	if e = os.WriteFile(filepath.Join(base, "bin", "wineserver"), append(binary, 0), 0700); e != nil {
		t.Fatal(e)
	}
	if _, e := run("stop", strings.TrimSpace(string(proof))); e == nil {
		t.Fatal("runner replacement accepted")
	}
}

func TestDirectWineProgramRequiresOwnedLaunchTree(t *testing.T) {
	for _, tc := range []struct {
		name, parameters, program string
		want                      bool
	}{
		{"explicit", `{"winebridge":false}`, `{}`, true},
		{"default", `{}`, `{}`, false},
		{"bridge", `{"winebridge":true}`, `{}`, false},
		{"program_override", `{"winebridge":true}`, `{"winebridge":false}`, true},
		{"program_bridge", `{"winebridge":false}`, `{"winebridge":true}`, false},
		{"environment", `{"winebridge":false}`, `{"environment":{"WINEPREFIX":"other"}}`, false},
		{"wrapper", `{"winebridge":false}`, `{"launch_options":"wrapper %command%"}`, false},
		{"pre_script", `{"winebridge":false}`, `{"pre_script":"anything"}`, false},
		{"post_script", `{"winebridge":false}`, `{"post_script":"anything"}`, false},
		{"empty", `{"winebridge":false}`, `{"environment":{},"launch_options":"","post_script":null}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var program map[string]json.RawMessage
			if e := json.Unmarshal([]byte(tc.program), &program); e != nil {
				t.Fatal(e)
			}
			if got := directWineProgram(bottleConfig{Parameters: json.RawMessage(tc.parameters)}, program); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

// An inert replacement CLI records only fixture arguments and selected fixture
// environment values. This does not invoke Bottles, Flatpak, Wine or a proxy.
func TestControlledWineLaunchCleansOnlyChildControlEnvironment(t *testing.T) {
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("Python unavailable")
	}
	dir := t.TempDir()
	script := "#!" + python + "\nimport json,os,sys\nprint(json.dumps(dict(args=sys.argv[1:],wine={k:v for k,v in os.environ.items() if k.startswith('WINE')},tmp=os.environ.get('TMPDIR'),ld=os.environ.get('LD_AUDIT'),proxy=os.environ.get('https_proxy'),display=os.environ.get('DISPLAY'))))\n"
	if e = os.WriteFile(filepath.Join(dir, "bottles-cli"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	setup := `import os
os.environ.update({'WINESERVER':'untrusted','WINELOADER':'untrusted','WINEPREFIX':'other','WINE_SERVER_DIR':'other','TMPDIR':'other','LD_AUDIT':'untrusted','https_proxy':'http://fixture.invalid','DISPLAY':':fixture'})
`
	cmd := exec.Command(python, "-I", "-c", setup+controlledBottlesLaunchHelper, "zah-test", "App; literal")
	cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin"}
	out, e := cmd.Output()
	if e != nil {
		t.Fatal(e)
	}
	var got struct {
		Args           []string
		Wine           map[string]string
		Tmp, LD        *string
		Proxy, Display string
	}
	if e = json.Unmarshal(out, &got); e != nil {
		t.Fatal(e)
	}
	if strings.Join(got.Args, "|") != "run|-b|zah-test|-p|App; literal" || len(got.Wine) != 0 || got.Tmp != nil || got.LD != nil || got.Proxy != "http://fixture.invalid" || got.Display != ":fixture" {
		t.Fatalf("unexpected inert launch: %s", out)
	}
}

func TestUninstalledApplicationCanStillStopOnlyItsPinnedPrefix(t *testing.T) {
	r, in, cfg, programs, raw := wineControlFixture(t)
	f := &scriptRunner{replies: []process.Result{{Stdout: cfg}, {Stdout: programs}, {Stdout: "/test/bottles/bottles"}, {Stdout: raw}}}
	a := Adapter{f}
	proof, e := a.VerifyManualBottles(context.Background(), r, in)
	if e != nil {
		t.Fatal(e)
	}
	r.Windows = proof.Windows
	if e = os.Remove(in.ExecutablePath); e != nil {
		t.Fatal(e)
	} // Isolated fixture, not a user program.
	if _, e = a.InspectBottles(context.Background(), r, proof.ResourceID); e == nil {
		t.Fatal("missing program became launchable")
	}
	f = &scriptRunner{replies: []process.Result{{Stdout: raw}}}
	a.Runner = f
	if stopped, e := a.Stop(context.Background(), r, proof.ResourceID); e != nil || stopped.State != "stopped" {
		t.Fatal(stopped, e)
	}
	if len(f.calls) != 1 || f.calls[0].Args[6] != "stop" || f.calls[0].Args[8] != in.BottlePath {
		t.Fatal("stop scope changed", f.calls)
	}
	before := len(f.calls)
	if _, e = a.Stop(context.Background(), r, strings.Repeat("0", 64)); e == nil || len(f.calls) != before {
		t.Fatal("wrong identity accepted")
	}
}
