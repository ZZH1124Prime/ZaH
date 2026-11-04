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

// Legacy builds did not distinguish this exact pre-execution ELF rejection.
// Only the latest launch for this instance may release its lease, and only
// after the caller has independently confirmed the environment stopped.
func (s *Service) rejectedLegacyLaunch(ctx context.Context, v domain.AppInstance, env domain.Environment) bool {
	if env.LaunchToken == "" {
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
		p, e := get[domain.Plan](ctx, s.Store, "plan", j.PlanID)
		if e != nil {
			continue
		}
		var in struct {
			ID string `json:"id"`
		}
		if p.Operation != "instance.launch" || json.Unmarshal(p.Input, &in) != nil || in.ID != v.ID {
			continue
		}
		if latest.ID == "" || j.CreatedAt.After(latest.CreatedAt) {
			latest = j
		}
	}
	if latest.State != domain.Failed || latest.Error != "supervisor must be statically linked for the target container" || latest.WorkerPID <= 0 {
		return false
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", latest.WorkerPID)); !os.IsNotExist(err) {
		return false
	}
	var proof backend.Evidence
	if json.Unmarshal(latest.Result, &proof) != nil {
		return false
	}
	return proof.ResourceID == env.Identity && proof.State == "application_state_unknown" && proof.Result.Identity.PID == 0 && !proof.Result.DescendantsUnknown && proof.Lifecycle == nil
}
