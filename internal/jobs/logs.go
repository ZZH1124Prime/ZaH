package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/infra"
	"github.com/ZZH1124Prime/ZaH/internal/storage"
)

// LogPolicy bounds each worker log and the reserved capacity of the log directory.
// Every existing log reserves at least MaxFileBytes, including a currently empty
// active log, so later writes cannot exceed the admitted aggregate budget.
type LogPolicy struct {
	MaxFileBytes, MaxTotalBytes int64
	MaxFiles                    int
}

// DefaultLogPolicy reserves bounded diagnostics without imposing application-data quotas.
func DefaultLogPolicy() LogPolicy {
	return LogPolicy{MaxFileBytes: 64 << 10, MaxTotalBytes: 16 << 20, MaxFiles: 256}
}
func (p LogPolicy) validate() error {
	if p.MaxFileBytes < 128 || p.MaxFileBytes > 16<<20 || p.MaxTotalBytes < p.MaxFileBytes || p.MaxFiles < 1 || p.MaxFiles > 10000 {
		return errors.New("invalid worker log policy")
	}
	return nil
}

var ErrLogBudget = errors.New("worker log budget exhausted; inactive logs require safe retention")

const logMarker = "\n[zah: log size limit reached; durable job evidence is retained]\n"

// WorkerLog independently owns its file and lifetime lock; it survives loss of
// the manager. Write discards bytes beyond the configured limit without making
// external application operations fail merely because their diagnostic is full.
type WorkerLog struct {
	mu          sync.Mutex
	file        *os.File
	lock        *infra.Lock
	limit, size int64
}

// Write reports accepted input length, discarding excess bytes once its file budget is full.
func (w *WorkerLog) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, os.ErrClosed
	}
	original := len(data)
	remaining := w.limit - w.size
	if remaining <= 0 {
		return original, nil
	}
	write := data
	if int64(len(data)) > remaining {
		if remaining >= int64(len(logMarker)) {
			write = append(append([]byte(nil), data[:int(remaining)-len(logMarker)]...), []byte(logMarker)...)
		} else {
			write = []byte(logMarker[:int(remaining)])
		}
	}
	n, err := w.file.Write(write)
	w.size += int64(n)
	if err != nil {
		return n, err
	}
	if n != len(write) {
		return n, io.ErrShortWrite
	}
	return original, nil
}

// Close flushes the diagnostic and releases the worker-owned lifetime lock.
func (w *WorkerLog) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := errors.Join(w.file.Sync(), w.file.Close(), w.lock.Close())
	w.file = nil
	return err
}

// OpenWorkerLog admits a log under the total quota before opening it. It does
// not trim an existing log: only the inactive-job retention path may do that.
func OpenWorkerLog(logDir, runtimeDir, id string, policy LogPolicy) (*WorkerLog, error) {
	if !validID(id) {
		return nil, errors.New("invalid worker log ID")
	}
	if err := policy.validate(); err != nil {
		return nil, err
	}
	if err := infra.EnsurePrivateDir(logDir); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	guard, err := infra.AcquireLocks(ctx, runtimeDir, "logs")
	if err != nil {
		return nil, err
	}
	defer guard.Close()
	lifetime, err := infra.AcquireLock(filepath.Join(runtimeDir, "worker-log-"+id+".lock"))
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*WorkerLog, error) { lifetime.Close(); return nil, e }
	entries, err := scanWorkerLogs(logDir)
	if err != nil {
		return fail(err)
	}
	count, reserved := logUsage(entries, policy)
	exists := false
	for _, entry := range entries {
		if entry.id == id {
			exists = true
			if !entry.safe || entry.size > policy.MaxFileBytes {
				return fail(ErrLogBudget)
			}
		}
	}
	if !exists {
		count++
		reserved += policy.MaxFileBytes
	}
	if count > policy.MaxFiles || reserved > policy.MaxTotalBytes {
		return fail(ErrLogBudget)
	}
	filename := filepath.Join(logDir, "worker-"+id+".log")
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return fail(err)
	}
	info, err := f.Stat()
	if err != nil || !safeLog(info) {
		f.Close()
		if err == nil {
			err = errors.New("unsafe worker log")
		}
		return fail(err)
	}
	if info.Size() > policy.MaxFileBytes {
		f.Close()
		return fail(ErrLogBudget)
	}
	return &WorkerLog{file: f, lock: lifetime, limit: policy.MaxFileBytes, size: info.Size()}, nil
}

type logEntry struct {
	id, path string
	size     int64
	modified time.Time
	info     os.FileInfo
	safe     bool
}

func safeLog(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1 && info.Mode().Perm()&0077 == 0
}
func scanWorkerLogs(dir string) ([]logEntry, error) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	entries := make([]logEntry, 0, len(items))
	for _, item := range items {
		name := item.Name()
		if !strings.HasPrefix(name, "worker-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, "worker-"), ".log")
		if !validID(id) {
			continue
		}
		filename := filepath.Join(dir, name)
		info, e := os.Lstat(filename)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return nil, e
		}
		entries = append(entries, logEntry{id: id, path: filename, size: info.Size(), modified: info.ModTime(), info: info, safe: safeLog(info)})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].modified.Equal(entries[j].modified) {
			return entries[i].id < entries[j].id
		}
		return entries[i].modified.Before(entries[j].modified)
	})
	return entries, nil
}
func logUsage(entries []logEntry, p LogPolicy) (int, int64) {
	reserved := int64(0)
	for _, entry := range entries {
		size := entry.size
		if size < p.MaxFileBytes {
			size = p.MaxFileBytes
		}
		reserved += size
	}
	return len(entries), reserved
}

// LogRetention reports only this pass. Unsafe/unknown/live records are preserved
// even if that means the configured budget cannot yet be met.
type LogRetention struct {
	Scanned, Removed, Compacted, Preserved int
	RetainedBytes, ReservedBytes           int64
	BudgetExceeded                         bool
}

// RetainWorkerLogs rotates logs by job: it caps tails of proven-inactive logs and
// removes the oldest eligible job logs until one future log can be admitted.
// Audit plans, results, references and events in SQLite are never removed.
func RetainWorkerLogs(ctx context.Context, store *storage.Store, runtimeDir, logDir string, policy LogPolicy) (LogRetention, error) {
	var report LogRetention
	if err := policy.validate(); err != nil {
		return report, err
	}
	if err := infra.EnsurePrivateDir(logDir); err != nil {
		return report, err
	}
	guard, err := infra.AcquireLocks(ctx, runtimeDir, "logs")
	if err != nil {
		return report, err
	}
	defer guard.Close()
	entries, err := scanWorkerLogs(logDir)
	if err != nil {
		return report, err
	}
	report.Scanned = len(entries)
	for i := range entries {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		entry := &entries[i]
		if !entry.safe || entry.size <= policy.MaxFileBytes {
			continue
		}
		err = withInactiveLog(ctx, store, runtimeDir, *entry, func(f *os.File) error {
			tailSize := policy.MaxFileBytes - int64(len(logMarker))
			tail := make([]byte, int(tailSize))
			if _, e := f.ReadAt(tail, entry.size-tailSize); e != nil {
				return e
			}
			if e := f.Truncate(0); e != nil {
				return e
			}
			if _, e := f.Seek(0, io.SeekStart); e != nil {
				return e
			}
			if _, e := f.Write([]byte(logMarker)); e != nil {
				return e
			}
			if _, e := f.Write(tail); e != nil {
				return e
			}
			return f.Sync()
		})
		if err == nil {
			entry.size = policy.MaxFileBytes
			report.Compacted++
		} else if !errors.Is(err, errLogPreserved) {
			return report, err
		}
	}
	count, reserved := logUsage(entries, policy)
	for i := range entries {
		entry := &entries[i]
		if err = ctx.Err(); err != nil {
			return report, err
		}
		if count < policy.MaxFiles && reserved+policy.MaxFileBytes <= policy.MaxTotalBytes {
			break
		}
		if !entry.safe {
			continue
		}
		err = withInactiveLog(ctx, store, runtimeDir, *entry, func(f *os.File) error {
			current, e := os.Lstat(entry.path)
			if e != nil {
				return e
			}
			opened, e := f.Stat()
			if e != nil {
				return e
			}
			if !os.SameFile(current, opened) {
				return errLogPreserved
			}
			return os.Remove(entry.path)
		})
		if err == nil {
			size := entry.size
			if size < policy.MaxFileBytes {
				size = policy.MaxFileBytes
			}
			reserved -= size
			count--
			entry.size = 0
			entry.path = ""
			report.Removed++
		} else if !errors.Is(err, errLogPreserved) {
			return report, err
		}
	}
	report.Preserved = report.Scanned - report.Removed
	report.ReservedBytes = reserved
	report.BudgetExceeded = count >= policy.MaxFiles || reserved+policy.MaxFileBytes > policy.MaxTotalBytes
	for _, entry := range entries {
		if entry.path != "" {
			report.RetainedBytes += entry.size
		}
	}
	return report, nil
}

var errLogPreserved = errors.New("worker log must be preserved")

func withInactiveLog(ctx context.Context, store *storage.Store, runtimeDir string, entry logEntry, action func(*os.File) error) error {
	// Log lifetime lock precedes job lock, matching worker bootstrap order.
	logLock, err := infra.AcquireLock(filepath.Join(runtimeDir, "worker-log-"+entry.id+".lock"))
	if err != nil {
		if errors.Is(err, infra.ErrLocked) {
			return errLogPreserved
		}
		return err
	}
	defer logLock.Close()
	jobLock, err := infra.AcquireLock(filepath.Join(runtimeDir, "job-"+entry.id+".lock"))
	if err != nil {
		if errors.Is(err, infra.ErrLocked) {
			return errLogPreserved
		}
		return err
	}
	defer jobLock.Close()
	rec, err := store.Get(ctx, "job", entry.id)
	if err != nil {
		return errLogPreserved
	}
	var job domain.Job
	if json.Unmarshal(rec.Data, &job) != nil || job.ID != entry.id {
		return errLogPreserved
	}
	switch job.State {
	case domain.Succeeded, domain.Failed, domain.Cancelled, domain.Interrupted, domain.WaitingUser:
	default:
		return errLogPreserved
	}
	if job.WorkerPID == 0 && job.WorkerStart == "" {
		if job.State != domain.Cancelled {
			return errLogPreserved
		}
	} else {
		if job.WorkerPID <= 0 || job.WorkerStart == "" {
			return errLogPreserved
		}
		identity, e := Identity(job.WorkerPID)
		if e == nil {
			if identity == job.WorkerStart {
				return errLogPreserved
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return errLogPreserved
		}
	}
	f, err := os.OpenFile(entry.path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ELOOP) {
			return errLogPreserved
		}
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !safeLog(info) || !os.SameFile(entry.info, info) || info.Size() != entry.size {
		return errLogPreserved
	}
	if err = action(f); err != nil {
		return fmt.Errorf("retain worker log: %w", err)
	}
	return nil
}
