package process

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func validStopFile(filename, nonce string) bool {
	f, e := os.OpenFile(filename, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return false
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() != 32 || st.Mode().Perm() != 0600 {
		return false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys.Uid != uint32(os.Geteuid()) || sys.Nlink != 1 {
		return false
	}
	b, e := io.ReadAll(io.LimitReader(f, 33))
	return e == nil && string(b) == nonce
}

// Do not scan the host process list or signal groups. A direct child cannot have
// its PID reused while it is our unreaped child. Killing parents causes their
// surviving descendants to be adopted by this dedicated subreaper; subsequent
// iterations handle them the same way, until wait4 proves ECHILD.
// This must run in a dedicated helper with no unrelated children and no other
// waiter. Every signal additionally uses a full identity and Linux pidfd.
func stopDirectChildren() error {
	tasks, e := os.ReadDir("/proc/self/task")
	if e != nil {
		return errors.New("cannot enumerate supervisor tasks")
	}
	seen := map[int]bool{}
	for _, task := range tasks {
		b, e := os.ReadFile(filepath.Join("/proc/self/task", task.Name(), "children"))
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return errors.New("cannot enumerate owned children")
		}
		for _, value := range strings.Fields(string(b)) {
			pid, e := strconv.Atoi(value)
			if e != nil || pid <= 0 {
				return errors.New("invalid owned child identity")
			}
			if seen[pid] {
				continue
			}
			seen[pid] = true
			raw, e := os.ReadFile(filepath.Join("/proc", value, "stat"))
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				return errors.New("owned child unavailable")
			}
			end := strings.LastIndexByte(string(raw), ')')
			if end < 0 {
				return errors.New("invalid child stat")
			}
			fields := strings.Fields(string(raw[end+1:]))
			if len(fields) < 2 {
				return errors.New("incomplete child stat")
			}
			parent, e := strconv.Atoi(fields[1])
			if e != nil || parent != os.Getpid() {
				return errors.New("child ownership changed")
			}
			if fields[0] == "Z" || fields[0] == "X" {
				continue
			}
			id, e := Capture(pid)
			if e != nil {
				continue
			} // exited; the sole wait4 loop reaps it
			if e = Signal(id, syscall.SIGKILL); e != nil && Alive(id) {
				return errors.New("cannot safely terminate owned child")
			}
		}
	}
	return nil
}
