package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ZZH1124Prime/ZaH/internal/host"
	"github.com/ZZH1124Prime/ZaH/internal/installmedia"
)

// InstallMaterial describes a statically inspected archived EXE/ZIP; it is never
// an arbitrary command. Installer launch remains a separate reviewed operation.
type InstallMaterial struct {
	HideWineTray bool   `json:"hide_wine_tray,omitempty"`
	DPI          int    `json:"dpi,omitempty"`
	Format       string `json:"format"`
	Mode         string `json:"mode"`
	Entry        string `json:"entry"`
}

func (a Adapter) bottleCatalog(ctx context.Context) (map[string]json.RawMessage, error) {
	out, e := a.runner().Run(ctx, bottles("--json", "list", "bottles"))
	if e != nil {
		return nil, e
	}
	if out.Truncated {
		return nil, errors.New("Bottles catalog exceeds limit")
	}
	var catalog map[string]json.RawMessage
	e = json.Unmarshal([]byte(out.Stdout), &catalog)
	if e == nil && catalog == nil {
		e = errors.New("missing Bottle inventory")
	}
	return catalog, e
}
func (a Adapter) newBottleRoot(ctx context.Context, r InstallRequest) (host.BindingEvidence, error) {
	catalog, e := a.bottleCatalog(ctx)
	if e != nil {
		return host.BindingEvidence{}, e
	}
	var cfg bottleConfig
	if e = json.Unmarshal(catalog[name(r.InstanceID)], &cfg); e != nil {
		return host.BindingEvidence{}, e
	}
	if cfg.Name != name(r.InstanceID) || cfg.Path != cfg.Name || cfg.Arch != "win64" || cfg.Runner != r.Recipe.Runner || cfg.CreationDate == "" || (len(cfg.CustomPath) > 0 && string(cfg.CustomPath) != "false" && string(cfg.CustomPath) != "null" && string(cfg.CustomPath) != `""`) {
		return host.BindingEvidence{}, errors.New("new Bottle identity differs from requested dedicated environment")
	}
	out, e := a.runner().Run(ctx, bottles("info", "bottles-path"))
	if e != nil {
		return host.BindingEvidence{}, e
	}
	root := strings.TrimSpace(out.Stdout)
	if out.Truncated || !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Base(root) != "bottles" {
		return host.BindingEvidence{}, errors.New("unsupported Bottle root")
	}
	target := filepath.Join(root, cfg.Path)
	canonical, e := filepath.EvalSymlinks(target)
	if e != nil || canonical != target {
		return host.BindingEvidence{}, errors.New("new Bottle path traverses links")
	}
	return host.InspectBinding(target, false)
}
func validateInstallMaterial(r InstallRequest) error {
	m := r.Material
	if m == nil || r.Recipe.Platform != "windows" || (m.Format != "exe" && m.Format != "zip") || (m.Mode != "installer" && m.Mode != "portable") {
		return errors.New("explicit inspected Windows material required")
	}
	if !filepath.IsLocal(m.Entry) || strings.ContainsAny(m.Entry, "\\:\x00\r\n") || filepath.Clean(m.Entry) != m.Entry {
		return errors.New("invalid portable/installer entry")
	}
	if m.DPI != 0 && (m.DPI < 96 || m.DPI > 384) {
		return errors.New("unsupported Wine DPI")
	}
	if m.Format == "exe" && installmedia.EXEName(m.Entry) != nil {
		return errors.New("unexpected single executable entry")
	}
	if len(r.Bindings) != 0 {
		return errors.New("guided Bottle creation does not configure host drive bindings; use the reviewed advanced workflow")
	}
	return nil
}
func (a Adapter) prepareBottles(ctx context.Context, r InstallRequest) (Evidence, error) {
	if e := validateInstallMaterial(r); e != nil {
		return Evidence{}, e
	}
	if e := verifyFile(ctx, r.ArtifactPath, r.ArtifactSHA256); e != nil {
		return Evidence{}, e
	}
	info, e := installmedia.Inspect(ctx, r.ArtifactPath)
	if e != nil {
		return Evidence{}, e
	}
	found := false
	for _, entry := range info.Entries {
		if entry.Path == r.Material.Entry || r.Material.Format == "exe" {
			found = true
		}
	}
	if info.Format != r.Material.Format || !found {
		return Evidence{}, errors.New("archived material or chosen entry changed")
	}
	catalog, e := a.bottleCatalog(ctx)
	if e != nil {
		return Evidence{}, e
	}
	if _, exists := catalog[name(r.InstanceID)]; exists {
		return Evidence{}, errors.New("Bottle already exists; never adopt or repeat creation")
	}
	create := bottles("new", "--bottle-name", name(r.InstanceID), "--environment", "application", "--arch", "win64", "--runner", r.Recipe.Runner)
	create.SafeCancel = false
	create.Timeout = 0
	if _, e = a.runner().Run(ctx, create); e != nil {
		return Evidence{State: "needs_inspection"}, e
	}
	bottle, e := a.newBottleRoot(ctx, r)
	if e != nil {
		return Evidence{State: "needs_inspection"}, e
	}
	// Only the newly created dedicated Bottle is changed. These precise settings
	// are disclosed in the plan; no host/Flatpak permission setting is modified.
	edit := bottles("edit", "-b", name(r.InstanceID), "--params", "winebridge:false,sandbox:false")
	edit.SafeCancel = false
	edit.Timeout = 0
	if _, e = a.runner().Run(ctx, edit); e != nil {
		return Evidence{State: "needs_inspection"}, e
	}
	if e = host.VerifyBinding(bottle); e != nil {
		return Evidence{}, e
	}
	drive := filepath.Join(bottle.Path, "drive_c")
	if canonical, e := filepath.EvalSymlinks(drive); e != nil || canonical != drive {
		return Evidence{}, errors.New("Bottle C drive must be an actual directory")
	}
	target := filepath.Join(drive, "zah-material")
	if r.Material.Format == "zip" {
		e = installmedia.ExtractZIP(ctx, r.ArtifactPath, target)
	} else {
		e = os.Mkdir(target, 0700)
		if e == nil {
			e = copyInstaller(ctx, r.ArtifactPath, filepath.Join(target, r.Material.Entry), r.ArtifactSHA256)
		}
	}
	if e != nil {
		return Evidence{State: "needs_inspection"}, e
	}
	entry := filepath.Join(target, filepath.FromSlash(r.Material.Entry))
	if _, e = regularInside(bottle.Path, entry); e != nil {
		return Evidence{}, e
	}
	if e = host.VerifyBinding(bottle); e != nil {
		return Evidence{}, e
	}
	add := bottles("add", "-b", name(r.InstanceID), "-n", r.Recipe.Entry, "-p", entry)
	add.SafeCancel = false
	add.Timeout = 0
	if _, e = a.runner().Run(ctx, add); e != nil {
		return Evidence{State: "needs_inspection"}, e
	}
	if e = a.prepareWindowsDisplay(ctx, r, bottle.Path); e != nil {
		return Evidence{State: "needs_inspection"}, e
	}
	proof, e := a.VerifyManualBottles(ctx, r, ManualBottlesInput{BottlePath: bottle.Path, ExecutablePath: entry, ProgramName: r.Recipe.Entry})
	if e != nil {
		return proof, e
	}
	if proof.Windows == nil || proof.Windows.Control == nil || !proof.Windows.ControlledLaunch {
		return proof, errors.New("new Bottle lacks exact Run/Stop control; installer was not launched")
	}
	r.Windows = proof.Windows
	// Registry tools may leave Wine services alive; stop only this newly created, identity-pinned Bottle.
	if _, e = a.wineControl(ctx, r, proof.Windows.ControlRoot, "stop", proof.Windows.Control); e != nil {
		return proof, e
	}
	if _, e = a.ConfirmStopped(ctx, r, proof.ResourceID); e != nil {
		return proof, e
	}
	return proof, nil
}
func copyInstaller(ctx context.Context, source, target, digest string) error {
	in, e := os.Open(source)
	if e != nil {
		return e
	}
	defer in.Close()
	out, e := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, e = io.Copy(out, contextReader{ctx, in})
	if e == nil {
		e = out.Sync()
	}
	closed := out.Close()
	if e == nil {
		e = closed
	}
	if e != nil {
		return e
	}
	return verifyFile(ctx, target, digest)
}
