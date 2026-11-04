package backend

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/process"
)

func supervisorFixture(t *testing.T) (InstallRequest, string, string, string) {
	t.Helper()
	r := request(t)
	if e := os.Chmod(filepath.Dir(r.PrivateHome), 0700); e != nil {
		t.Fatal(e)
	}
	directory := filepath.Join(r.PrivateHome, ".zah-runtime")
	if e := os.MkdirAll(directory, 0700); e != nil {
		t.Fatal(e)
	}
	// A nonexecuted minimal static ELF header is sufficient to test the adapter's
	// format gate. Service owns copying and digest-verifying the real ZaH binary.
	header := make([]byte, 64)
	copy(header, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(header[16:], 2)
	binary.LittleEndian.PutUint16(header[18:], 62)
	binary.LittleEndian.PutUint32(header[20:], 1)
	binary.LittleEndian.PutUint16(header[52:], 64)
	helper := filepath.Join(directory, "zah")
	if e := os.WriteFile(helper, header, 0700); e != nil {
		t.Fatal(e)
	}
	return r, helper, strings.Repeat("a", 64), strings.Repeat("b", 32)
}
func supervisedReplies(r InstallRequest, id, stdout string) []process.Result {
	info, _ := json.Marshal([]any{map[string]any{"Id": id, "Name": "/zah-" + r.InstanceID, "Config": map[string]any{"Labels": map[string]string{"org.zah.instance": r.InstanceID}, "Env": []string{"HOME=" + r.PrivateHome}}, "Mounts": []any{map[string]any{"Type": "bind", "Source": r.PrivateHome, "Destination": r.PrivateHome, "RW": true}}, "State": map[string]any{"Running": true}}})
	return []process.Result{{Stdout: string(info)}, {Stdout: string(info)}, {Stdout: strconv.Itoa(os.Geteuid())}, {Stdout: stdout, ExitCode: 0}}
}
func TestSupervisedCompletionIsOnlyAutomaticCleanupCue(t *testing.T) {
	r, helper, id, nonce := supervisorFixture(t)
	proof := fmt.Sprintf(`{"protocol":"zah.supervise.v1","nonce":%q,"completed":true,"entry_exit_code":7,"reaped":3}`, nonce)
	f := &scriptRunner{replies: supervisedReplies(r, id, proof)}
	ev, e := (Adapter{f}).LaunchSupervised(context.Background(), r, id, helper, nonce)
	if e != nil || ev.State != "application_exited" || ev.Lifecycle == nil || ev.Lifecycle.EntryExitCode != 7 || ev.Lifecycle.Reaped != 3 {
		t.Fatalf("%+v %v", ev, e)
	}
	cmd := f.calls[len(f.calls)-1]
	want := []string{helper, "supervise", nonce, filepath.Join(filepath.Dir(helper), "run-"+nonce+".started"), entry(r)}
	if !strings.HasSuffix(strings.Join(cmd.Args, "|"), strings.Join(want, "|")) || cmd.SafeCancel || cmd.Timeout != 0 {
		t.Fatal("unreviewed supervisor invocation", cmd)
	}
	for _, c := range f.calls {
		if len(c.Args) > 1 && c.Args[1] == "stop" {
			t.Fatal("adapter bypassed service lifecycle gate")
		}
	}
}
func TestSupervisorNeverTreatsBareLauncherExitAsApplicationExit(t *testing.T) {
	for _, mode := range []string{"empty", "different_nonce", "missing", "duplicate", "extra", "null", "false", "no_children", "negative_exit", "trailing", "truncated", "descendants_unknown", "bad_exit"} {
		t.Run(mode, func(t *testing.T) {
			r, helper, id, nonce := supervisorFixture(t)
			proof := fmt.Sprintf(`{"protocol":"zah.supervise.v1","nonce":%q,"completed":true,"entry_exit_code":0,"reaped":1}`, nonce)
			switch mode {
			case "empty":
				proof = ""
			case "different_nonce":
				proof = strings.Replace(proof, nonce, strings.Repeat("c", 32), 1)
			case "missing":
				proof = strings.Replace(proof, `,"reaped":1`, "", 1)
			case "duplicate":
				proof = strings.Replace(proof, `"reaped":1`, `"reaped":1,"reaped":1`, 1)
			case "extra":
				proof = strings.Replace(proof, `"reaped":1`, `"reaped":1,"extra":true`, 1)
			case "null":
				proof = strings.Replace(proof, `"entry_exit_code":0`, `"entry_exit_code":null`, 1)
			case "false":
				proof = strings.Replace(proof, `"completed":true`, `"completed":false`, 1)
			case "no_children":
				proof = strings.Replace(proof, `"reaped":1`, `"reaped":0`, 1)
			case "negative_exit":
				proof = strings.Replace(proof, `"entry_exit_code":0`, `"entry_exit_code":-1`, 1)
			case "trailing":
				proof += "\n{}"
			}
			replies := supervisedReplies(r, id, proof)
			if mode == "truncated" {
				replies[3].Truncated = true
			}
			if mode == "descendants_unknown" {
				replies[3].DescendantsUnknown = true
			}
			if mode == "bad_exit" {
				replies[3].ExitCode = 1
			}
			ev, e := (Adapter{&scriptRunner{replies: replies}}).LaunchSupervised(context.Background(), r, id, helper, nonce)
			if e == nil || ev.State == "application_exited" {
				t.Fatalf("accepted uncertain application lifetime %+v %v", ev, e)
			}
		})
	}
}
func TestSupervisorRejectsUnmanagedHelperAndReusedNonce(t *testing.T) {
	for _, mode := range []string{"outside", "symlink", "nonelf", "public", "reused_nonce", "nonce"} {
		t.Run(mode, func(t *testing.T) {
			r, helper, id, nonce := supervisorFixture(t)
			switch mode {
			case "outside":
				helper = filepath.Join(t.TempDir(), "helper")
			case "symlink":
				old := helper + ".original"
				_ = os.Rename(helper, old)
				_ = os.Symlink(old, helper)
			case "nonelf":
				_ = os.WriteFile(helper, []byte("#!/bin/sh\nexit 0"), 0700)
			case "public":
				_ = os.Chmod(helper, 0755)
			case "reused_nonce":
				_ = os.WriteFile(filepath.Join(filepath.Dir(helper), "run-"+nonce+".started"), []byte("old"), 0600)
			case "nonce":
				nonce = "not-a-nonce"
			}
			f := &fakeRunner{}
			if _, e := (Adapter{f}).LaunchSupervised(context.Background(), r, id, helper, nonce); e == nil || len(f.calls) != 0 {
				t.Fatal("untrusted helper accepted", e)
			}
		})
	}
}

func TestSupervisedCompletionAcceptsOnlyExplicitStderrTruncation(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		stdout, stderr, combined, accept bool
	}{
		{"stderr_only", false, true, true, true},
		{"stdout_only", true, false, true, false},
		{"stdout_flag_without_summary", true, false, false, false},
		{"both_streams", true, true, true, false},
		{"legacy_ambiguous", false, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, helper, id, nonce := supervisorFixture(t)
			raw := fmt.Sprintf(`{"protocol":"zah.supervise.v1","nonce":%q,"completed":true,"entry_exit_code":0,"reaped":1}`, nonce)
			replies := supervisedReplies(r, id, raw)
			replies[3].StdoutTruncated = tc.stdout
			replies[3].StderrTruncated = tc.stderr
			replies[3].Truncated = tc.combined
			evidence, err := (Adapter{&scriptRunner{replies: replies}}).LaunchSupervised(context.Background(), r, id, helper, nonce)
			if (err == nil) != tc.accept || (evidence.State == "application_exited") != tc.accept {
				t.Fatalf("%+v %v", evidence, err)
			}
		})
	}
}
