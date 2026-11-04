package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/infra"
	"github.com/ZZH1124Prime/ZaH/internal/storage"
)

func logFixture(t *testing.T) (*storage.Store, string, string) {
	t.Helper()
	store, root := jobFixture(t)
	dir := filepath.Join(root, "logs")
	if e := infra.EnsurePrivateDir(dir); e != nil {
		t.Fatal(e)
	}
	return store, root, dir
}
func logJob(t *testing.T, s *storage.Store, state domain.JobState, pid int, start string) string {
	t.Helper()
	id := domain.NewID()
	data, e := json.Marshal(domain.Job{ID: id, State: state, WorkerPID: pid, WorkerStart: start})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Put(context.Background(), domain.Record{Kind: "job", ID: id, Data: data}); e != nil {
		t.Fatal(e)
	}
	return id
}
func logFile(t *testing.T, dir, id, content string, age time.Duration) string {
	t.Helper()
	name := filepath.Join(dir, "worker-"+id+".log")
	if e := os.WriteFile(name, []byte(content), 0600); e != nil {
		t.Fatal(e)
	}
	stamp := time.Now().Add(-age)
	if e := os.Chtimes(name, stamp, stamp); e != nil {
		t.Fatal(e)
	}
	return name
}
func readLog(t *testing.T, name string) []byte {
	t.Helper()
	data, e := os.ReadFile(name)
	if e != nil {
		t.Fatal(e)
	}
	return data
}

func TestWorkerLogBoundsWritesAndRetainsLifetimeLock(t *testing.T) {
	_, runtimeDir, logDir := logFixture(t)
	id := domain.NewID()
	policy := LogPolicy{512, 1024, 2}
	writer, e := OpenWorkerLog(logDir, runtimeDir, id, policy)
	if e != nil {
		t.Fatal(e)
	}
	payload := bytes.Repeat([]byte("x"), 1<<20)
	for i := 0; i < 3; i++ {
		n, e := writer.Write(payload)
		if e != nil || n != len(payload) {
			t.Fatalf("write %d %v", n, e)
		}
	}
	data := readLog(t, filepath.Join(logDir, "worker-"+id+".log"))
	if len(data) != 512 || !bytes.Contains(data, []byte("log size limit")) {
		t.Fatalf("size or truncation evidence %d", len(data))
	}
	if second, e := OpenWorkerLog(logDir, runtimeDir, id, policy); !errors.Is(e, infra.ErrLocked) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("live writer lock missing: %v", e)
	}
	if e = writer.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = writer.Write([]byte("later")); !errors.Is(e, os.ErrClosed) {
		t.Fatal(e)
	}
	reopened, e := OpenWorkerLog(logDir, runtimeDir, id, policy)
	if e != nil {
		t.Fatal(e)
	}
	reopened.Write([]byte("does not grow"))
	reopened.Close()
	if len(readLog(t, filepath.Join(logDir, "worker-"+id+".log"))) != 512 {
		t.Fatal("reopen bypassed bound")
	}
}

func TestWorkerLogReservesFutureGrowthAndPreservesUnknownFiles(t *testing.T) {
	store, runtimeDir, logDir := logFixture(t)
	policy := LogPolicy{128, 256, 2}
	first, e := OpenWorkerLog(logDir, runtimeDir, domain.NewID(), policy)
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close()
	second, e := OpenWorkerLog(logDir, runtimeDir, domain.NewID(), policy)
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	if third, e := OpenWorkerLog(logDir, runtimeDir, domain.NewID(), policy); !errors.Is(e, ErrLogBudget) {
		if third != nil {
			third.Close()
		}
		t.Fatalf("empty active logs were not reserved: %v", e)
	}
	result, e := RetainWorkerLogs(context.Background(), store, runtimeDir, logDir, policy)
	if e != nil {
		t.Fatal(e)
	}
	if result.Removed != 0 || !result.BudgetExceeded || result.ReservedBytes != 256 {
		t.Fatalf("unsafe retention: %+v", result)
	}
}

func TestRetentionRemovesOldestProvenInactiveLogsButKeepsJobEvidence(t *testing.T) {
	store, runtimeDir, logDir := logFixture(t)
	policy := LogPolicy{128, 256, 2}
	ids := []string{}
	names := []string{}
	for i := 0; i < 3; i++ {
		id := logJob(t, store, domain.Succeeded, 1<<30, "old-boot:42")
		ids = append(ids, id)
		names = append(names, logFile(t, logDir, id, "completed", time.Duration(3-i)*time.Hour))
	}
	result, e := RetainWorkerLogs(context.Background(), store, runtimeDir, logDir, policy)
	if e != nil {
		t.Fatal(e)
	}
	if result.Removed != 2 || result.Preserved != 1 || result.BudgetExceeded {
		t.Fatalf("retention result %+v", result)
	}
	for _, name := range names[:2] {
		if _, e = os.Lstat(name); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("old log retained")
		}
	}
	if string(readLog(t, names[2])) != "completed" {
		t.Fatal("newest log changed")
	}
	for _, id := range ids {
		if _, e = store.Get(context.Background(), "job", id); e != nil {
			t.Fatal("durable evidence deleted", e)
		}
	}
}

func TestRetentionCapsOnlyInactiveLogTails(t *testing.T) {
	store, runtimeDir, logDir := logFixture(t)
	policy := LogPolicy{128, 8192, 20}
	live, e := Identity(os.Getpid())
	if e != nil {
		t.Fatal(e)
	}
	content := strings.Repeat("old evidence ", 80) + "LATEST"
	deadID := logJob(t, store, domain.Failed, 1<<30, "old-boot:42")
	dead := logFile(t, logDir, deadID, content, time.Hour)
	preserved := []string{logFile(t, logDir, logJob(t, store, domain.Succeeded, os.Getpid(), live), content, 0), logFile(t, logDir, logJob(t, store, domain.Running, 1<<30, "old-boot:42"), content, 0), logFile(t, logDir, logJob(t, store, domain.WaitingUser, 0, ""), content, 0), logFile(t, logDir, domain.NewID(), content, 0)}
	result, e := RetainWorkerLogs(context.Background(), store, runtimeDir, logDir, policy)
	if e != nil {
		t.Fatal(e)
	}
	if result.Compacted != 1 || result.Removed != 0 {
		t.Fatalf("retention %+v", result)
	}
	data := readLog(t, dead)
	if len(data) != 128 || !bytes.HasSuffix(data, []byte("LATEST")) {
		t.Fatal("inactive tail not kept")
	}
	for _, name := range preserved {
		if string(readLog(t, name)) != content {
			t.Fatal("changed live, uncertain or unregistered log")
		}
	}
}

func TestRetentionRefusesSymbolicAndHardLinks(t *testing.T) {
	store, runtimeDir, logDir := logFixture(t)
	policy := LogPolicy{128, 256, 2}
	target := filepath.Join(runtimeDir, "outside-log-scope")
	content := strings.Repeat("private", 100)
	if e := os.WriteFile(target, []byte(content), 0600); e != nil {
		t.Fatal(e)
	}
	symlinkID := logJob(t, store, domain.Cancelled, 0, "")
	link := filepath.Join(logDir, "worker-"+symlinkID+".log")
	if e := os.Symlink(target, link); e != nil {
		t.Fatal(e)
	}
	hardID := logJob(t, store, domain.Cancelled, 0, "")
	hard := filepath.Join(logDir, "worker-"+hardID+".log")
	if e := os.Link(target, hard); e != nil {
		t.Fatal(e)
	}
	result, e := RetainWorkerLogs(context.Background(), store, runtimeDir, logDir, policy)
	if e != nil {
		t.Fatal(e)
	}
	if result.Removed != 0 || result.Compacted != 0 || !result.BudgetExceeded {
		t.Fatalf("unsafe link handling %+v", result)
	}
	if string(readLog(t, target)) != content {
		t.Fatal("external target modified")
	}
	if _, e = os.Lstat(link); e != nil {
		t.Fatal("link deleted")
	}
	if _, e = os.Lstat(hard); e != nil {
		t.Fatal("hardlink deleted")
	}
	if w, e := OpenWorkerLog(logDir, runtimeDir, symlinkID, policy); e == nil {
		w.Close()
		t.Fatal("followed symlink")
	}
}

func TestRetentionHonorsWriterAndJobLocks(t *testing.T) {
	store, runtimeDir, logDir := logFixture(t)
	policy := LogPolicy{128, 128, 1}
	id := logJob(t, store, domain.Cancelled, 0, "")
	writer, e := OpenWorkerLog(logDir, runtimeDir, id, policy)
	if e != nil {
		t.Fatal(e)
	}
	writer.Write([]byte("keep"))
	result, e := RetainWorkerLogs(context.Background(), store, runtimeDir, logDir, policy)
	if e != nil || result.Removed != 0 || !result.BudgetExceeded {
		t.Fatalf("writer lock ignored %+v %v", result, e)
	}
	writer.Close()
	jobLock, e := infra.AcquireLock(filepath.Join(runtimeDir, "job-"+id+".lock"))
	if e != nil {
		t.Fatal(e)
	}
	result, e = RetainWorkerLogs(context.Background(), store, runtimeDir, logDir, policy)
	if e != nil || result.Removed != 0 {
		t.Fatalf("job lock ignored %+v %v", result, e)
	}
	jobLock.Close()
	result, e = RetainWorkerLogs(context.Background(), store, runtimeDir, logDir, policy)
	if e != nil || result.Removed != 1 || result.BudgetExceeded {
		t.Fatalf("inactive log not released %+v %v", result, e)
	}
}

func TestRetentionCancellationDoesNotModifyLogs(t *testing.T) {
	store, runtimeDir, logDir := logFixture(t)
	id := logJob(t, store, domain.Cancelled, 0, "")
	name := logFile(t, logDir, id, "keep", 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := RetainWorkerLogs(ctx, store, runtimeDir, logDir, LogPolicy{128, 128, 1}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if string(readLog(t, name)) != "keep" {
		t.Fatal("cancelled pass modified log")
	}
}
