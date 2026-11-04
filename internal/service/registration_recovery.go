package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ZZH1124Prime/ZaH/internal/backend"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

// A stale lease may be reconciled only when the latest launch was rejected
// before starting any process. Fresh registration must still prove Wine stopped.
func (s *Service) registrationLaunchRejected(ctx context.Context, v domain.AppInstance, env domain.Environment) bool {
	if env.LaunchToken == "" || env.StopRequested {
		return false
	}
	marker := filepath.Join(v.PrivateData, ".zah-runtime", "run-"+env.LaunchToken+".started")
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		return false
	}
	rows, err := s.Store.List(ctx, "job")
	if err != nil {
		return false
	}
	var latest domain.Job
	for _, row := range rows {
		var j domain.Job
		if json.Unmarshal(row.Data, &j) != nil {
			return false
		}
		p, err := get[domain.Plan](ctx, s.Store, "plan", j.PlanID)
		if err != nil || p.Operation != "instance.launch" || p.InstanceID != v.ID {
			continue
		}
		if latest.ID == "" || j.CreatedAt.After(latest.CreatedAt) {
			latest = j
		}
	}
	if latest.State != domain.Failed || latest.WorkerPID <= 0 {
		return false
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", latest.WorkerPID)); !os.IsNotExist(err) {
		return false
	}
	var proof backend.Evidence
	return json.Unmarshal(latest.Result, &proof) == nil && proof.ResourceID == env.Identity && proof.State == "launch_not_started" && proof.Result.Identity.PID == 0 && !proof.Result.DescendantsUnknown && proof.Lifecycle == nil
}
