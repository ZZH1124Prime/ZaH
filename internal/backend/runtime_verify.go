package backend

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ZZH1124Prime/ZaH/internal/host"
)

// verifyRuntimeBindings checks the actual Docker configuration, then observes
// the UID selected by Distrobox for ordinary commands. Container init itself may
// run as root; that is not accepted as proof of the application user's identity.
func (a Adapter) verifyRuntimeBindings(ctx context.Context, r InstallRequest, id string) error {
	if !digestPattern.MatchString(id) {
		return errors.New("managed container identity required")
	}
	home, err := host.InspectBinding(r.PrivateHome, false)
	if err != nil {
		return err
	}
	if home.Path != r.PrivateHome {
		return errors.New("private HOME resolves differently from reviewed path")
	}
	res, err := a.runner().Run(ctx, command("docker", "container", "inspect", id))
	if err != nil {
		return err
	}
	if res.Truncated {
		return errors.New("runtime configuration exceeds limit")
	}
	var rows []struct {
		ID     string `json:"Id"`
		Name   string
		Config struct {
			Labels map[string]string
			Env    []string
		}
		Mounts []struct {
			Type        string
			Source      string
			Destination string
			RW          bool
		}
	}
	if json.Unmarshal([]byte(res.Stdout), &rows) != nil || len(rows) != 1 {
		return errors.New("invalid runtime configuration response")
	}
	v := rows[0]
	if v.ID != id || v.Name != "/"+name(r.InstanceID) || v.Config.Labels["org.zah.instance"] != r.InstanceID {
		return errors.New("runtime ownership changed")
	}
	homes := 0
	for _, entry := range v.Config.Env {
		if strings.HasPrefix(entry, "HOME=") {
			homes++
			if strings.TrimPrefix(entry, "HOME=") != r.PrivateHome {
				return errors.New("container HOME differs from reviewed private HOME")
			}
		}
	}
	if homes != 1 {
		return errors.New("container HOME configuration missing or ambiguous")
	}
	expected := map[string]struct {
		source string
		rw     bool
	}{r.PrivateHome: {source: r.PrivateHome, rw: true}}
	for _, binding := range r.Bindings {
		if err := host.VerifyBinding(binding.Host); err != nil {
			return err
		}
		expected[binding.Target] = struct {
			source string
			rw     bool
		}{binding.Host.Path, !binding.Host.ReadOnly}
	}
	seen := map[string]bool{}
	for _, mount := range v.Mounts {
		want, ok := expected[mount.Destination]
		if !ok {
			continue
		}
		if seen[mount.Destination] || mount.Type != "bind" || filepath.Clean(mount.Source) != want.source || mount.Source != filepath.Clean(mount.Source) || mount.RW != want.rw {
			return errors.New("actual HOME/file binding differs from reviewed mapping")
		}
		seen[mount.Destination] = true
	}
	if len(seen) != len(expected) {
		return errors.New("reviewed HOME or host binding is missing from container")
	}
	identity, err := a.runner().Run(ctx, enter(r, "id", "-u"))
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(strings.TrimSpace(identity.Stdout))
	if identity.Truncated || err != nil || uid <= 0 || uid != os.Geteuid() {
		return errors.New("Distrobox application user is not the current ordinary host user")
	}
	return nil
}
