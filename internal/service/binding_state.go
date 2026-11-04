package service

import (
	"context"
	"encoding/json"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"path/filepath"
	"strings"
)

// Call only after the backend has checked every configured mapping. Registration
// is historical evidence; each launch still validates the live mapping again.
func (s *Service) bindingsVerified(ctx context.Context, instance string) error {
	rows, e := s.Store.List(ctx, "binding")
	if e != nil {
		return e
	}
	for _, row := range rows {
		var b domain.FileBinding
		if e = json.Unmarshal(row.Data, &b); e != nil {
			return e
		}
		if b.InstanceID != instance {
			continue
		}
		b.State = "verified"
		if e = s.Store.Put(ctx, record("binding", b.ID, b)); e != nil {
			return e
		}
	}
	return nil
}

func validBindingTarget(platform, target string) bool {
	if strings.ContainsAny(target, "\x00\r\n") {
		return false
	}
	if platform == "windows" {
		return len(target) == 2 && target[0] >= 'D' && target[0] <= 'Y' && target[1] == ':'
	}
	return platform == "linux" && strings.HasPrefix(target, "/mnt/zah/") && filepath.Clean(target) == target
}
