// Package process runs bounded external commands without a shell. It does not
// install software, elevate privileges, or change the host's network settings.
package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Command is an internal execution request. Business APIs must generate its
// executable and arguments from validated operations, never accept raw RPC argv.
type Command struct {
	// Output is internal-only, receives raw chunks; callers must redact before persistence.
	Output    func(string) `json:"-"`
	Program   string
	Args      []string
	Env       map[string]string
	Dir       string
	Timeout   time.Duration
	MaxOutput int
	// SafeCancel permits termination. When false, cancellation waits for the
	// transaction to finish; callers must retain locks and show cancel_requested.
	SafeCancel bool
}

// Result retains bounded evidence. Consumers must redact application output
// before placing it in logs; the runner never logs arguments or environment.
type Result struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	// Truncated remains the aggregate compatibility flag. Stream-specific flags
	// let callers distinguish intact protocol stdout from shortened diagnostics.
	Truncated       bool     `json:"truncated"`
	StdoutTruncated bool     `json:"stdout_truncated"`
	StderrTruncated bool     `json:"stderr_truncated"`
	Identity        Identity `json:"identity"`
	// DescendantsUnknown requires reconciliation: terminating the exact leader
	// does not establish that its application or container descendants exited.
	DescendantsUnknown bool `json:"descendants_unknown"`
}

// Runner is replaceable for deterministic backend contract tests.
type Runner interface {
	Run(context.Context, Command) (Result, error)
}

// OSRunner executes a command as the current user with a clean environment.
type OSRunner struct{}

const defaultOutput = 1 << 20
const maximumOutput = 16 << 20

// Run executes without shell expansion. Output is drained even after its limit.
// Timeout defaults to two minutes for cancellable commands. Unsafe commands
// have no implicit timeout and are never killed on context cancellation. Once
// started, their natural completion outcome is returned; callers check pending
// cancellation at the next safe boundary, preserving committed evidence. Safe
// cancellation signals only the exact leader through pidfd, never a reused PID
// or process-group number. Descendants require separate backend reconciliation.
// If safe signalling is unavailable, Run waits for natural completion so callers
// retain their locks instead of releasing a still-active environment.
func (OSRunner) Run(ctx context.Context, spec Command) (Result, error) {
	result := Result{ExitCode: -1}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if spec.Program == "" || strings.ContainsRune(spec.Program, 0) {
		return result, errors.New("invalid executable")
	}
	if spec.Dir != "" && !filepath.IsAbs(spec.Dir) {
		return result, errors.New("working directory must be absolute")
	}
	for _, a := range spec.Args {
		if strings.ContainsRune(a, 0) {
			return result, errors.New("invalid argument")
		}
	}
	if spec.MaxOutput < 0 || spec.MaxOutput > maximumOutput {
		return result, errors.New("invalid output limit")
	}
	if spec.MaxOutput == 0 {
		spec.MaxOutput = defaultOutput
	}
	if spec.Timeout < 0 {
		return result, errors.New("invalid timeout")
	}
	if spec.SafeCancel && spec.Timeout == 0 {
		spec.Timeout = 2 * time.Minute
	}
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}
	env, err := CleanEnvironment(os.Environ(), spec.Env)
	if err != nil {
		return result, err
	}
	executable, err := exec.LookPath(spec.Program)
	if err != nil {
		return result, errors.New("executable unavailable")
	}
	cmd := exec.Command(executable, spec.Args...)
	cmd.Env, cmd.Dir = env, spec.Dir
	// Isolate the child from the caller's terminal process group. Cancellation
	// uses an exact pidfd target, never a negative-PID process-group signal.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Prevent a descendant that inherits output descriptors from blocking forever
	// after its parent has exited. This closes pipes, not descendant processes.
	cmd.WaitDelay = 2 * time.Second
	out, errout := &limitedBuffer{limit: spec.MaxOutput}, &limitedBuffer{limit: spec.MaxOutput}
	cmd.Stdout, cmd.Stderr = out, errout
	if spec.Output != nil {
		sink := &callbackOutput{callback: spec.Output}
		cmd.Stdout = io.MultiWriter(out, sink)
		cmd.Stderr = io.MultiWriter(errout, sink)
	}
	if err := cmd.Start(); err != nil {
		return result, errors.New("cannot start executable")
	}
	result.Identity, _, err = readIdentity(cmd.Process.Pid)
	if err != nil {
		// Wait has not run yet, so this child cannot have been reaped and its PID
		// cannot be reused. Kill only our unreaped child if identity cannot be
		// established; never leave an untrackable execution running.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return result, errors.New("cannot establish child process identity")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var runErr error
	cancelled := false
	select {
	case runErr = <-done:
	case <-ctx.Done():
		cancelled = true
		if spec.SafeCancel {
			// Exact pidfd signalling avoids the Alive -> kill(-pid) reuse race.
			// Backend descendants may outlive this leader and need reconciliation.
			result.DescendantsUnknown = true
			_ = Signal(result.Identity, syscall.SIGTERM)
			timer := time.NewTimer(500 * time.Millisecond)
			select {
			case runErr = <-done:
				if !timer.Stop() {
					<-timer.C
				}
			case <-timer.C:
				_ = Signal(result.Identity, syscall.SIGKILL)
				runErr = <-done
			}
		} else {
			runErr = <-done
			cancelled = false
		}
	}
	var outTruncated, errTruncated bool
	result.Stdout, outTruncated = out.snapshot()
	result.Stderr, errTruncated = errout.snapshot()
	result.StdoutTruncated = outTruncated
	result.StderrTruncated = errTruncated
	result.Truncated = outTruncated || errTruncated
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if cancelled {
		return result, ctx.Err()
	}
	if runErr != nil {
		// Nonzero exits can mask ErrWaitDelay in os/exec. Conservatively require
		// reconciliation rather than claiming that descendants have exited.
		result.DescendantsUnknown = true
		// Do not return exec errors containing the user's executable, args or output.
		if errors.Is(runErr, exec.ErrWaitDelay) {
			return result, errors.New("output descriptors remained open after process exit")
		}
		return result, fmt.Errorf("command exited with status %d", result.ExitCode)
	}
	return result, nil
}

// CleanEnvironment strips injected runtime/library configuration while preserving
// existing desktop and proxy settings. Explicit overrides cannot restore blocked
// loader variables. No values are included in validation errors.
func CleanEnvironment(base []string, overrides map[string]string) ([]string, error) {
	vals := make(map[string]string)
	for _, item := range base {
		k, v, ok := strings.Cut(item, "=")
		if ok && k != "" && !strings.ContainsRune(k, 0) && !strings.ContainsRune(v, 0) && !blockedEnvironment(k) {
			vals[k] = v
		}
	}
	for k, v := range overrides {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) || blockedEnvironment(k) {
			return nil, errors.New("disallowed environment override")
		}
		vals[k] = v
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(vals))
	for _, k := range keys {
		result = append(result, k+"="+vals[k])
	}
	return result, nil
}

func blockedEnvironment(k string) bool {
	return strings.HasPrefix(k, "LD_") || strings.HasPrefix(k, "DYLD_") || strings.HasPrefix(k, "ELECTRON_") || strings.HasPrefix(k, "NODE_") || k == "PYTHONPATH" || k == "PYTHONHOME" || k == "GCONV_PATH" || k == "GTK_PATH" || k == "GTK_EXE_PREFIX" || k == "GTK_DATA_PREFIX" || k == "QT_PLUGIN_PATH" || k == "QT_QPA_PLATFORM_PLUGIN_PATH" || k == "BASH_ENV" || k == "ENV" || strings.HasPrefix(k, "BASH_FUNC_") || k == "SHELLOPTS" || k == "BASHOPTS" || k == "CDPATH" || k == "PYTHONSTARTUP" || k == "PYTHONINSPECT" || k == "PERL5OPT" || k == "PERL5LIB" || k == "RUBYOPT" || k == "RUBYLIB" || k == "GIO_EXTRA_MODULES" || k == "GI_TYPELIB_PATH" || k == "GDK_PIXBUF_MODULE_FILE"
}

type limitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	available := b.limit - b.buf.Len()
	if available < len(p) {
		p = p[:available]
		b.truncated = true
	}
	_, _ = b.buf.Write(p)
	return n, nil
}
func (b *limitedBuffer) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String(), b.truncated
}

// Identity combines the Linux boot ID and /proc start ticks with the PID.
// A zero identity is never considered alive.
type Identity struct {
	PID        int    `json:"pid"`
	StartTicks uint64 `json:"start_ticks"`
	BootID     string `json:"boot_id"`
}

// Capture reads Linux process identity without invoking an external command.
func Capture(pid int) (Identity, error) {
	id, state, err := readIdentity(pid)
	if err != nil {
		return Identity{}, err
	}
	if state == "Z" || state == "X" {
		return Identity{}, errors.New("process is not running")
	}
	return id, nil
}

// readIdentity also accepts an unreaped child that already exited. This lets Run
// retain an identity for fast commands without treating normal exit as failure.
func readIdentity(pid int) (Identity, string, error) {
	if pid <= 0 {
		return Identity{}, "", errors.New("invalid process ID")
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return Identity{}, "", errors.New("process unavailable")
	}
	// comm is enclosed in parentheses and may itself contain spaces or ')'.
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return Identity{}, "", errors.New("invalid process identity")
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 20 {
		return Identity{}, "", errors.New("process is not running")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return Identity{}, "", errors.New("invalid process start time")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return Identity{}, "", errors.New("boot identity unavailable")
	}
	return Identity{PID: pid, StartTicks: ticks, BootID: strings.TrimSpace(string(boot))}, fields[0], nil
}

// Alive checks the full identity, never PID alone.
func Alive(id Identity) bool {
	if id.PID <= 0 || id.StartTicks == 0 || id.BootID == "" {
		return false
	}
	current, err := Capture(id.PID)
	return err == nil && current == id
}

// Signal atomically targets a Linux pidfd after verifying the full identity.
// It fails closed on kernels lacking pidfd support, rather than falling back to
// a PID-only kill that could race with PID reuse. This API never signals groups.
func Signal(id Identity, sig syscall.Signal) error {
	if id.PID <= 0 {
		return errors.New("invalid process identity")
	}
	// ZaH's supported target is linux/amd64; these syscall numbers are Linux's
	// generic pidfd assignments. No third-party syscall package is required.
	fd, _, errno := syscall.Syscall(434, uintptr(id.PID), 0, 0)
	if errno != 0 {
		return errors.New("safe process signalling unavailable")
	}
	defer syscall.Close(int(fd))
	if !Alive(id) {
		return errors.New("process identity changed")
	}
	_, _, errno = syscall.Syscall6(424, fd, uintptr(sig), 0, 0, 0, 0)
	if errno != 0 {
		return errors.New("process signal failed")
	}
	return nil
}

// Serialize the two pipe readers before publishing progress.
type callbackOutput struct {
	mu       sync.Mutex
	callback func(string)
}

func (w *callbackOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.callback(string(p))
	return len(p), nil
}
