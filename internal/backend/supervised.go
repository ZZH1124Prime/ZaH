package backend

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ZZH1124Prime/ZaH/internal/infra"
)

// SupervisedCompletion reports process-tree completion, never window-close or
// minimization intent. The caller decides when to stop the dedicated environment.
type SupervisedCompletion struct {
	Protocol      string `json:"protocol"`
	Nonce         string `json:"nonce"`
	Completed     bool   `json:"completed"`
	EntryExitCode int    `json:"entry_exit_code"`
	Reaped        int    `json:"reaped"`
}

func validSupervisionNonce(nonce string) bool {
	if len(nonce) != 32 || strings.ToLower(nonce) != nonce {
		return false
	}
	_, e := hex.DecodeString(nonce)
	return e == nil
}
func verifySupervisorPath(r InstallRequest, path, nonce string) error {
	if !validSupervisionNonce(nonce) || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Dir(path) != filepath.Join(r.PrivateHome, ".zah-runtime") || unsafePath(path) {
		return errors.New("supervisor must be a direct managed runtime file with a fresh 128-bit nonce")
	}
	if e := infra.ValidatePrivateDir(filepath.Dir(path)); e != nil {
		return e
	}
	st, e := os.Lstat(path)
	if e != nil {
		return e
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Mode().Perm()&0100 == 0 || sys.Uid != uint32(os.Geteuid()) || sys.Nlink != 1 {
		return errors.New("supervisor must be a private owned executable regular file")
	}
	binary, e := elf.Open(path)
	if e != nil {
		return errors.New("supervisor is not a Linux ELF executable")
	}
	defer binary.Close()
	if binary.Class != elf.ELFCLASS64 || binary.Machine != elf.EM_X86_64 || (binary.Type != elf.ET_EXEC && binary.Type != elf.ET_DYN) {
		return errors.New("supervisor must be a native amd64 executable")
	}
	for _, program := range binary.Progs {
		if program.Type == elf.PT_INTERP {
			return errors.New("supervisor must be statically linked for the target container")
		}
	}
	marker := filepath.Join(filepath.Dir(path), "run-"+nonce+".started")
	if _, e = os.Lstat(marker); !os.IsNotExist(e) {
		return errors.New("supervisor start marker already exists or cannot be checked")
	}
	return nil
}
func parseSupervisedCompletion(raw, nonce string) (SupervisedCompletion, error) {
	var proof SupervisedCompletion
	if len(raw) > 4096 || !validSupervisionNonce(nonce) {
		return proof, errors.New("invalid supervisor proof size or nonce")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	first, e := decoder.Token()
	if e != nil || first != json.Delim('{') {
		return proof, errors.New("supervisor completion object required")
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		key, e := decoder.Token()
		if e != nil {
			return proof, errors.New("invalid supervisor proof")
		}
		name, ok := key.(string)
		if !ok {
			return proof, errors.New("invalid supervisor field")
		}
		switch name {
		case "protocol", "nonce", "completed", "entry_exit_code", "reaped":
		default:
			return proof, errors.New("unknown supervisor field")
		}
		if _, ok = fields[name]; ok {
			return proof, errors.New("duplicate supervisor field")
		}
		var value json.RawMessage
		if e = decoder.Decode(&value); e != nil {
			return proof, errors.New("invalid supervisor field value")
		}
		fields[name] = value
	}
	if _, e = decoder.Token(); e != nil {
		return proof, errors.New("incomplete supervisor proof")
	}
	if e = decoder.Decode(new(any)); e != io.EOF || len(fields) != 5 {
		return proof, errors.New("trailing or missing supervisor proof fields")
	}
	joined, e := json.Marshal(fields)
	if e != nil {
		return proof, e
	}
	if e = json.Unmarshal(joined, &proof); e != nil {
		return proof, errors.New("invalid supervisor completion types")
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return proof, errors.New("null supervisor field")
		}
	}
	if proof.Protocol != "zah.supervise.v1" || proof.Nonce != nonce || !proof.Completed || proof.Reaped < 1 || proof.Reaped > 1<<30 || proof.EntryExitCode < 0 || proof.EntryExitCode > 255 {
		return proof, errors.New("supervisor did not prove this application's process tree exited")
	}
	return proof, nil
}

// LaunchSupervised only accepts the service's verified copy of the same static
// ZaH executable. The service owns binary-copy digest verification and lifetime
// gates. A successful application_exited result is the only automatic-stop cue;
// ordinary launcher exit, window events and cancellation never substitute for it.
func (a Adapter) LaunchSupervised(ctx context.Context, r InstallRequest, id, helperPath, nonce string) (Evidence, error) {
	ev := Evidence{ResourceID: id, State: "launch_not_started"}
	if e := validate(r); e != nil {
		return ev, e
	}
	if a.Runner == nil && os.Geteuid() == 0 {
		return ev, errors.New("ordinary user required")
	}
	if r.Recipe.Platform == "windows" {
		return a.launchWindowsControlled(ctx, r, id, helperPath, nonce)
	}
	if r.Recipe.Platform != "linux" {
		return ev, ErrUnsupported
	}
	if e := verifySupervisorPath(r, helperPath, nonce); e != nil {
		return ev, e
	}
	if _, e := a.Inspect(ctx, r, id); e != nil {
		return ev, e
	}
	if e := a.verifyRuntimeBindings(ctx, r, id); e != nil {
		return ev, e
	}
	marker := filepath.Join(filepath.Dir(helperPath), "run-"+nonce+".started")
	argv, e := a.selectedLinuxCommand(ctx, r, id)
	if e != nil {
		if errors.Is(e, ErrLinuxEntryChanged) {
			// No supervisor/application command has been issued on this path.
			ev.State = "entry_unavailable"
		}
		return ev, e
	}
	c := enter(r, append([]string{helperPath, "supervise", nonce, marker}, argv...)...)
	c.SafeCancel = false
	c.Timeout = 0
	ev.State = "application_state_unknown"
	result, e := a.runner().Run(ctx, c)
	ev.Result = result
	if e != nil {
		return ev, e
	}
	// Diagnostic stderr truncation does not corrupt the separate stdout proof.
	// Legacy combined-only truncation remains ambiguous and fails closed.
	if result.StdoutTruncated || (result.Truncated && !result.StderrTruncated) || result.DescendantsUnknown || result.ExitCode != 0 {
		return ev, errors.New("supervisor execution or completion output is uncertain")
	}
	proof, e := parseSupervisedCompletion(result.Stdout, nonce)
	if e != nil {
		return ev, e
	}
	ev.State = "application_exited"
	ev.Lifecycle = &proof
	return ev, nil
}

// The helper executes on the host and owns only this invocation of Flatpak.
// No helper needs to be injected into or granted extra access by the sandbox.
func (a Adapter) launchWindowsControlled(ctx context.Context, r InstallRequest, id, helperPath, nonce string) (Evidence, error) {
	ev := Evidence{ResourceID: id, State: "launch_not_started"}
	if r.Windows == nil || r.Windows.Control == nil || !r.Windows.ControlledLaunch {
		return ev, fmtUnsupportedLaunch()
	}
	if e := verifySupervisorPath(r, helperPath, nonce); e != nil {
		return ev, e
	}
	if _, e := a.InspectBottles(ctx, r, id); e != nil {
		return ev, e
	}
	flatpak, e := exec.LookPath("flatpak")
	if e != nil {
		if a.Runner == nil {
			return ev, e
		}
		flatpak = "/usr/bin/flatpak"
	} // mock runner never executes this path
	flatpak, e = filepath.Abs(flatpak)
	if e != nil {
		return ev, e
	}
	marker := filepath.Join(filepath.Dir(helperPath), "run-"+nonce+".started")
	args := []string{"supervise-controlled", nonce, marker, flatpak}
	args = append(args, "run", "--command=python3", "com.usebottles.bottles", "-I", "-c", controlledBottlesLaunchHelper, name(r.InstanceID), r.Windows.ProgramName)
	c := command(helperPath, args...)
	c.SafeCancel = false
	c.Timeout = 0
	ev.State = "application_state_unknown"
	out, e := a.runner().Run(ctx, c)
	ev.Result = out
	if e != nil {
		return ev, e
	}
	if out.StdoutTruncated || (out.Truncated && !out.StderrTruncated) || out.DescendantsUnknown || out.ExitCode != 0 {
		return ev, errors.New("controlled launcher completion uncertain")
	}
	proof, e := parseSupervisedCompletion(out.Stdout, nonce)
	if e != nil {
		return ev, e
	}
	ev.State = "application_exited"
	ev.Lifecycle = &proof
	return ev, nil
}
func fmtUnsupportedLaunch() error {
	return errors.New("safe Windows Run/Stop requires verified Wine control and direct Wine mode (winebridge=false); re-register the Bottle after selecting that mode")
}
