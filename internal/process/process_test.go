package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("ZAH_PROCESS_TEST_MODE")
	if mode == "" {
		return
	}
	switch mode {
	case "unsafe-failure":
		time.Sleep(100 * time.Millisecond)
		fmt.Fprint(os.Stderr, "transaction failed")
		os.Exit(31)
	case "hold-output":
		time.Sleep(2300 * time.Millisecond)
		_ = os.WriteFile(os.Getenv("ZAH_PROCESS_DONE"), []byte("done"), 0600)
	case "descendant":
		child := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
		child.Env = append(os.Environ(), "ZAH_PROCESS_TEST_MODE=hold-output")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(32)
		}
		_ = child.Process.Release()

	case "stderr-only":
		fmt.Print("complete-proof")
		fmt.Fprint(os.Stderr, strings.Repeat("diagnostic ", 10000))
	case "stdout-only":
		fmt.Print(strings.Repeat("output", 10000))
		fmt.Fprint(os.Stderr, "short")
	case "output":
		fmt.Print(strings.Repeat("o", 10000))
		fmt.Fprint(os.Stderr, strings.Repeat("e", 10000))
	case "env":
		fmt.Print(os.Getenv("LD_PRELOAD") + "|" + os.Getenv("HTTP_PROXY") + "|" + os.Getenv("ZAH_TEST_VALUE"))
	case "wait":
		time.Sleep(10 * time.Second)
	case "unsafe":
		time.Sleep(100 * time.Millisecond)
		fmt.Print("committed")
	case "exit":
		os.Exit(23)
	}
	os.Exit(0)
}

func helper(mode string) Command {
	return Command{Program: os.Args[0], Args: []string{"-test.run=^TestHelperProcess$"}, Env: map[string]string{"ZAH_PROCESS_TEST_MODE": mode, "GORACE": "atexit_sleep_ms=0"}, SafeCancel: true, Timeout: time.Second}
}

func TestBoundedOutput(t *testing.T) {
	cmd := helper("output")
	cmd.MaxOutput = 17
	result, err := (OSRunner{}).Run(context.Background(), cmd)
	if err != nil || result.ExitCode != 0 || !result.Truncated || len(result.Stdout) != 17 || len(result.Stderr) != 17 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}
func TestEnvironment(t *testing.T) {
	t.Setenv("LD_PRELOAD", "secret-library")
	t.Setenv("HTTP_PROXY", "http://existing.invalid:123")
	cmd := helper("env")
	cmd.Env["ZAH_TEST_VALUE"] = "kept"
	result, err := (OSRunner{}).Run(context.Background(), cmd)
	if err != nil || result.Stdout != "|http://existing.invalid:123|kept" {
		t.Fatalf("unexpected clean environment result %q, %v", result.Stdout, err)
	}
	for _, key := range []string{"LD_PRELOAD", "NODE_OPTIONS", "BASH_ENV", "GTK_PATH"} {
		if _, err := CleanEnvironment(nil, map[string]string{key: "secret"}); err == nil {
			t.Fatalf("accepted %s", key)
		}
	}
}
func TestSafeCancellation(t *testing.T) {
	cmd := helper("wait")
	cmd.Timeout = 50 * time.Millisecond
	begin := time.Now()
	result, err := (OSRunner{}).Run(context.Background(), cmd)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(begin) > 3*time.Second {
		t.Fatalf("cancel error %v", err)
	}
	if Alive(result.Identity) {
		t.Fatal("child still running")
	}
	if !result.DescendantsUnknown {
		t.Fatal("cancellation must not assert descendant termination")
	}
}
func TestUnsafeCancellationWaitsForCommit(t *testing.T) {
	cmd := helper("unsafe")
	cmd.Timeout = 20 * time.Millisecond
	cmd.SafeCancel = false
	result, err := (OSRunner{}).Run(context.Background(), cmd)
	if err != nil || result.Stdout != "committed" || result.ExitCode != 0 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}
func TestExitAndValidation(t *testing.T) {
	cmd := helper("exit")
	result, err := (OSRunner{}).Run(context.Background(), cmd)
	if err == nil || result.ExitCode != 23 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	cmd.Program = "/secret/nonexistent"
	_, err = (OSRunner{}).Run(context.Background(), cmd)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaks executable: %v", err)
	}
	cmd = helper("output")
	cmd.MaxOutput = maximumOutput + 1
	if _, err = (OSRunner{}).Run(context.Background(), cmd); err == nil {
		t.Fatal("unbounded output accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = (OSRunner{}).Run(ctx, helper("output")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestIdentity(t *testing.T) {
	id, err := Capture(os.Getpid())
	if err != nil || !Alive(id) {
		t.Fatalf("identity=%+v error=%v", id, err)
	}
	bad := id
	bad.StartTicks++
	if Alive(bad) {
		t.Fatal("reused PID accepted")
	}
	if err := Signal(bad, syscall.Signal(0)); err == nil {
		t.Fatal("mismatched identity signalled")
	}
	if Alive(Identity{}) {
		t.Fatal("empty identity accepted")
	}
}

func TestUnsafeCancellationPreservesFailure(t *testing.T) {
	cmd := helper("unsafe-failure")
	cmd.Timeout = 20 * time.Millisecond
	cmd.SafeCancel = false
	result, err := (OSRunner{}).Run(context.Background(), cmd)
	if err == nil || errors.Is(err, context.DeadlineExceeded) || result.ExitCode != 31 || result.Stderr != "transaction failed" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestDescendantOutputDescriptors(t *testing.T) {
	donePath := filepath.Join(t.TempDir(), "done")
	cmd := helper("descendant")
	cmd.Timeout = 5 * time.Second
	cmd.Env["ZAH_PROCESS_DONE"] = donePath
	result, err := (OSRunner{}).Run(context.Background(), cmd)
	// The leader succeeds, but the inherited pipe remains open. It is unsafe to
	// report an ordinary completed operation just because the leader exited.
	if err == nil || result.ExitCode != 0 || !result.DescendantsUnknown {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(donePath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("test descendant did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPidfdSignalTargetsOnlyRecordedChild(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "ZAH_PROCESS_TEST_MODE=wait")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	id, err := Capture(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	bad := id
	bad.BootID = "another-boot"
	if err := Signal(bad, syscall.SIGTERM); err == nil || !Alive(id) {
		t.Fatal("mismatched boot signalled child")
	}
	if err := Signal(id, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("expected terminated child")
	}
	if Alive(id) {
		t.Fatal("terminated child still alive")
	}
	if err := Signal(id, syscall.Signal(0)); err == nil {
		t.Fatal("stale process accepted")
	}
}

func TestEnvironmentFunctionInjectionAndLimits(t *testing.T) {
	base := []string{"BASH_FUNC_docker%%=() { echo injected; }", "SHELLOPTS=xtrace", "PERL5OPT=-Mbad", "PYTHONSTARTUP=/bad", "HTTP_PROXY=http://existing.invalid", "GOOD=value", "BAD\x00=invalid", "EMPTY\x00=invalid", "=invalid"}
	env, err := CleanEnvironment(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 2 || env[0] != "GOOD=value" || env[1] != "HTTP_PROXY=http://existing.invalid" {
		t.Fatalf("unexpected environment %q", env)
	}
	for _, key := range []string{"BASH_FUNC_docker%%", "SHELLOPTS", "PERL5OPT", "PYTHONSTARTUP"} {
		if _, err := CleanEnvironment(nil, map[string]string{key: "bad"}); err == nil {
			t.Fatalf("accepted %s", key)
		}
	}
	for _, n := range []int{0, 1, 8} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			b := &limitedBuffer{limit: n}
			data := []byte("12345678")
			w, err := b.Write(data)
			if w != len(data) || err != nil {
				t.Fatal("writer did not drain")
			}
			got, truncated := b.snapshot()
			if len(got) != n || truncated != (n < len(data)) {
				t.Fatalf("snapshot=%q truncated=%t", got, truncated)
			}
			_, _ = b.Write(nil)
		})
	}
}

func TestOutputTruncationIdentifiesAffectedStream(t *testing.T) {
	for _, tc := range []struct {
		mode           string
		stdout, stderr bool
	}{{"stderr-only", false, true}, {"stdout-only", true, false}, {"output", true, true}} {
		t.Run(tc.mode, func(t *testing.T) {
			cmd := helper(tc.mode)
			cmd.MaxOutput = 32
			result, err := (OSRunner{}).Run(context.Background(), cmd)
			if err != nil || !result.Truncated || result.StdoutTruncated != tc.stdout || result.StderrTruncated != tc.stderr {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if tc.mode == "stderr-only" && result.Stdout != "complete-proof" {
				t.Fatal("diagnostic truncation changed intact stdout")
			}
		})
	}
}
