package process

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const supervisorTestNonce = "0123456789abcdef0123456789abcdef"

// This helper runs only in explicit subprocesses. Calling Supervise in the
// ordinary go test process would incorrectly give it ownership of other tests'
// children, so every subreaper assertion below crosses an executable boundary.
func TestSuperviseHelper(t *testing.T) {
	var args []string
	for i, a := range os.Args {
		if a == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(90)
	}
	child := func(role, dir string) {
		cmd := exec.Command(executable, "-test.run=^TestSuperviseHelper$", "--", role, dir)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if role == "leaf" {
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		}
		if err := cmd.Start(); err != nil {
			os.Exit(91)
		}
		_ = cmd.Process.Release()
	}
	switch args[0] {
	case "supervisor", "supervisor-controlled", "supervisor-delayed":
		if len(args) != 3 {
			os.Exit(92)
		}
		marker := filepath.Join(filepath.Dir(executable), "run-"+supervisorTestNonce+".started")
		supervise := Supervise
		role := "entry"
		if args[0] != "supervisor" {
			supervise = SuperviseControlled
		}
		if args[0] == "supervisor-delayed" {
			role = "delayed"
		}
		err := supervise(supervisorTestNonce, marker, executable, []string{"-test.run=^TestSuperviseHelper$", "--", role, args[1]}, os.Stdout)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(93)
		}
	case "delayed":
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if _, e := os.Stat(filepath.Join(args[1], "release")); e == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		os.WriteFile(filepath.Join(args[1], "late-start"), []byte("started"), 0600)
		child("leaf", args[1])
		os.Exit(0)
	case "entry":
		child("middle", args[1])
		// Application output must never be mistaken for helper completion evidence.
		fmt.Fprintln(os.Stdout, `{"completed":true,"nonce":"application-output"}`)
		os.Exit(23)
	case "middle":
		child("leaf", args[1])
		os.Exit(0)
	case "leaf":
		id, err := Capture(os.Getpid())
		if err != nil {
			os.Exit(94)
		}
		b, _ := json.Marshal(id)
		if os.WriteFile(filepath.Join(args[1], "leaf.identity"), b, 0600) != nil {
			os.Exit(95)
		}
		deadline := time.Now().Add(8 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(args[1], "release")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(96)
			}
			time.Sleep(5 * time.Millisecond)
		}
	default:
		os.Exit(97)
	}
	os.Exit(0)
}

type superviseFixture struct {
	dir, helper, marker string
	cmd                 *exec.Cmd
	done                chan struct{}
	err                 error
	out, stderr         bytes.Buffer
}

func newSuperviseFixture(t *testing.T) *superviseFixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Join(dir, ".zah-runtime")
	if err := os.Mkdir(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	helper := filepath.Join(runtimeDir, "zah")
	target, err := os.OpenFile(helper, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(target, source); err != nil {
		target.Close()
		t.Fatal(err)
	}
	if err = target.Close(); err != nil {
		t.Fatal(err)
	}
	f := &superviseFixture{dir: dir, helper: helper, marker: filepath.Join(runtimeDir, "run-"+supervisorTestNonce+".started"), done: make(chan struct{})}
	t.Cleanup(func() {
		_ = os.Chmod(runtimeDir, 0700)
		_ = os.WriteFile(filepath.Join(dir, "release"), nil, 0600)
		if f.cmd != nil {
			select {
			case <-f.done:
			case <-time.After(2 * time.Second):
				_ = f.cmd.Process.Kill()
				select {
				case <-f.done:
				case <-time.After(2 * time.Second):
					t.Error("test supervisor did not exit")
				}
			}
		}
		b, err := os.ReadFile(filepath.Join(dir, "leaf.identity"))
		if err == nil {
			var id Identity
			if json.Unmarshal(b, &id) == nil && Alive(id) {
				if err := Signal(id, syscall.SIGKILL); err != nil {
					t.Errorf("test leaf cleanup failed: %v", err)
				}
			}
		}
	})
	return f
}
func (f *superviseFixture) start(t *testing.T) { f.startRole(t, "supervisor") }
func (f *superviseFixture) startRole(t *testing.T, role string) {
	t.Helper()
	f.cmd = exec.Command(f.helper, "-test.run=^TestSuperviseHelper$", "--", role, f.dir, "test")
	f.cmd.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
	f.cmd.Stdout, f.cmd.Stderr = &f.out, &f.stderr
	if err := f.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { f.err = f.cmd.Wait(); close(f.done) }()
}
func (f *superviseFixture) leaf(t *testing.T) Identity {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(filepath.Join(f.dir, "leaf.identity"))
		if err == nil {
			var id Identity
			if json.Unmarshal(b, &id) == nil && Alive(id) {
				return id
			}
		}
		select {
		case <-f.done:
			t.Fatalf("supervisor exited before live leaf: %v %s", f.err, f.stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("test leaf did not become ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func (f *superviseFixture) release(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not finish after tree exit")
	}
}
func assertWaiting(t *testing.T, f *superviseFixture) {
	t.Helper()
	select {
	case <-f.done:
		t.Fatalf("supervisor ended while descendant remained: %v %s", f.err, f.stderr.String())
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSuperviseWaitsForDoubleForkAndSeparatesProof(t *testing.T) {
	f := newSuperviseFixture(t)
	f.start(t)
	leaf := f.leaf(t)
	assertWaiting(t, f)
	data, err := os.ReadFile(f.marker)
	if err != nil {
		t.Fatal(err)
	}
	var started SuperviseStarted
	if err = json.Unmarshal(data, &started); err != nil {
		t.Fatal(err)
	}
	if started.Protocol != SuperviseProtocol || started.Nonce != supervisorTestNonce || started.PID != f.cmd.Process.Pid {
		t.Fatalf("invalid started marker: %+v", started)
	}
	info, err := os.Stat(f.marker)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("marker permissions: %v %v", info, err)
	}
	f.release(t)
	if f.err != nil {
		t.Fatalf("supervisor failed: %v %s", f.err, f.stderr.String())
	}
	if Alive(leaf) {
		t.Fatal("completion emitted with surviving descendant")
	}
	var complete SuperviseCompletion
	decoder := json.NewDecoder(bytes.NewReader(f.out.Bytes()))
	if err = decoder.Decode(&complete); err != nil {
		t.Fatal(err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		t.Fatal("stdout contained extra application output")
	}
	if !complete.Completed || complete.Protocol != SuperviseProtocol || complete.Nonce != supervisorTestNonce || complete.EntryExitCode != 23 || complete.Reaped != 3 {
		t.Fatalf("completion=%+v", complete)
	}
	if !strings.Contains(f.stderr.String(), "application-output") {
		t.Fatal("application stdout not redirected to stderr")
	}
}

func TestSuperviseInterruptNeverProvesCompletion(t *testing.T) {
	f := newSuperviseFixture(t)
	f.start(t)
	leaf := f.leaf(t)
	if err := f.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	assertWaiting(t, f)
	if !Alive(leaf) {
		t.Fatal("supervisor signal killed descendant")
	}
	f.release(t)
	if f.err == nil || f.out.Len() != 0 || !strings.Contains(f.stderr.String(), "no completion proof") {
		t.Fatalf("interrupted supervisor returned proof: err=%v out=%q stderr=%q", f.err, f.out.String(), f.stderr.String())
	}
	if Alive(leaf) {
		t.Fatal("interrupted helper abandoned descendant")
	}
}

func TestSuperviseExistingMarkerPreventsLaunch(t *testing.T) {
	f := newSuperviseFixture(t)
	if err := os.WriteFile(f.marker, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	f.start(t)
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		t.Fatal("duplicate marker did not fail")
	}
	if f.err == nil || f.out.Len() != 0 {
		t.Fatal("duplicate marker accepted")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "leaf.identity")); !os.IsNotExist(err) {
		t.Fatal("application launched despite preexisting marker")
	}
	b, err := os.ReadFile(f.marker)
	if err != nil || string(b) != "existing" {
		t.Fatal("existing marker overwritten")
	}
}

func TestSuperviseMarkerFailureStillReapsDescendants(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires ordinary-user directory permissions")
	}
	f := newSuperviseFixture(t)
	if err := os.Chmod(filepath.Dir(f.helper), 0500); err != nil {
		t.Fatal(err)
	}
	f.start(t)
	leaf := f.leaf(t)
	assertWaiting(t, f)
	if _, err := os.Stat(f.marker); !os.IsNotExist(err) {
		t.Fatalf("unexpected marker: %v", err)
	}
	f.release(t)
	if f.err == nil || f.out.Len() != 0 {
		t.Fatal("marker failure produced success")
	}
	if Alive(leaf) {
		t.Fatal("marker failure abandoned descendant")
	}
}

func TestSuperviseRejectsInvalidRequestsBeforeSubreaping(t *testing.T) {
	for _, tc := range []struct {
		nonce, entry string
		args         []string
	}{{"short", "/bin/true", nil}, {strings.Repeat("G", 32), "/bin/true", nil}, {supervisorTestNonce, "relative", nil}, {supervisorTestNonce, "/bin/../bin/true", nil}, {supervisorTestNonce, "/bin/true", []string{"bad\x00argument"}}} {
		var out bytes.Buffer
		if err := Supervise(tc.nonce, "/invalid-marker", tc.entry, tc.args, &out); err == nil || out.Len() != 0 {
			t.Fatalf("accepted invalid request %+v", tc)
		}
	}
}

func TestControlledSupervisorDrainsOwnedDoubleForkOnly(t *testing.T) {
	f := newSuperviseFixture(t)
	f.startRole(t, "supervisor-controlled")
	leaf := f.leaf(t)
	peer := newSuperviseFixture(t)
	peer.start(t)
	other := peer.leaf(t)
	stop := strings.TrimSuffix(f.marker, ".started") + ".stop"
	if e := os.WriteFile(stop, []byte(supervisorTestNonce), 0600); e != nil {
		t.Fatal(e)
	}
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		t.Fatal("controlled launch did not drain")
	}
	if f.err != nil {
		t.Fatalf("%v %s", f.err, f.stderr.String())
	}
	var proof SuperviseCompletion
	if e := json.Unmarshal(f.out.Bytes(), &proof); e != nil {
		t.Fatal(e)
	}
	if !proof.Completed || proof.Reaped < 3 || Alive(leaf) {
		t.Fatalf("incomplete drain %+v", proof)
	}
	if !Alive(other) {
		t.Fatal("unrelated sibling launch was affected")
	}
	peer.release(t)
}
func TestControlledSupervisorPreventsDelayedStartAndRejectsWrongNonce(t *testing.T) {
	f := newSuperviseFixture(t)
	f.startRole(t, "supervisor-delayed")
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, e := os.Stat(f.marker); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no marker")
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop := strings.TrimSuffix(f.marker, ".started") + ".stop"
	if e := os.WriteFile(stop, []byte(strings.Repeat("f", 32)), 0600); e != nil {
		t.Fatal(e)
	}
	select {
	case <-f.done:
		t.Fatal("wrong nonce stopped launch")
	case <-time.After(80 * time.Millisecond):
	}
	if e := os.WriteFile(stop, []byte(supervisorTestNonce), 0600); e != nil {
		t.Fatal(e)
	}
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		t.Fatal("delayed launcher did not drain")
	}
	if f.err != nil {
		t.Fatalf("%v %s", f.err, f.stderr.String())
	}
	if e := os.WriteFile(filepath.Join(f.dir, "release"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(f.dir, "late-start")); !os.IsNotExist(e) {
		t.Fatal("cancelled launcher started application")
	}
}
