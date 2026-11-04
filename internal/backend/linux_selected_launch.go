package backend

import (
	"context"
	"errors"
	"path/filepath"
)

// ErrLinuxEntryChanged is returned before any application launcher is invoked.
var ErrLinuxEntryChanged = errors.New("selected installed entry changed or disappeared; select it again")

func (a Adapter) selectedLinuxCommand(ctx context.Context, r InstallRequest, id string) ([]string, error) {
	if r.Recipe.PendingEntry {
		return nil, errors.New("select and confirm the installed application first")
	}
	selected := r.Recipe.LinuxEntry
	if selected == nil {
		return []string{entry(r)}, nil
	} // Existing verified recipes remain readable.
	path := entry(r)
	candidates, e := a.LinuxProgramCandidates(ctx, r, id)
	if e != nil {
		return nil, e
	}
	found := false
	for _, candidate := range candidates {
		if candidate.Path == path && candidate.Kind == selected.Kind && candidate.SHA256 == selected.SHA256 {
			found = true
			break
		}
	}
	if !found {
		return nil, ErrLinuxEntryChanged
	}
	if selected.Kind == "desktop" {
		// GIO interprets the original desktop entry including Exec field codes and
		// Path; ZaH never turns its contents into a shell command.
		if _, e := a.runner().Run(ctx, enter(r, "/usr/bin/test", "-x", "/usr/bin/gio")); e != nil {
			return nil, errors.New("container needs gio to launch the selected desktop entry")
		}
		return []string{"/usr/bin/gio", "launch", path}, nil
	}
	return []string{"/usr/bin/env", "-C", filepath.Dir(path), path}, nil
}

// BeginLinuxEntrySelection is an explicit write operation, separate from the
// read-only inventory. It may start/initialize this verified existing container.
func (a Adapter) BeginLinuxEntrySelection(ctx context.Context, r InstallRequest, id string) (Evidence, error) {
	if r.Recipe.Platform != "linux" {
		return Evidence{}, ErrUnsupported
	}
	proof, e := a.Inspect(ctx, r, id)
	if e != nil {
		return proof, e
	}
	if e = a.verifyRuntimeBindings(ctx, r, id); e != nil {
		return proof, e
	}
	proof.State = "environment_prepared"
	return proof, nil
}
