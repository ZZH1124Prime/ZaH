package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/host"
)

// DesktopPlan renders a reviewable shortcut without touching host applications.
// Its Exec target is this installed Go binary and a stable verified instance ID.
func (s *Service) DesktopPlan(ctx context.Context, id string) (map[string]string, error) {
	v, err := get[domain.AppInstance](ctx, s.Store, "instance", id)
	if err != nil {
		return nil, err
	}
	if v.State != "ready" {
		return nil, errors.New("desktop shortcut requires a verified installed entry")
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, err
	}
	content, err := host.DesktopEntry(executable, v.ID, v.Name)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(filepath.Dir(s.Paths.Data), "applications")
	return map[string]string{"path": filepath.Join(dir, "zah-"+v.ID+".desktop"), "applications_dir": dir, "executable": executable, "instance_id": v.ID, "display_name": v.Name, "content": string(content)}, nil
}
