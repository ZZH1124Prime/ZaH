package bootstrap

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/infra"
	"github.com/ZZH1124Prime/ZaH/internal/jobs"
	"github.com/ZZH1124Prime/ZaH/internal/service"
	"github.com/ZZH1124Prime/ZaH/internal/storage"
	"github.com/ZZH1124Prime/ZaH/internal/transport"
)

func manager(args []string, stdout, stderr io.Writer) int {
	readyFD := -1
	if len(args) > 0 {
		if len(args) != 2 || args[0] != "--ready-fd" {
			fmt.Fprintln(stderr, "manager [--ready-fd N]")
			return 2
		}
		var e error
		readyFD, e = strconv.Atoi(args[1])
		if e != nil || readyFD < 3 {
			return 2
		}
	}
	paths, e := infra.Resolve()
	if e != nil {
		return failure(stderr, e)
	}
	if e = paths.Ensure(); e != nil {
		return failure(stderr, e)
	}
	l, e := infra.AcquireLock(filepath.Join(paths.Runtime, "manager.lock"))
	if e != nil {
		return failure(stderr, e)
	}
	defer l.Close()
	s, e := service.Open(paths)
	if e != nil {
		return failure(stderr, e)
	}
	defer s.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if e = jobs.Recover(ctx, s.Store); e != nil {
		return failure(stderr, e)
	}
	// Retention failures must not prevent opening the management UI. Admission
	// of a new worker independently enforces the same finite log budget.
	_, _ = jobs.RetainWorkerLogs(ctx, s.Store, paths.Runtime, filepath.Join(paths.State, "logs"), jobs.DefaultLogPolicy())
	var mu sync.Mutex
	clients := 0
	handlers := 0
	closing := false
	last := time.Now()
	active := map[string]bool{}
	handler := func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
		mu.Lock()
		if closing {
			mu.Unlock()
			return nil, errors.New("manager is shutting down; reconnect")
		}
		last = time.Now()
		handlers++
		mu.Unlock()
		defer func() { mu.Lock(); handlers--; last = time.Now(); mu.Unlock() }()
		result, err := s.Handle(ctx, method, raw)
		return result, publicError(err)
	}
	server := transport.Server{Handler: handler, OnConnect: func() { mu.Lock(); clients++; last = time.Now(); mu.Unlock() }, OnDisconnect: func() { mu.Lock(); clients--; last = time.Now(); mu.Unlock() }, OnReady: func() {
		msg := map[string]any{"ready": true, "protocol": 1, "socket": filepath.Join(paths.Runtime, "manager.sock")}
		if readyFD >= 3 {
			f := os.NewFile(uintptr(readyFD), "ready")
			if f != nil {
				json.NewEncoder(f).Encode(msg)
				f.Close()
			}
		} else {
			json.NewEncoder(stdout).Encode(msg)
		}
	}}
	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				mu.Lock()
				_ = jobs.Recover(ctx, s.Store)
				rows, err := s.Store.List(ctx, "job")
				if err != nil {
					mu.Unlock()
					continue
				}
				busy := false
				modifier := false
				occupied := map[string]bool{}
				for _, r := range rows {
					var j domain.Job
					if json.Unmarshal(r.Data, &j) != nil {
						continue
					}
					if active[j.ID] || j.State == domain.Running || j.State == domain.CancelRequested || j.State == domain.Queued {
						busy = true
					}
					if j.State == domain.Running || j.State == domain.CancelRequested {
						var p domain.Plan
						pr, e := s.Store.Get(ctx, "plan", j.PlanID)
						if e == nil {
							_ = json.Unmarshal(pr.Data, &p)
						}
						for _, envID := range p.EnvironmentIDs {
							occupied[envID] = true
						}
						if p.Operation != "instance.launch" {
							modifier = true
						}
					}
				}
				for _, r := range rows {
					var j domain.Job
					if json.Unmarshal(r.Data, &j) != nil {
						continue
					}
					if j.State != domain.Queued || active[j.ID] || modifier {
						continue
					}
					pr, e := s.Store.Get(ctx, "plan", j.PlanID)
					if e != nil {
						continue
					}
					var p domain.Plan
					if json.Unmarshal(pr.Data, &p) != nil {
						continue
					}
					blocked := false
					for _, envID := range p.EnvironmentIDs {
						if occupied[envID] {
							blocked = true
						}
					}
					if blocked {
						continue
					}
					active[j.ID] = true
					last = time.Now()
					modifier = true
					if e := startWorker(paths, s.Store, j.ID, func() { mu.Lock(); delete(active, j.ID); last = time.Now(); mu.Unlock() }); e != nil {
						delete(active, j.ID)
						_ = s.Store.TransitionJob(ctx, j.ID, domain.Queued, domain.Interrupted, "worker could not start")
					}
				}
				if !busy && len(active) == 0 && clients == 0 && handlers == 0 && time.Since(last) > 30*time.Second {
					closing = true
					mu.Unlock()
					cancel()
					return
				}
				mu.Unlock()
			}
		}
	}()
	if e = server.Serve(ctx, filepath.Join(paths.Runtime, "manager.sock")); e != nil && !errors.Is(e, context.Canceled) {
		return failure(stderr, e)
	}
	return 0
}

func startWorker(paths infra.Paths, store *storage.Store, id string, onExit func()) error {
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	logDir := filepath.Join(paths.State, "logs")
	if e = infra.EnsurePrivateDir(logDir); e != nil {
		return e
	}
	retentionCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, e = jobs.RetainWorkerLogs(retentionCtx, store, paths.Runtime, logDir, jobs.DefaultLogPolicy())
	cancel()
	if e != nil {
		return e
	}
	cmd := exec.Command(exe, "worker", id)
	cmd.Stdin = nil
	// The worker opens its own bounded file after validating the job ID. Raw
	// runtime/app stderr never becomes an unbounded or credential-bearing log.
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if e = cmd.Start(); e != nil {
		return e
	}
	go func() {
		_ = cmd.Wait()
		onExit()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = jobs.RetainWorkerLogs(cleanupCtx, store, paths.Runtime, logDir, jobs.DefaultLogPolicy())
	}()
	return nil
}

func worker(args []string, stderr io.Writer) (code int) {
	if len(args) != 1 {
		return failure(stderr, errors.New("worker requires one durable job ID"))
	}
	paths, e := infra.Resolve()
	if e != nil {
		return failure(stderr, e)
	}
	s, e := service.Open(paths)
	if e != nil {
		return failure(stderr, e)
	}
	defer s.Close()
	log, e := jobs.OpenWorkerLog(filepath.Join(paths.State, "logs"), paths.Runtime, args[0], jobs.DefaultLogPolicy())
	if e != nil {
		_ = s.Store.TransitionJob(context.Background(), args[0], domain.Queued, domain.Interrupted, "worker log admission failed; inspect private log retention budget")
		return failure(stderr, e)
	}
	defer log.Close()
	s.LaunchDiagnostics = log
	defer func() {
		if recover() != nil {
			_, _ = io.WriteString(log, "worker panic; execution outcome requires inspection in durable job records\n")
			code = 1
		}
	}()
	_, _ = io.WriteString(log, "worker started; detailed plan and evidence remain in SQLite\n")
	// The worker intentionally ignores manager lifetime. SIGTERM is not forwarded
	// to package writers; durable job.cancel controls safe operation boundaries.
	signal.Ignore(syscall.SIGHUP)
	e = jobs.Run(context.Background(), s.Store, paths.Runtime, args[0], s)
	if e != nil {
		// Fixed summaries intentionally exclude error text, argv, output, paths,
		// account names and tokens. Structured failure evidence stays in the DB.
		_, _ = io.WriteString(log, "worker ended with an error or required interaction; inspect durable job state\n")
		return failure(stderr, e)
	}
	_, _ = io.WriteString(log, "worker completed; inspect durable result and capability evidence\n")
	return 0
}

// ensureManager starts the same executable and waits for its actual ready frame.
// Existing managers are contacted first; no backend is prepared on startup.
func ensureManager(ctx context.Context, paths infra.Paths) error {
	socket := filepath.Join(paths.Runtime, "manager.sock")
	var hello any
	if e := transport.Call(ctx, socket, "system.info", struct{}{}, &hello); e == nil {
		return nil
	}
	if e := paths.Ensure(); e != nil {
		return e
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	r, w, e := os.Pipe()
	if e != nil {
		return e
	}
	defer r.Close()
	cmd := exec.Command(exe, "manager", "--ready-fd", "3")
	cmd.ExtraFiles = []*os.File{w}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// No inherited output pipes: a parent CLI exiting cannot block the manager.
	cmd.Stdout = nil
	cmd.Stderr = nil
	if e = cmd.Start(); e != nil {
		w.Close()
		return e
	}
	w.Close()
	go func() { _ = cmd.Wait() }()
	ready := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 1024), 4096)
		if scanner.Scan() {
			var v struct {
				Ready bool `json:"ready"`
			}
			if e := json.Unmarshal(scanner.Bytes(), &v); e == nil && v.Ready {
				ready <- nil
				return
			}
		}
		ready <- errors.New("manager did not report ready")
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case e = <-ready:
		if e == nil {
			return nil
		}
	}
	// A concurrent CLI may have won the single-instance lock. Resolve that race
	// through a real handshake, never by assuming that a socket file means ready.
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("manager unavailable: %w", ctx.Err())
		case <-ticker.C:
			if e = transport.Call(ctx, socket, "system.info", struct{}{}, &hello); e == nil {
				return nil
			}
		}
	}
}
func failure(w io.Writer, e error) int {
	msg := e.Error()
	if len(msg) > 4096 {
		msg = msg[:4096]
	}
	fmt.Fprintln(w, msg)
	return 1
}

func publicError(err error) error {
	if err == nil {
		return nil
	}
	code := -32010
	message := err.Error()
	switch {
	case errors.Is(err, domain.ErrConfigurationRejected):
		code = -32014
	case strings.HasPrefix(message, "unknown method:"):
		code = -32601
	case errors.Is(err, domain.ErrInvalid):
		code = -32602
	case errors.Is(err, domain.ErrNotFound):
		code = -32004
		message = "requested record not found"
	case errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrReferenced), errors.Is(err, infra.ErrLocked):
		code = -32009
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	if len(message) > 512 {
		message = "operation failed; inspect task status"
	}
	return &transport.Error{Code: code, Message: message}
}
