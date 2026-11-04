package backend

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

// LaunchLinuxInstallationGUI opens only a fixed native tool inside the verified
// running container. Administrative tools use container sudo, never host sudo.
// It does not install tools, infer installation success, or select an app entry.
func (a Adapter) LaunchLinuxInstallationGUI(ctx context.Context, r InstallRequest, resourceID, action string) (Evidence, error) {
	var tool string
	var args []string
	admin := false
	switch action {
	case "files":
		tool = "/usr/bin/thunar"
		args = []string{r.PrivateHome}
	case "packages":
		tool = "/usr/sbin/synaptic"
		admin = true
	case "install":
		switch r.Recipe.Strategy {
		case "deb":
			tool = "/usr/bin/gdebi-gtk"
			args = []string{filepath.Join(r.PrivateHome, ".zah-install/installer.deb")}
			admin = true
		case "appimage":
			tool = "/usr/bin/thunar"
			args = []string{filepath.Join(r.PrivateHome, ".zah-install")}
		default:
			return Evidence{}, ErrUnsupported
		}
	default:
		return Evidence{}, errors.New("unsupported Linux maintenance action")
	}
	if err := a.linuxInteractiveContext(ctx, r, resourceID); err != nil {
		return Evidence{}, err
	}
	if action == "install" {
		material := filepath.Join(r.PrivateHome, ".zah-install", "installer."+r.Recipe.Strategy)
		if err := verifyFile(ctx, material, r.ArtifactSHA256); err != nil {
			return Evidence{}, err
		}
	}
	if _, err := a.runner().Run(ctx, enter(r, "/usr/bin/test", "-x", tool)); err != nil {
		return Evidence{ResourceID: resourceID, State: "waiting_user"}, fmt.Errorf("%w: container GUI tool %s is unavailable; no dependency was installed", ErrManual, filepath.Base(tool))
	}
	argv := append([]string{tool}, args...)
	if admin {
		argv = append([]string{"/usr/bin/sudo", "-n", "--"}, argv...)
	}
	c := enter(r, argv...)
	result, err := a.runner().Run(ctx, c)
	proof := Evidence{ResourceID: resourceID, State: "maintenance_tool_exited", Result: result}
	if err != nil {
		proof.State = "maintenance_failed"
	}
	return proof, err
}
