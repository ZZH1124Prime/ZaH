package backend

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/ZZH1124Prime/ZaH/internal/host"
)

type ProgramCandidate struct {
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	CWD       string   `json:"cwd,omitempty"`
	Arguments string   `json:"arguments,omitempty"`
	Args      []string `json:"args,omitempty"`
}

// ProgramCandidates uses upstream discovery; no application is executed. Only
// regular x86/AMD64 EXEs inside this registered Bottle are exposed as candidates.
func (a Adapter) ProgramCandidates(ctx context.Context, r InstallRequest) ([]ProgramCandidate, error) {
	if r.Windows == nil {
		return nil, errors.New("prepare and register a dedicated Bottle first")
	}
	if e := host.VerifyBinding(r.Windows.Bottle); e != nil {
		return nil, e
	}
	out, e := a.Programs(ctx, r.InstanceID)
	if e != nil {
		return nil, e
	}
	if out.Truncated {
		return nil, errors.New("program inventory exceeds limit")
	}
	var rows []struct {
		Name      string `json:"name"`
		Path      string `json:"path"`
		Arguments string `json:"arguments"`
		Folder    string `json:"folder"`
	}
	if e = json.Unmarshal([]byte(out.Stdout), &rows); e != nil {
		return nil, e
	}
	if len(rows) > 1000 {
		return nil, errors.New("too many Bottle programs")
	}
	result := []ProgramCandidate{}
	for _, row := range rows {
		p, e := windowsProgramPath(r.Windows.Bottle.Path, row.Path)
		if e != nil {
			continue
		}
		p, e = regularInside(r.Windows.Bottle.Path, p)
		if e != nil || verifyPE(p) != nil {
			continue
		}
		args, e := discoveredProgramArgs(row.Arguments)
		if e != nil {
			return nil, e
		}
		cwd := filepath.Dir(p)
		if row.Folder != "" {
			cwd = row.Folder
		}

		result = append(result, ProgramCandidate{Name: row.Name, Path: p, CWD: cwd, Arguments: row.Arguments, Args: args})
		if len(result) > 256 {
			return nil, errors.New("too many executable candidates")
		}
	}
	return result, nil
}
func (a Adapter) EntryDigest(ctx context.Context, r InstallRequest, path string) (string, error) {
	if r.Windows == nil {
		return "", errors.New("Bottle registration required")
	}
	if e := host.VerifyBinding(r.Windows.Bottle); e != nil {
		return "", e
	}
	p, e := regularInside(r.Windows.Bottle.Path, path)
	if e != nil {
		return "", e
	}
	if e = verifyPE(p); e != nil {
		return "", e
	}
	return hashRegular(ctx, p, 4<<30)
}

// SaveProgram adds an explicit entry via the public CLI; it does not execute it.
// The caller proves the environment is stopped and persists the returned proof.
func (a Adapter) SaveProgram(ctx context.Context, r InstallRequest, identity, program, path, digest string, options ...WindowsEntryOptions) (Evidence, error) {
	if program == "" || strings.HasPrefix(program, "-") || strings.ContainsAny(program, "\x00\r\n") || len(program) > 128 {
		return Evidence{}, errors.New("invalid saved program")
	}
	if _, e := a.ConfirmStopped(ctx, r, identity); e != nil {
		return Evidence{}, e
	}
	sum, e := a.EntryDigest(ctx, r, path)
	if e != nil {
		return Evidence{}, e
	}
	if sum != digest {
		return Evidence{}, errors.New("selected executable changed since review")
	}
	settings := WindowsEntryOptions{}
	if len(options) > 1 {
		return Evidence{}, errors.New("one entry selection expected")
	}
	if len(options) == 1 {
		settings = options[0]
	}
	saved, e := newSavedEntry(r.Windows.Bottle.Path, path, settings)
	if e != nil {
		return Evidence{}, e
	}
	args, _ := quotedProgramArgs(saved.Args)
	candidates, e := a.ProgramCandidates(ctx, r)
	if e != nil {
		return Evidence{}, e
	}
	exists := false
	for _, candidate := range candidates {
		if candidate.Name == program {
			if candidate.Path != path || exists {
				return Evidence{}, errors.New("saved program name is ambiguous or changed")
			}
			exists = true
		}
	}
	if !exists {
		c := bottles("add", "-b", name(r.InstanceID), "-n", program, "-p", path)
		if args != "" {
			c.Args = append(c.Args, "-l", args)
		}
		c.SafeCancel = false
		c.Timeout = 0
		if _, e = a.runner().Run(ctx, c); e != nil {
			return Evidence{}, e
		}
	}
	next := r
	copyRegistration := *r.Windows
	copyRegistration.SavedEntry = saved
	next.Windows = &copyRegistration
	next.Recipe.Entry = program
	proof, e := a.VerifyManualBottles(ctx, next, ManualBottlesInput{BottlePath: r.Windows.Bottle.Path, ExecutablePath: path, ProgramName: program})
	if e != nil {
		return proof, e
	}
	if proof.Windows == nil || proof.Windows.Control == nil || !proof.Windows.ControlledLaunch || proof.Windows.Control.persistentIdentity() != r.Windows.Control.persistentIdentity() {
		return proof, errors.New("saved entry changed exact Wine control")
	}
	if proof.Windows.EntrySHA256 != digest {
		return proof, errors.New("saved executable changed during registration")
	}
	next.Windows = proof.Windows
	if _, e = a.ConfirmStopped(ctx, next, proof.ResourceID); e != nil {
		return proof, e
	}
	return proof, nil
}
func InstallerPath(r InstallRequest) (string, error) {
	if r.Windows == nil || r.Material == nil || r.Material.Mode != "installer" {
		return "", errors.New("no retained installer")
	}
	return filepath.Join(r.Windows.Bottle.Path, "drive_c", "zah-material", filepath.FromSlash(r.Material.Entry)), nil
}

// InstallationLocation is a native-dialog starting point, not an arbitrary host
// path supplied by the renderer. Final entry validation still confines the file.
func (a Adapter) InstallationLocation(r InstallRequest) (string, error) {
	if r.Windows == nil {
		return "", errors.New("prepare a Windows environment first")
	}
	if e := host.VerifyBinding(r.Windows.Bottle); e != nil {
		return "", e
	}
	path := filepath.Join(r.Windows.Bottle.Path, "drive_c")
	if resolved, e := filepath.EvalSymlinks(path); e != nil || resolved != path {
		return "", errors.New("Bottle drive path changed")
	}
	return path, nil
}
func (a Adapter) LaunchWindowsInstallationGUI(ctx context.Context, r InstallRequest, id, action string) (Evidence, error) {
	tool := ""
	switch action {
	case "files":
		tool = "explorer"
	case "uninstall":
		tool = "uninstaller"
	default:
		return Evidence{}, ErrUnsupported
	}
	proof, e := a.InspectBottles(ctx, r, id)
	if e != nil {
		return proof, e
	}
	c := bottles("tools", tool, "-b", name(r.InstanceID))
	c.SafeCancel = false
	c.Timeout = 0
	out, e := a.runner().Run(ctx, c)
	proof.Result = out
	proof.State = "launcher_exited_application_unknown"
	return proof, e
}
