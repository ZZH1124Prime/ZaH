package process

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/infra"
)

// SuperviseProtocol identifies the private helper's readiness and completion
// evidence. It is independent of the manager RPC protocol.
const SuperviseProtocol = "zah.supervise.v1"

// SuperviseStarted proves that the entry was actually started inside the
// environment. Consumers must match the nonce and planned marker location.
type SuperviseStarted struct {
	Protocol string `json:"protocol"`
	Nonce    string `json:"nonce"`
	PID      int    `json:"pid"`
}

// SuperviseCompletion proves that wait4 reported ECHILD after entry startup.
// EntryExitCode is the original entry's exit status (128+signal when signalled);
// completion does not mean the application functioned correctly.
type SuperviseCompletion struct {
	Protocol      string `json:"protocol"`
	Nonce         string `json:"nonce"`
	Completed     bool   `json:"completed"`
	EntryExitCode int    `json:"entry_exit_code"`
	Reaped        int    `json:"reaped"`
}

// Supervise runs only in a dedicated process of the same ZaH binary. Linux
// subreaper state and wait4(-1) are process-wide: NEVER call this in a manager,
// worker, or test process that owns unrelated children. The dedicated CLI role
// uses no other subprocesses. App stdout/stderr go to helper stderr; stdout is
// reserved exclusively for the one completion proof.
//
// The nonce is 32 lowercase hexadecimal characters. startedPath must be
// run-NONCE.started beside the helper executable, in its owned private directory.
// The marker is published only after entry startup. Double-forked descendants
// remain children of this subreaper, and no completion is emitted until ECHILD.
//
// A caught terminating signal invalidates completion, but does not kill any
// process group or abandon live descendants. The helper still waits for their
// real termination and returns an error without a completion proof. SIGKILL or
// fatal termination naturally prevents proof as well. The caller must preserve
// an interrupted/unknown state when no matching proof is received.
func Supervise(nonce, startedPath, entry string, args []string, proof io.Writer) error {
	return supervise(nonce, startedPath, entry, args, proof, false)
}

// SuperviseControlled additionally accepts a nonce-bound stop file beside the
// started marker. It drains only this helper's own child tree before completing.
func SuperviseControlled(nonce, startedPath, entry string, args []string, proof io.Writer) error {
	return supervise(nonce, startedPath, entry, args, proof, true)
}
func supervise(nonce, startedPath, entry string, args []string, proof io.Writer, controlled bool) error {
	if !validNonce(nonce) {
		return errors.New("invalid supervisor nonce")
	}
	if proof == nil {
		return errors.New("supervisor proof writer required")
	}
	if !filepath.IsAbs(entry) || filepath.Clean(entry) != entry || strings.ContainsRune(entry, 0) {
		return errors.New("supervisor entry must be a clean absolute path")
	}
	if len(args) > 256 {
		return errors.New("too many application arguments")
	}
	for _, arg := range args {
		if len(arg) > 65536 || strings.ContainsRune(arg, 0) {
			return errors.New("invalid application argument")
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return errors.New("helper executable identity unavailable")
	}
	directory := filepath.Dir(executable)
	if startedPath != filepath.Join(directory, "run-"+nonce+".started") {
		return errors.New("started marker does not match helper and nonce")
	}
	if err = infra.ValidatePrivateDir(directory); err != nil {
		return fmt.Errorf("helper directory must be owned, private and free of symlinks: %w", err)
	}
	if _, err = os.Lstat(startedPath); !errors.Is(err, os.ErrNotExist) {
		return errors.New("started marker already exists or is unavailable")
	}
	info, err := os.Stat(entry)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return errors.New("application entry must be a regular unprivileged executable")
	}
	env, err := CleanEnvironment(os.Environ(), nil)
	if err != nil {
		return err
	}

	// prctl(PR_SET_CHILD_SUBREAPER, 1) applies to the helper process, not the
	// host or its parent. Orphans from this descendant tree reparent here.
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, 36, 1, 0, 0, 0, 0); errno != 0 {
		return errors.New("Linux child subreaper is unavailable")
	}
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(signals)
	// Leave notifications queued while wait4 blocks. A signal must not be consumed
	// asynchronously and race with the final completion decision.
	select {
	case <-signals:
		return errors.New("supervisor interrupted before entry startup")
	default:
	}
	if controlled {
		// Probe pidfd support before accepting a launch we may later need to drain.
		fd, _, errno := syscall.Syscall(434, uintptr(os.Getpid()), 0, 0)
		if errno != 0 {
			return errors.New("safe launcher cancellation requires Linux pidfd support")
		}
		_, _, errno = syscall.Syscall6(424, fd, 0, 0, 0, 0, 0)
		syscall.Close(int(fd))
		if errno != 0 {
			return errors.New("safe launcher cancellation is unavailable")
		}
	}
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		return errors.New("cannot prepare application input")
	}
	defer stdin.Close()
	cmd := exec.Command(entry, args...)
	cmd.Env = env
	cmd.Stdin = stdin
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		return errors.New("cannot start supervised entry")
	}
	entryPID := cmd.Process.Pid
	// No cmd.Wait goroutine is permitted: wait4 below exclusively owns reaping.
	// Release closes Go's process handle without reaping the underlying child.
	_ = cmd.Process.Release()
	markerErr := publishStarted(startedPath, SuperviseStarted{Protocol: SuperviseProtocol, Nonce: nonce, PID: os.Getpid()})
	completion := SuperviseCompletion{Protocol: SuperviseProtocol, Nonce: nonce, Completed: true, EntryExitCode: -1}
	entryObserved := false
	stopping := false
	var controlErr error
	for {
		if controlled && !stopping {
			stopping = validStopFile(strings.TrimSuffix(startedPath, ".started")+".stop", nonce)
		}
		if stopping {
			if e := stopDirectChildren(); e != nil {
				controlErr = e
			}
		}
		var status syscall.WaitStatus
		flags := 0
		if controlled {
			flags = syscall.WNOHANG
		}
		pid, waitErr := syscall.Wait4(-1, &status, flags, nil)
		if errors.Is(waitErr, syscall.EINTR) {
			continue
		}
		if errors.Is(waitErr, syscall.ECHILD) {
			break
		}
		if waitErr != nil {
			return errors.New("supervisor cannot establish descendant completion")
		}
		if controlled && pid == 0 {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if pid <= 0 {
			return errors.New("supervisor returned an invalid child identity")
		}
		completion.Reaped++
		if pid == entryPID && !entryObserved {
			entryObserved = true
			switch {
			case status.Exited():
				completion.EntryExitCode = status.ExitStatus()
			case status.Signaled():
				completion.EntryExitCode = 128 + int(status.Signal())
			default:
				return errors.New("supervisor entry exit status is unknown")
			}
		}
	}
	if controlErr != nil {
		return controlErr
	}
	if !entryObserved {
		return errors.New("supervisor did not observe entry termination")
	}
	// A queued signal invalidates success. No application output uses this
	// proof stream, and no proof exists before ECHILD has been observed.
	select {
	case <-signals:
		return errors.New("supervisor interrupted; no completion proof emitted")
	default:
	}
	if markerErr != nil {
		return markerErr
	}
	if err = json.NewEncoder(proof).Encode(completion); err != nil {
		return errors.New("cannot write supervisor completion proof")
	}
	return nil
}

func validNonce(nonce string) bool {
	if len(nonce) != 32 {
		return false
	}
	for _, r := range nonce {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func publishStarted(filename string, started SuperviseStarted) error {
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return errors.New("cannot publish supervisor started marker")
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			_ = os.Remove(filename)
		}
	}()
	if err = json.NewEncoder(f).Encode(started); err != nil {
		return errors.New("cannot write supervisor started marker")
	}
	if err = f.Sync(); err != nil {
		return errors.New("cannot sync supervisor started marker")
	}
	if err = f.Close(); err != nil {
		return errors.New("cannot close supervisor started marker")
	}
	dir, err := os.Open(filepath.Dir(filename))
	if err != nil {
		return errors.New("cannot sync supervisor marker directory")
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return errors.New("cannot sync supervisor marker directory")
	}
	ok = true
	return nil
}
