package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/host"
	"github.com/ZZH1124Prime/ZaH/internal/installmedia"
	"github.com/ZZH1124Prime/ZaH/internal/process"
)

// ManualBottlesInput is the user's explicit completion evidence after installing
// in Bottles. Nothing here is an arbitrary command or an inferred executable.
type ManualBottlesInput struct {
	BottlePath     string `json:"bottle_path"`
	ExecutablePath string `json:"executable_path"`
	ProgramName    string `json:"program_name"`
}

// BottlesRegistration pins the selected upstream bottle and installed program.
// It is generated only by VerifyManualBottles and must be persisted by service.
// Hashes detect config/entry drift without retaining potentially sensitive args.
type BottlesRegistration struct {
	SavedEntry          *SavedWindowsEntry       `json:"saved_entry,omitempty"`
	ControlledLaunch    bool                     `json:"controlled_launch,omitempty"`
	Control             *WineControl             `json:"wine_control,omitempty"`
	ControlRoot         string                   `json:"control_root,omitempty"`
	SchemaVersion       int                      `json:"schema_version"`
	InstanceID          string                   `json:"instance_id"`
	BottleName          string                   `json:"bottle_name"`
	Bottle              host.BindingEvidence     `json:"bottle"`
	ProgramName         string                   `json:"program_name"`
	ExecutablePath      string                   `json:"executable_path"`
	EntrySHA256         string                   `json:"entry_sha256"`
	ConfigurationSHA256 string                   `json:"configuration_sha256"`
	ProgramSHA256       string                   `json:"program_sha256"`
	Runner              string                   `json:"runner"`
	Architecture        string                   `json:"architecture"`
	VerifiedAt          time.Time                `json:"verified_at"`
	Bindings            []WindowsBindingEvidence `json:"bindings,omitempty"`
}

// ResourceIdentity covers stable directory identity and reviewed executable
// evidence; changes require a fresh explicit manual registration.
func (r BottlesRegistration) ResourceIdentity() string {
	r.VerifiedAt = time.Time{}
	if r.Control != nil {
		control := r.Control.persistentIdentity()
		r.Control = &control
	}
	b, _ := json.Marshal(r)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func bottles(args ...string) process.Command {
	return command("flatpak", append([]string{"run", "--command=bottles-cli", "com.usebottles.bottles"}, args...)...)
}

type bottleConfig struct {
	Parameters           json.RawMessage `json:"Parameters,omitempty"`
	EnvironmentVariables json.RawMessage `json:"Environment_Variables,omitempty"`
	Name                 string          `json:"Name"`
	Arch                 string          `json:"Arch"`
	Runner               string          `json:"Runner"`
	Path                 string          `json:"Path"`
	CustomPath           json.RawMessage `json:"Custom_Path"`
	CreationDate         string          `json:"Creation_Date"`
	Environment          string          `json:"Environment"`
}

// VerifyManualBottles observes supported CLI output and reads package headers;
// it never runs an installer/application or edits Bottles configuration files.
// The upstream CLI may itself initialize caches; this is an explicit backend
// operation, never part of passive doctor or manager startup.
func (a Adapter) VerifyManualBottles(ctx context.Context, r InstallRequest, in ManualBottlesInput) (Evidence, error) {
	if err := validate(r); err != nil {
		return Evidence{}, err
	}
	if r.Recipe.Platform != "windows" {
		return Evidence{}, errors.New("Windows instance required")
	}
	if in.ProgramName != r.Recipe.Entry || strings.ContainsAny(in.ProgramName, "\x00\r\n") || strings.HasPrefix(in.ProgramName, "-") {
		return Evidence{}, errors.New("registered program must match the recipe entry")
	}
	bottle, err := host.InspectBinding(in.BottlePath, true)
	if err != nil {
		return Evidence{}, err
	}
	if bottle.Path == "/" {
		return Evidence{}, errors.New("invalid bottle root")
	}
	entry, err := regularInside(bottle.Path, in.ExecutablePath)
	if err != nil {
		return Evidence{}, err
	}
	if err = verifyPE(entry); err != nil {
		return Evidence{}, err
	}
	source, err := a.runner().Run(ctx, bottles("--json", "list", "bottles"))
	if err != nil {
		return Evidence{State: "unknown"}, err
	}
	if source.Truncated {
		return Evidence{}, errors.New("Bottles catalog output exceeds limit")
	}
	var all map[string]json.RawMessage
	if err = json.Unmarshal([]byte(source.Stdout), &all); err != nil {
		return Evidence{}, errors.New("unsupported Bottles catalog JSON shape")
	}
	raw, ok := all[name(r.InstanceID)]
	if !ok {
		return Evidence{State: "missing"}, errors.New("expected bottle is not registered in Bottles")
	}
	var cfg bottleConfig
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return Evidence{}, errors.New("unsupported bottle configuration evidence")
	}
	if cfg.Name != name(r.InstanceID) || cfg.Arch != "win64" || cfg.Runner != r.Recipe.Runner || cfg.CreationDate == "" || cfg.Environment == "Steam" {
		return Evidence{State: "changed"}, errors.New("bottle name, architecture, runner or creation evidence does not match")
	}
	reported := cfg.Path
	custom := string(cfg.CustomPath) != "" && string(cfg.CustomPath) != "null" && string(cfg.CustomPath) != "false" && string(cfg.CustomPath) != `""`
	if !custom {
		base, err := a.runner().Run(ctx, bottles("info", "bottles-path"))
		if err != nil {
			return Evidence{}, err
		}
		if base.Truncated {
			return Evidence{}, errors.New("bottle root output exceeds limit")
		}
		root := strings.TrimSpace(base.Stdout)
		if !filepath.IsAbs(root) || !filepath.IsLocal(cfg.Path) {
			return Evidence{}, errors.New("unsupported upstream bottle path")
		}
		reported = filepath.Join(root, cfg.Path)
	}
	canonical, err := filepath.EvalSymlinks(reported)
	if err != nil || canonical != bottle.Path {
		return Evidence{}, errors.New("selected directory differs from upstream bottle path")
	}
	res, err := a.Programs(ctx, r.InstanceID)
	if err != nil {
		return Evidence{State: "unknown"}, err
	}
	if res.Truncated {
		return Evidence{}, errors.New("Bottles program output exceeds limit")
	}
	var programs []map[string]json.RawMessage
	if err = json.Unmarshal([]byte(res.Stdout), &programs); err != nil {
		return Evidence{}, errors.New("unsupported Bottles programs JSON shape")
	}
	var selected map[string]json.RawMessage
	count := 0
	for _, p := range programs {
		var n string
		_ = json.Unmarshal(p["name"], &n)
		if n == in.ProgramName {
			selected = p
			count++
		}
	}
	if count != 1 {
		return Evidence{}, errors.New("program entry missing or ambiguous; register one explicit entry in Bottles")
	}
	for _, key := range []string{"pre_script", "post_script"} {
		if value := selected[key]; len(value) > 0 && string(value) != "null" && string(value) != `""` {
			return Evidence{}, errors.New("program scripts require a specialized reviewed adapter")
		}
	}
	var path string
	if json.Unmarshal(selected["path"], &path) != nil {
		return Evidence{}, errors.New("program path unavailable")
	}
	resolved, err := windowsProgramPath(bottle.Path, path)
	if err != nil || resolved != entry {
		return Evidence{}, errors.New("selected executable differs from registered program")
	}
	sum, err := hashRegular(ctx, entry, 4<<30)
	if err != nil {
		return Evidence{}, err
	}
	mappingProof, err := a.verifyWindowsBindings(ctx, r, bottle)
	if err != nil {
		return Evidence{State: "waiting_user"}, err
	}
	configJSON, _ := json.Marshal(cfg)
	programJSON, _ := json.Marshal(selected)
	configHash := sha256.Sum256(configJSON)
	programHash := sha256.Sum256(programJSON)
	registration := BottlesRegistration{SchemaVersion: 1, InstanceID: r.InstanceID, BottleName: name(r.InstanceID), Bottle: bottle, ProgramName: in.ProgramName, ExecutablePath: entry, EntrySHA256: sum, ConfigurationSHA256: hex.EncodeToString(configHash[:]), ProgramSHA256: hex.EncodeToString(programHash[:]), Runner: cfg.Runner, Architecture: cfg.Arch, VerifiedAt: time.Now().UTC(), Bindings: mappingProof}
	if r.Windows != nil && r.Windows.SavedEntry != nil {
		saved := *r.Windows.SavedEntry
		if e := saved.verify(bottle.Path, entry, selected); e != nil {
			return Evidence{}, e
		}
		registration.SavedEntry = &saved
	}
	// Missing/unsupported control metadata does not fake stop support. Existing
	// registrations stay readable; new compatible registrations carry exact proof.
	r.Windows = &registration
	if len(cfg.Parameters) > 0 {
		control, root, e := a.probeWineControl(ctx, r, cfg)
		if e == nil {
			registration.Control = control
			registration.ControlRoot = root
			registration.ControlledLaunch = directWineProgram(cfg, selected)
		}
	}
	return Evidence{ResourceID: registration.ResourceIdentity(), State: "entry_verified", Entry: in.ProgramName, Windows: &registration}, nil
}

// InspectBottles detects external replacement or application/configuration drift
// before launch. A previous successful registration is mandatory.
func (a Adapter) InspectBottles(ctx context.Context, r InstallRequest, resourceID string) (Evidence, error) {
	old := r.Windows
	if old == nil || old.SchemaVersion != 1 || old.InstanceID != r.InstanceID || old.BottleName != name(r.InstanceID) || old.ResourceIdentity() != resourceID {
		return Evidence{}, errors.New("verified bottle registration required")
	}
	if err := host.VerifyBinding(old.Bottle); err != nil {
		return Evidence{State: "changed"}, err
	}
	ev, err := a.VerifyManualBottles(ctx, r, ManualBottlesInput{BottlePath: old.Bottle.Path, ExecutablePath: old.ExecutablePath, ProgramName: old.ProgramName})
	if err != nil {
		return ev, err
	}
	if ev.ResourceID != resourceID {
		return Evidence{State: "changed", Windows: ev.Windows}, errors.New("bottle, runner or installed program changed; reverify before launch")
	}
	ev.State = "registered_running_state_unknown"
	return ev, nil
}

func (a Adapter) launchBottles(ctx context.Context, r InstallRequest, id string) (Evidence, error) {
	ev, err := a.InspectBottles(ctx, r, id)
	if err != nil {
		return ev, err
	}
	for _, b := range r.Bindings {
		if err := host.VerifyBinding(b.Host); err != nil {
			return Evidence{}, err
		}
	}
	c := bottles("run", "-b", name(r.InstanceID), "-p", r.Windows.ProgramName)
	c.SafeCancel = false
	c.Timeout = 0
	result, err := a.runner().Run(ctx, c)
	ev.Result = result
	ev.State = "launcher_exited_application_unknown"
	return ev, err
}

func regularInside(root, path string) (string, error) {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return "", errors.New("absolute installed executable path required")
	}
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return "", errors.New("installed executable must remain inside the selected bottle")
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("installed executable must be a regular file")
	}
	return p, nil
}
func windowsProgramPath(root, p string) (string, error) {
	if len(p) > 3 && strings.EqualFold(p[:3], `C:\`) {
		rel := strings.ReplaceAll(p[3:], `\`, "/")
		if !filepath.IsLocal(rel) || strings.Contains(rel, ":") {
			return "", errors.New("unsafe Wine drive path")
		}
		p = filepath.Join(root, "drive_c", rel)
	}
	return regularInside(root, p)
}
func verifyPE(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if installmedia.PEArchitecture(f, st.Size()) == "" {
		return errors.New("registered executable must be x86 or AMD64 PE")
	}
	return nil
}
func hashRegular(ctx context.Context, p string, max int64) (string, error) {
	st, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() || st.Size() > max {
		return "", errors.New("file type or size exceeds allowed limit")
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(contextReader{ctx, f}, max+1))
	if err != nil {
		return "", err
	}
	if n > max {
		return "", errors.New("file exceeds size limit")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
