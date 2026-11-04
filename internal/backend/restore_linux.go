package backend

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ZZH1124Prime/ZaH/internal/host"
	"os"
	"path/filepath"
	"strings"
)

// RebuildLinux follows Distrobox's documented load -> create --image -> enter
// workflow. Only a fresh target, or this persisted attempt's exact token, is used.
// persist runs before initialization so failed verification leaves owned evidence.
func (a Adapter) RebuildLinux(ctx context.Context, r InstallRequest, archivePath, digest, token, stage string, persist func(Evidence) error) (Evidence, error) {
	ev := Evidence{State: "restore_pending"}
	if e := validate(r); e != nil {
		return ev, e
	}
	if r.Recipe.Platform != "linux" || len(token) != 32 || !digestPattern.MatchString(token+token) {
		return ev, errors.New("valid Linux restoration attempt required")
	}
	if a.Runner == nil && os.Geteuid() == 0 {
		return ev, errors.New("ordinary user required")
	}
	if _, e := host.InspectBinding(r.PrivateHome, false); e != nil {
		return ev, e
	}
	archive, e := InspectDockerArchive(ctx, archivePath, MaxSnapshotBytes)
	if e != nil {
		return ev, e
	}
	if archive.SHA256 != digest {
		return ev, errors.New("restoration archive changed")
	}
	// Existing targets are never adopted merely because their name matches.
	id, e := a.restoreLinuxTarget(ctx, r, archive.ImageID, token)
	if e != nil {
		return ev, e
	}
	if id == "" {
		root, e := a.localDockerRoot(ctx)
		if e != nil {
			return ev, e
		}
		if _, e = a.localDockerDriver(ctx); e != nil {
			return ev, e
		}
		capacity, e := checkCapacity("restore_docker_storage", root, uint64(archive.Size)*2+(256<<20), false)
		if e != nil {
			return ev, e
		}
		if e = capacityTotals([]CapacityCheck{capacity}); e != nil {
			return ev, e
		}
		plan, e := a.PlanLinuxRestore(ctx, r, archivePath, digest)
		if e != nil {
			return ev, e
		}
		if _, e = a.LoadLinuxSnapshot(ctx, plan, stage); e != nil {
			return ev, e
		}
		if e = ctx.Err(); e != nil {
			return ev, e
		}
		flags := "--label org.zah.instance=" + r.InstanceID + " --label org.zah.restore=" + token + " --pull=never"
		for _, b := range r.Bindings {
			if e = host.VerifyBinding(b.Host); e != nil {
				return ev, e
			}
			mode := "rw"
			if b.Host.ReadOnly {
				mode = "ro"
			}
			flags += " --volume \"" + b.Host.Path + ":" + b.Target + ":" + mode + "\""
		}
		c := dbx("create", "--yes", "--name", name(r.InstanceID), "--image", archive.ImageID, "--home", r.PrivateHome, "--no-entry", "--additional-flags", flags)
		c.Env["DBX_CONTAINER_ALWAYS_PULL"] = "0"
		c.SafeCancel = false
		c.Timeout = 0
		if e = persist(Evidence{State: "restore_creating"}); e != nil {
			return ev, e
		}
		_, createErr := a.runner().Run(ctx, c)
		// Reconcile a lost command response using the already persisted nonce.
		id, e = a.restoreLinuxTarget(context.WithoutCancel(ctx), r, archive.ImageID, token)
		if e != nil {
			return ev, e
		}
		if id == "" {
			if e = persist(Evidence{State: "restore_pending"}); e != nil {
				return ev, e
			}
			if createErr != nil {
				return ev, createErr
			}
			return ev, errors.New("created restoration target not found")
		}
	}
	ev = Evidence{ResourceID: id, State: "restore_created"}
	if e = persist(ev); e != nil {
		return ev, e
	}
	if e = ctx.Err(); e != nil {
		return ev, e
	}
	proof, e := a.VerifyManualLinuxRestore(ctx, r, id, archive.ImageID)
	if e != nil {
		return ev, e
	}
	// Verification may initialize/start this container; leave it stopped until Run.
	if _, e = a.Stop(context.WithoutCancel(ctx), r, id); e != nil {
		return ev, e
	}
	proof.State = "entry_verified"
	return proof, nil
}
func (a Adapter) restoreLinuxTarget(ctx context.Context, r InstallRequest, image, token string) (string, error) {
	out, e := a.runner().Run(ctx, command("docker", "container", "ls", "--all", "--filter", "name=^/"+name(r.InstanceID)+"$", "--no-trunc", "--format", "{{.ID}}"))
	if e != nil {
		return "", e
	}
	if out.Truncated {
		return "", errors.New("incomplete restoration target inventory")
	}
	ids := strings.Fields(out.Stdout)
	if len(ids) == 0 {
		return "", nil
	}
	if len(ids) != 1 || !digestPattern.MatchString(ids[0]) {
		return "", errors.New("ambiguous restoration target")
	}
	out, e = a.runner().Run(ctx, command("docker", "container", "inspect", ids[0]))
	if e != nil {
		return "", e
	}
	if out.Truncated {
		return "", errors.New("incomplete restoration identity")
	}
	var rows []struct {
		ID     string `json:"Id"`
		Name   string
		Image  string
		Config struct{ Labels map[string]string }
		Mounts []struct {
			Type, Source, Destination string
			RW                        bool
		}
	}
	if json.Unmarshal([]byte(out.Stdout), &rows) != nil || len(rows) != 1 {
		return "", errors.New("invalid restoration identity")
	}
	v := rows[0]
	if v.ID != ids[0] || v.Name != "/"+name(r.InstanceID) || v.Image != image || v.Config.Labels["org.zah.instance"] != r.InstanceID || v.Config.Labels["org.zah.restore"] != token {
		return "", errors.New("target belongs to another creation attempt; refusing adoption")
	}
	home := 0
	for _, m := range v.Mounts {
		if m.Destination == r.PrivateHome {
			home++
			if m.Type != "bind" || m.Source != filepath.Clean(r.PrivateHome) || !m.RW {
				return "", errors.New("restoration HOME mount differs")
			}
		}
	}
	if home != 1 {
		return "", errors.New("restoration HOME mount missing")
	}
	return v.ID, nil
}
