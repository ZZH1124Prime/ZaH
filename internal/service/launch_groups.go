package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/infra"
)

func sharesGroup(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// claimLaunch combines live locks with durable occupancy. A launcher returning
// does not prove its application stopped; only a later backend proof clears the
// durable lease. An interrupted launch stays blocked even if the environment is
// momentarily stopped, because an orphan launcher may still be about to enter it.
func (s *Service) claimLaunch(ctx context.Context, v domain.AppInstance, env *domain.Environment) (func(), error) {
	if v.State != "ready" {
		return nil, fmt.Errorf("%w: instance is not ready", domain.ErrConflict)
	}
	rel, e := get[domain.AppRelease](ctx, s.Store, "release", v.ReleaseID)
	if e != nil {
		return nil, e
	}
	family := sha256.Sum256([]byte(rel.FamilyID))
	names := []string{"launch-instance-" + v.ID, "launch-family-" + hex.EncodeToString(family[:])}
	for _, group := range v.ConflictGroups {
		sum := sha256.Sum256([]byte(group))
		names = append(names, "launch-group-"+hex.EncodeToString(sum[:]))
	}
	claimCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	live, e := infra.AcquireLocks(claimCtx, s.Paths.Runtime, names...)
	if e != nil {
		if ctx.Err() == nil && errors.Is(e, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: this application or a conflicting group already has an active launch; stop it before switching versions", domain.ErrConflict)
		}
		return nil, e
	}
	failed := true
	defer func() {
		if failed {
			live.Close()
		}
	}()
	claimsCtx, claimsCancel := context.WithTimeout(ctx, 5*time.Second)
	defer claimsCancel()
	lock, e := infra.AcquireLocks(claimsCtx, s.Paths.Runtime, "launch-claims")
	if e != nil {
		return nil, e
	}
	defer lock.Close()
	barrier, e := infra.AcquireLock(filepath.Join(s.Paths.Runtime, "stop-all.lock"))
	if e != nil {
		return nil, fmt.Errorf("%w: stopping managed applications", domain.ErrConflict)
	}
	defer barrier.Close()

	rows, e := s.Store.List(ctx, "instance")
	if e != nil {
		return nil, e
	}
	for _, row := range rows {
		var old domain.AppInstance
		if e = json.Unmarshal(row.Data, &old); e != nil {
			return nil, e
		}
		oldRel, e := get[domain.AppRelease](ctx, s.Store, "release", old.ReleaseID)
		if e != nil {
			return nil, e
		}
		if old.ID != v.ID && oldRel.FamilyID != rel.FamilyID && !sharesGroup(v.ConflictGroups, old.ConflictGroups) {
			continue
		}
		prior, e := get[domain.Environment](ctx, s.Store, "environment", old.EnvironmentID)
		if e != nil {
			return nil, e
		}
		if !prior.LaunchLease {
			continue
		}
		if old.State == "launching" || old.State == "needs_inspection" {
			return nil, fmt.Errorf("%w: instance %s has an unresolved prior launch; inspect surviving upstream processes before reconciliation", domain.ErrConflict, old.ID)
		}
		r, _, e := s.request(ctx, old)
		if e != nil {
			return nil, e
		}
		if _, e = s.Backend.ConfirmStopped(ctx, r, prior.Identity); e != nil {
			return nil, fmt.Errorf("%w: instance %s still occupies its launch group: %v", domain.ErrConflict, old.ID, e)
		}
		prior.LaunchLease = false
		if e = s.Store.Put(ctx, record("environment", prior.ID, prior)); e != nil {
			return nil, e
		}
		if prior.ID == env.ID {
			*env = prior
		}
	}
	env.LaunchLease = true
	if e = s.Store.Put(ctx, record("environment", env.ID, *env)); e != nil {
		return nil, e
	}
	failed = false
	return func() { _ = live.Close() }, nil
}
