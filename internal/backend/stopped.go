package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ConfirmStopped proves the current managed container has no running process.
// The caller must hold its environment lock through any dependent operation.
// Missing/unknown state, daemon failure and disappeared resources are not proof.
func (a Adapter) ConfirmStopped(ctx context.Context, r InstallRequest, identity string) (Evidence, error) {
	ev := Evidence{ResourceID: identity, State: "unknown"}
	if err := validate(r); err != nil {
		return ev, err
	}
	if r.Recipe.Platform != "linux" {
		if r.Windows == nil || r.Windows.Control == nil {
			return ev, fmt.Errorf("%w: exact Wine control registration required", ErrUnsupported)
		}
		if err := a.verifyBottlesControlTarget(ctx, r, identity); err != nil {
			return ev, err
		}
		if _, err := a.wineControl(ctx, r, r.Windows.ControlRoot, "wait", r.Windows.Control); err != nil {
			return ev, err
		}
		ev.State = "stopped"
		return ev, nil
	}
	if !digestPattern.MatchString(identity) {
		return ev, errors.New("full managed container ID required")
	}
	out, err := a.runner().Run(ctx, command("docker", "container", "inspect", identity))
	if err != nil {
		return ev, err
	}
	if out.Truncated {
		return ev, errors.New("container inspection exceeds limit")
	}
	var rows []struct {
		ID     string `json:"Id"`
		Name   string
		Config struct{ Labels map[string]string }
		State  *struct {
			Status                            string
			Running, Paused, Restarting, Dead *bool
			Pid                               *int
		}
	}
	if json.Unmarshal([]byte(out.Stdout), &rows) != nil || len(rows) != 1 {
		return ev, errors.New("invalid container inspection")
	}
	v := rows[0]
	if v.ID != identity || v.Name != "/"+name(r.InstanceID) || v.Config.Labels["org.zah.instance"] != r.InstanceID {
		return ev, errors.New("container ownership changed")
	}
	st := v.State
	if st == nil || st.Running == nil || st.Paused == nil || st.Restarting == nil || st.Dead == nil || st.Pid == nil {
		return ev, errors.New("container stopped state is incomplete")
	}
	if *st.Running || *st.Paused || *st.Restarting || *st.Dead || *st.Pid != 0 || (st.Status != "exited" && st.Status != "created") {
		return ev, errors.New("managed container is not proven stopped")
	}
	ev.State = "stopped"
	return ev, nil
}

// VerifyAbsent verifies both the managed name and a previously registered ID.
// A failed daemon request never means absent. The name filter is deliberately
// conservative: even a substring collision blocks deletion of private data.
func (a Adapter) VerifyAbsent(ctx context.Context, instanceID, platform, expectedResourceID string) (Evidence, error) {
	ev := Evidence{ResourceID: expectedResourceID, State: "unknown"}
	if !idPattern.MatchString(instanceID) {
		return ev, errors.New("invalid instance ID")
	}
	if platform != "linux" && platform != "distrobox-docker" {
		return ev, fmt.Errorf("%w: no verified absence contract for this backend", ErrUnsupported)
	}
	if expectedResourceID != "" && !digestPattern.MatchString(expectedResourceID) {
		return ev, errors.New("full expected container ID required")
	}
	filters := []string{"name=" + name(instanceID)}
	if expectedResourceID != "" {
		filters = append(filters, "id="+expectedResourceID)
	}
	for _, filter := range filters {
		out, err := a.runner().Run(ctx, command("docker", "container", "ls", "--all", "--no-trunc", "--quiet", "--filter", filter))
		if err != nil {
			return ev, err
		}
		if out.Truncated || strings.TrimSpace(out.Stdout) != "" {
			return ev, errors.New("container absence not proven; matching resource or incomplete evidence")
		}
	}
	ev.State = "absent"
	return ev, nil
}
