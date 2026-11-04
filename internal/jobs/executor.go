// Package jobs executes durable plans in isolated workers. A recovered running
// job is never replayed automatically: external side effects need inspection.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/storage"
)

// Executor runs a server-created immutable operation, never arbitrary argv.
type Executor interface {
	Execute(context.Context, domain.Plan) (any, error)
}

// Identity includes boot ID and proc start time, preventing PID reuse confusion.
func Identity(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	end := strings.LastIndex(string(b), ")")
	if end < 0 {
		return "", errors.New("invalid proc stat")
	}
	fields := strings.Fields(string(b)[end+1:])
	if len(fields) < 20 || fields[0] == "Z" {
		return "", errors.New("short proc stat")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(boot)) + ":" + fields[19], nil
}
func Alive(pid int, identity string) bool {
	if pid <= 0 || identity == "" {
		return false
	}
	got, err := Identity(pid)
	return err == nil && got == identity
}

func lock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func unlock(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }

// Run claims exactly one queued job. Locks survive manager disconnection.
func Run(ctx context.Context, store *storage.Store, runtimeDir, id string, executor Executor) error {
	if !validID(id) {
		return errors.New("invalid job ID")
	}
	jl, err := lock(filepath.Join(runtimeDir, "job-"+id+".lock"))
	if err != nil {
		return err
	}
	defer unlock(jl)
	gl, err := lock(filepath.Join(runtimeDir, "modify.lock"))
	if err != nil {
		return err
	}
	globalHeld := true
	defer func() {
		if globalHeld {
			unlock(gl)
		}
	}()
	rec, err := store.Get(ctx, "job", id)
	if err != nil {
		return err
	}
	var job domain.Job
	if err = json.Unmarshal(rec.Data, &job); err != nil {
		return err
	}
	if string(job.State) != "queued" {
		return fmt.Errorf("job is %s; refusing replay", job.State)
	}
	p, err := store.Get(ctx, "plan", job.PlanID)
	if err != nil {
		return err
	}
	var plan domain.Plan
	if err = json.Unmarshal(p.Data, &plan); err != nil {
		return err
	}
	ids := append([]string(nil), plan.EnvironmentIDs...)
	sort.Strings(ids)
	held := []*os.File{}
	defer func() {
		for i := len(held) - 1; i >= 0; i-- {
			unlock(held[i])
		}
	}()
	previous := ""
	for _, envID := range ids {
		if envID == previous {
			continue
		}
		previous = envID
		if !validID(envID) {
			return errors.New("invalid environment ID")
		}
		el, e := lock(filepath.Join(runtimeDir, "environment-"+envID+".lock"))
		if e != nil {
			return fmt.Errorf("environment busy: %w", e)
		}
		held = append(held, el)
	}
	job.WorkerPID = os.Getpid()
	job.WorkerStart, err = Identity(job.WorkerPID)
	if err != nil {
		return err
	}
	if err = store.SetWorker(ctx, job.ID, job.WorkerPID, job.WorkerStart); err != nil {
		return err
	}
	if err = store.TransitionJob(ctx, id, domain.JobState("queued"), domain.JobState("running"), ""); err != nil {
		return err
	}
	if plan.Operation == "instance.launch" {
		unlock(gl)
		globalHeld = false
	}
	runCtx, cancel := WaitCancellation(ctx, store, id)
	defer cancel()
	result, runErr := executor.Execute(context.WithValue(runCtx, jobKey{}, id), plan)
	latest, err := store.Get(ctx, "job", id)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(latest.Data, &job); err != nil {
		return err
	}
	state := domain.JobState("succeeded")
	detail := ""
	if runErr != nil {
		state = domain.JobState("failed")
		detail = runErr.Error()
	}
	if errors.Is(runErr, context.Canceled) {
		state = domain.JobState("cancelled")
	}
	if errors.Is(runErr, backend.ErrManual) {
		state = domain.JobState("waiting_user")
	}
	// A completed non-cancellable operation is still successful, with its result;
	// cancellation does not imply rolling back external changes.
	if result != nil {
		data, _ := json.Marshal(result)
		if err = store.SetJobResult(ctx, id, data); err != nil {
			return err
		}
	}
	// Cancellation can race with completion; re-read then CAS without replaying work.
	for tries := 0; tries < 3; tries++ {
		latest, e := store.Get(ctx, "job", id)
		if e != nil {
			return e
		}
		if e = json.Unmarshal(latest.Data, &job); e != nil {
			return e
		}
		if job.State.Terminal() {
			return nil
		}
		if state == domain.WaitingUser && job.State == domain.CancelRequested {
			state = domain.Cancelled
		}
		err = store.TransitionJob(ctx, id, job.State, state, detail)
		if !errors.Is(err, domain.ErrConflict) {
			break
		}
	}
	if err != nil {
		return err
	}
	return runErr
}
func validID(s string) bool {
	if len(s) < 8 || len(s) > 80 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}
func save(ctx context.Context, s *storage.Store, kind, id string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return s.Put(ctx, domain.Record{ID: id, Kind: kind, Data: b})
}

// Recover marks abandoned work interrupted; it never guesses whether installation
// completed. Queued jobs are safe to claim because workers claim under a lock.
func Recover(ctx context.Context, s *storage.Store) error {
	records, err := s.List(ctx, "job")
	if err != nil {
		return err
	}
	for _, r := range records {
		var j domain.Job
		if err = json.Unmarshal(r.Data, &j); err != nil {
			return err
		}
		switch string(j.State) {
		case "queued", "running", "cancel_requested":
			if j.State == domain.Queued && j.WorkerPID == 0 {
				continue
			}
			if !Alive(j.WorkerPID, j.WorkerStart) {
				if err = s.TransitionJob(ctx, j.ID, j.State, domain.JobState("interrupted"), "worker no longer exists; inspect effects before creating a new plan"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// WaitCancellation gives cancellable handlers a durable cancellation signal.
// Stop the returned watcher after the operation completes.
func WaitCancellation(parent context.Context, s *storage.Store, id string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r, e := s.Get(ctx, "job", id)
				if e != nil {
					continue
				}
				var j domain.Job
				if json.Unmarshal(r.Data, &j) == nil && string(j.State) == "cancel_requested" {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

// WorkerLogName is derived from a validated opaque ID, never a display name.
func WorkerLogName(id string) string {
	return "worker-" + id + "-" + strconv.Itoa(os.Getpid()) + ".log"
}

type jobKey struct{}

// ID returns the durable task identity for step evidence.
func ID(ctx context.Context) string { v, _ := ctx.Value(jobKey{}).(string); return v }
