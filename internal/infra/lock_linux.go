package infra

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"syscall"
	"time"
)

// ErrLocked indicates another process holds a cooperative lock.
var ErrLocked = errors.New("resource is locked")

// Lock holds an advisory flock. Lock files remain on disk to avoid inode races.
type Lock struct{ file *os.File }

// AcquireLock acquires an exclusive nonblocking lock in a private directory.
func AcquireLock(path string) (*Lock, error) {
	if err := ValidatePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	fail := func(e error) (*Lock, error) { f.Close(); return nil, e }
	var st syscall.Stat_t
	if err = syscall.Fstat(fd, &st); err != nil {
		return fail(err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0 || st.Nlink != 1 {
		return fail(errors.New("unsafe lock file"))
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return fail(ErrLocked)
		}
		return fail(err)
	}
	return &Lock{file: f}, nil
}

// Close releases the lock; it is safe to call more than once, but not concurrently.
func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// LockSet owns locks acquired in stable lexical order.
type LockSet struct{ locks []*Lock }

func (s *LockSet) Close() error {
	var err error
	for i := len(s.locks) - 1; i >= 0; i-- {
		err = errors.Join(err, s.locks[i].Close())
	}
	s.locks = nil
	return err
}

var lockName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// AcquireLocks waits for the entire lock set, releasing partial acquisitions
// between attempts so cancellation never leaves a partial resource claim.
func AcquireLocks(ctx context.Context, dir string, names ...string) (*LockSet, error) {
	unique := map[string]bool{}
	for _, name := range names {
		if !lockName.MatchString(name) {
			return nil, fmt.Errorf("invalid lock name %q", name)
		}
		unique[name] = true
	}
	ordered := make([]string, 0, len(unique))
	for name := range unique {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		set := &LockSet{}
		var err error
		for _, name := range ordered {
			var l *Lock
			l, err = AcquireLock(filepath.Join(dir, name+".lock"))
			if err != nil {
				break
			}
			set.locks = append(set.locks, l)
		}
		if err == nil {
			return set, nil
		}
		set.Close()
		if !errors.Is(err, ErrLocked) {
			return nil, err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
