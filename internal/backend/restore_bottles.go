package backend

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ZZH1124Prime/ZaH/internal/host"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

type PortableBottle struct {
	SavedEntry    *SavedWindowsEntry         `json:"saved_entry,omitempty"`
	SchemaVersion int                        `json:"schema_version"`
	Username      string                     `json:"username"`
	Entry         string                     `json:"entry"`
	SHA256        string                     `json:"sha256"`
	Runner        string                     `json:"runner"`
	Parameters    map[string]json.RawMessage `json:"parameters"`
	Components    map[string]string          `json:"components"`
}
type BottleRebuildState struct {
	Fresh  *host.BindingEvidence `json:"fresh,omitempty"`
	Staged *host.BindingEvidence `json:"staged,omitempty"`
	Phase  string                `json:"phase"`
}

var componentName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+-]{0,127}$`)
var parameterName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func (a Adapter) PortableBottle(ctx context.Context, r InstallRequest) (PortableBottle, error) {
	p := PortableBottle{SchemaVersion: 1, Runner: r.Recipe.Runner, Components: map[string]string{}}
	if r.Windows == nil || !r.Windows.ControlledLaunch {
		return p, errors.New("controlled Bottle registration required")
	}
	if _, e := a.InspectBottles(ctx, r, r.Windows.ResourceIdentity()); e != nil {
		return p, e
	}
	u, e := user.Current()
	if e != nil {
		return p, e
	}
	p.Username = u.Username
	p.Entry, e = filepath.Rel(r.Windows.Bottle.Path, r.Windows.ExecutablePath)
	if e != nil {
		return p, e
	}
	p.Entry = filepath.ToSlash(p.Entry)
	p.SHA256 = r.Windows.EntrySHA256
	p.SavedEntry = r.Windows.SavedEntry
	catalog, e := a.bottleCatalog(ctx)
	if e != nil {
		return p, e
	}
	var fields map[string]json.RawMessage
	if e = json.Unmarshal(catalog[name(r.InstanceID)], &fields); e != nil {
		return p, e
	}
	if e = json.Unmarshal(fields["Parameters"], &p.Parameters); e != nil {
		return p, e
	}
	for _, key := range []string{"DXVK", "VKD3D", "NVAPI", "LatencyFleX"} {
		if raw := fields[key]; len(raw) > 0 && string(raw) != "null" {
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return p, errors.New("unsupported Bottle component metadata")
			}
			p.Components[key] = value
		}
	}
	for _, key := range []string{"DLL_Overrides", "Environment_Variables"} {
		raw := fields[key]
		if len(raw) > 0 && string(raw) != "null" && string(raw) != "{}" {
			return p, errors.New("custom Bottle environment or DLL overrides require a specialized migration adapter")
		}
	}
	return p, p.validate(r)
}
func (p PortableBottle) validate(r InstallRequest) error {
	if p.SchemaVersion != 1 || p.Runner != r.Recipe.Runner || !componentName.MatchString(p.Runner) || strings.Contains(strings.ToLower(p.Runner), "proton") || strings.HasPrefix(p.Runner, "sys-") || !digestPattern.MatchString(p.SHA256) || !strings.HasPrefix(p.Entry, "drive_c/") || !filepath.IsLocal(p.Entry) || filepath.ToSlash(filepath.Clean(p.Entry)) != p.Entry || strings.ContainsAny(p.Entry, "\\\x00\r\n") || len(p.Parameters) > 128 {
		return errors.New("unsupported portable Bottle profile")
	}
	if p.SavedEntry != nil {
		if p.SavedEntry.Path != p.Entry || p.SavedEntry.CWD != filepath.ToSlash(filepath.Dir(p.Entry)) {
			return errors.New("invalid portable saved entry")
		}
		if _, e := quotedProgramArgs(p.SavedEntry.Args); e != nil {
			return e
		}
	}
	if string(p.Parameters["winebridge"]) != "false" || string(p.Parameters["sandbox"]) != "false" {
		return errors.New("portable Bottle requires direct Wine control")
	}
	for k, v := range p.Parameters {
		if !parameterName.MatchString(k) || len(v) > 2048 {
			return errors.New("unsupported Bottle parameter")
		}
	}
	for k, v := range p.Components {
		if k != "DXVK" && k != "VKD3D" && k != "NVAPI" && k != "LatencyFleX" {
			return errors.New("unsupported Bottle component")
		}
		if v != "" && !componentName.MatchString(v) {
			return errors.New("invalid component version")
		}
	}
	return nil
}

// RebuildBottle creates through public CLI, restores only a new owned prefix,
// preserves its new bottle.yml, then registers a verified entry through public CLI.
// Old YAML is never rewritten or installed as a live Bottles database.
func (a Adapter) RebuildBottle(ctx context.Context, r InstallRequest, archive, digest, token string, p PortableBottle, state *BottleRebuildState, save func() error) (Evidence, error) {
	ev := Evidence{State: "restore_pending"}
	if e := validate(r); e != nil {
		return ev, e
	}
	if e := p.validate(r); e != nil {
		return ev, e
	}
	if len(r.Bindings) > 0 {
		return ev, errors.New("restore shared drive bindings explicitly before rebuilding this Bottle")
	}
	if len(token) != 32 || !digestPattern.MatchString(token+token) {
		return ev, errors.New("invalid restore attempt")
	}
	current, e := user.Current()
	if e != nil {
		return ev, e
	}
	if current.Username != p.Username {
		return ev, errors.New("Bottle username differs; automatic registry/profile path rewriting is not supported")
	}
	archiveInfo, e := InspectBottlesBackupArchive(ctx, archive, digest)
	if e != nil {
		return ev, e
	}
	options, e := a.InstallOptions(ctx, "windows")
	if e != nil {
		return ev, e
	}
	hasRunner := false
	for _, option := range options {
		hasRunner = hasRunner || option.Value == p.Runner
	}
	if !hasRunner {
		return ev, errors.New("the archived Bottle runner is not installed; no runner was downloaded")
	}
	if state.Fresh == nil {
		catalog, e := a.bottleCatalog(ctx)
		if e != nil {
			return ev, e
		}
		if _, ok := catalog[name(r.InstanceID)]; ok {
			return ev, errors.New("unclaimed Bottle exists; refusing adoption")
		}
		cmd := bottles("new", "--bottle-name", name(r.InstanceID), "--environment", "application", "--arch", "win64", "--runner", p.Runner)
		cmd.SafeCancel = false
		cmd.Timeout = 0
		state.Phase = "creating"
		if e = save(); e != nil {
			return ev, e
		}
		if _, e = a.runner().Run(ctx, cmd); e != nil {
			return ev, e
		}
		fresh, e := a.newBottleRoot(context.WithoutCancel(ctx), r)
		if e != nil {
			return ev, e
		}
		state.Fresh = &fresh
		state.Phase = "created"
		if e = save(); e != nil {
			return ev, e
		}
	}
	base := filepath.Dir(filepath.Dir(state.Fresh.Path))
	if filepath.Base(filepath.Dir(state.Fresh.Path)) != "bottles" || filepath.Base(state.Fresh.Path) != name(r.InstanceID) {
		return ev, errors.New("invalid restoration ownership")
	}
	staged := filepath.Join(base, ".zah-restore-"+token)
	original := filepath.Join(base, ".zah-original-"+token)
	if state.Phase == "created" {
		if e = host.VerifyBinding(*state.Fresh); e != nil {
			return ev, e
		}
		if e = a.restoreBottleParameters(ctx, r, p); e != nil {
			return ev, e
		}
		r.Windows = &BottlesRegistration{Bottle: *state.Fresh}
		control, e := a.wineControl(ctx, r, filepath.Dir(state.Fresh.Path), "probe", nil)
		if e != nil {
			return ev, e
		}
		if _, e = a.wineControl(ctx, r, filepath.Dir(state.Fresh.Path), "wait", control); e != nil {
			return ev, e
		}
		capacity, e := checkCapacity("restore_bottle_storage", base, uint64(archiveInfo.ExpandedBytes)+(256<<20), false)
		if e != nil {
			return ev, e
		}
		if e = capacityTotals([]CapacityCheck{capacity}); e != nil {
			return ev, e
		}
		if e = extractBottlePayload(ctx, archive, staged, digest); e != nil {
			return ev, e
		}
		// Stage is newly created; failure retains bytes, never touches a live prefix.
		root, e := host.InspectBinding(staged, false)
		if e != nil {
			return ev, e
		}
		state.Staged = &root
		state.Phase = "staged"
		if e = save(); e != nil {
			return ev, e
		}
	}
	if state.Phase == "staged" {
		if state.Staged == nil || state.Staged.Path != staged {
			return ev, errors.New("restoration stage identity missing")
		}
		if e = host.VerifyBinding(*state.Fresh); e != nil {
			return ev, e
		}
		if e = host.VerifyBinding(*state.Staged); e != nil {
			return ev, e
		}
		cfg := filepath.Join(state.Fresh.Path, "bottle.yml")
		b, e := readRegularBounded(cfg, 4<<20)
		if e != nil {
			return ev, e
		}
		if e = os.WriteFile(filepath.Join(staged, "bottle.yml"), b, 0600); e != nil {
			return ev, e
		}
		// Recreate only the fresh prefix's standard drive links, never archived links.
		if e = os.Mkdir(filepath.Join(staged, "dosdevices"), 0700); e != nil && !os.IsExist(e) {
			return ev, e
		}
		for _, drive := range []string{"c:", "z:"} {
			source := filepath.Join(state.Fresh.Path, "dosdevices", drive)
			target, e := os.Readlink(source)
			if os.IsNotExist(e) {
				if drive == "c:" {
					target = "../drive_c"
				} else {
					continue
				}
			} else if e != nil {
				return ev, e
			}
			if drive == "c:" && target != "../drive_c" || drive == "z:" && target != "/" {
				return ev, errors.New("unexpected fresh Wine drive mapping")
			}
			dest := filepath.Join(staged, "dosdevices", drive)
			if existing, e := os.Readlink(dest); e == nil {
				if existing != target {
					return ev, errors.New("staged drive changed")
				}
			} else if os.IsNotExist(e) {
				if e = os.Symlink(target, dest); e != nil {
					return ev, e
				}
			} else {
				return ev, e
			}
		}
		if e = syncDirectory(staged); e != nil {
			return ev, e
		}
		state.Phase = "swap_prepared"
		if e = save(); e != nil {
			return ev, e
		}
	}
	if state.Phase == "swap_prepared" {
		if state.Staged == nil {
			return ev, errors.New("missing staged prefix proof")
		}
		matches := func(path string, proof host.BindingEvidence) bool {
			proof.Path = path
			return host.VerifyBinding(proof) == nil
		}
		parent, e := os.OpenRoot(base)
		if e != nil {
			return ev, e
		}
		defer parent.Close()
		fd, e := parent.Open(".")
		if e != nil {
			return ev, e
		}
		defer fd.Close()
		move := func(from, to string) error {
			return unix.Renameat2(int(fd.Fd()), from, int(fd.Fd()), to, unix.RENAME_NOREPLACE)
		}
		if matches(state.Fresh.Path, *state.Fresh) {
			if e = move(filepath.Join("bottles", name(r.InstanceID)), filepath.Base(original)); e != nil {
				return ev, e
			}
			if e = fd.Sync(); e != nil {
				return ev, e
			}
		}
		if matches(original, *state.Fresh) && matches(staged, *state.Staged) {
			if e = move(filepath.Base(staged), filepath.Join("bottles", name(r.InstanceID))); e != nil {
				return ev, e
			}
			if e = fd.Sync(); e != nil {
				return ev, e
			}
		}
		if !matches(original, *state.Fresh) || !matches(state.Fresh.Path, *state.Staged) {
			return ev, errors.New("prefix publication identities changed; no overwrite performed")
		}
		state.Phase = "published"
		if e = save(); e != nil {
			return ev, e
		}
	}
	if state.Phase != "published" && state.Phase != "verified" {
		return ev, errors.New("unknown Bottle restoration phase")
	}
	target := *state.Staged
	target.Path = state.Fresh.Path
	if e = host.VerifyBinding(target); e != nil {
		return ev, e
	}
	entry := filepath.Join(target.Path, filepath.FromSlash(p.Entry))
	if e = verifyFile(ctx, entry, p.SHA256); e != nil {
		return ev, e
	}
	if e = verifyPE(entry); e != nil {
		return ev, e
	}
	catalog, e := a.bottleCatalog(ctx)
	if e != nil {
		return ev, e
	}
	if _, ok := catalog[name(r.InstanceID)]; !ok {
		return ev, errors.New("restored Bottle unavailable in upstream catalog")
	}
	add := bottles("add", "-b", name(r.InstanceID), "-n", r.Recipe.Entry, "-p", entry)
	if p.SavedEntry != nil {
		args, _ := quotedProgramArgs(p.SavedEntry.Args)
		if args != "" {
			add.Args = append(add.Args, "-l", args)
		}
	}
	add.SafeCancel = false
	add.Timeout = 0
	// Avoid re-adding a saved program after a lost reply.
	programs, e := a.Programs(ctx, r.InstanceID)
	if e != nil {
		return ev, e
	}
	var listed []struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if programs.Truncated || json.Unmarshal([]byte(programs.Stdout), &listed) != nil {
		return ev, errors.New("invalid restored program list")
	}
	found := false
	for _, v := range listed {
		if v.Name == r.Recipe.Entry {
			resolved, e := windowsProgramPath(target.Path, v.Path)
			if e != nil || resolved != entry || found {
				return ev, errors.New("restored program conflict")
			}
			found = true
		}
	}
	if !found {
		if _, e = a.runner().Run(ctx, add); e != nil {
			return ev, e
		}
	}
	if p.SavedEntry != nil {
		r.Windows = &BottlesRegistration{SavedEntry: p.SavedEntry}
	}
	ev, e = a.VerifyManualBottles(ctx, r, ManualBottlesInput{BottlePath: target.Path, ExecutablePath: entry, ProgramName: r.Recipe.Entry})
	if e != nil {
		return ev, e
	}
	if ev.Windows == nil || ev.Windows.Control == nil || !ev.Windows.ControlledLaunch {
		return ev, errors.New("restored Bottle lacks exact Run/Stop control")
	}
	r.Windows = ev.Windows
	if _, e = a.ConfirmStopped(ctx, r, ev.ResourceID); e != nil {
		return ev, e
	}
	state.Phase = "verified"
	if e = save(); e != nil {
		return ev, e
	}
	// Retain the fresh prefix baseline outside the Bottle catalog as recovery
	// evidence. It is never presented as user application data or deleted implicitly.
	return ev, nil
}
func (a Adapter) restoreBottleParameters(ctx context.Context, r InstallRequest, p PortableBottle) error {
	catalog, e := a.bottleCatalog(ctx)
	if e != nil {
		return e
	}
	var current map[string]json.RawMessage
	if json.Unmarshal(catalog[name(r.InstanceID)], &current) != nil {
		return errors.New("invalid new Bottle config")
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(current["Parameters"], &params) != nil {
		return errors.New("new Bottle parameters unavailable")
	}
	for key, want := range p.Components {
		var got string
		_ = json.Unmarshal(current[key], &got)
		if got != want {
			return errors.New("target Bottle component version differs; prepare the matching component configuration before restoration")
		}
	}
	changes := []string{}
	for key, want := range p.Parameters {
		got, ok := params[key]
		if ok && reflect.DeepEqual(got, want) {
			continue
		}
		if string(want) != "true" && string(want) != "false" {
			// Guided installations support integer DPI; preserve that same bounded
			// setting when restoring. Other non-boolean settings remain explicit.
			var dpi int
			if key != "custom_dpi" || json.Unmarshal(want, &dpi) != nil || (dpi != 0 && (dpi < 96 || dpi > 384)) {
				return errors.New("non-boolean Bottle setting differs from target defaults; automatic conversion refused")
			}
		}
		changes = append(changes, key+":"+string(want))
	}
	sort.Strings(changes)
	if len(changes) > 0 {
		c := bottles("edit", "-b", name(r.InstanceID), "--params", strings.Join(changes, ","))
		c.SafeCancel = false
		c.Timeout = 0
		if _, e = a.runner().Run(ctx, c); e != nil {
			return e
		}
	}
	catalog, e = a.bottleCatalog(ctx)
	if e != nil {
		return e
	}
	if json.Unmarshal(catalog[name(r.InstanceID)], &current) != nil || json.Unmarshal(current["Parameters"], &params) != nil {
		return errors.New("updated Bottle settings unavailable")
	}
	for key, want := range p.Parameters {
		var a, b any
		_ = json.Unmarshal(want, &a)
		_ = json.Unmarshal(params[key], &b)
		if !reflect.DeepEqual(a, b) {
			return errors.New("upstream did not apply restored Bottle settings")
		}
	}
	return nil
}
func readRegularBounded(path string, max int64) ([]byte, error) {
	f, e := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() || st.Size() > max {
		return nil, errors.New("invalid bounded configuration file")
	}
	return io.ReadAll(io.LimitReader(f, max+1))
}
func extractBottlePayload(ctx context.Context, archive, target, digest string) (err error) {
	if e := os.Mkdir(target, 0700); e != nil {
		return e
	}
	proof, e := host.InspectBinding(target, false)
	if e != nil {
		return e
	}
	defer func() {
		if err != nil && host.VerifyBinding(proof) == nil {
			_ = os.RemoveAll(target)
		}
	}()
	f, e := os.OpenFile(archive, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if e != nil {
		return e
	}
	defer f.Close()
	streamHash := sha256.New()
	gz, e := gzip.NewReader(io.TeeReader(contextReader{ctx, f}, streamHash))
	if e != nil {
		return e
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	root, e := os.OpenRoot(target)
	if e != nil {
		return e
	}
	defer root.Close()
	total := int64(0)
	aliases := map[string]string{}
	members := map[string]byte{}
	for n := 0; ; n++ {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if n > maxSnapshotEntries {
			return errors.New("too many prefix entries")
		}
		_, rel, ok := strings.Cut(strings.TrimSuffix(h.Name, "/"), "/")
		if !ok {
			if h.Typeflag == tar.TypeDir {
				continue
			}
			return errors.New("invalid prefix root")
		}
		if !filepath.IsLocal(rel) || filepath.ToSlash(filepath.Clean(rel)) != rel || strings.ContainsAny(rel, "\\\x00") {
			return errors.New("unsafe prefix member")
		}
		if _, exists := members[rel]; exists {
			return errors.New("duplicate prefix member")
		}
		members[rel] = h.Typeflag
		if rel == "bottle.yml" {
			continue
		}
		if strings.HasPrefix(rel, "dosdevices") || strings.Contains(rel, "lsfg-vk") || strings.HasPrefix(rel, ".zah-") {
			return errors.New("unportable prefix mapping")
		}
		if h.Typeflag == tar.TypeDir {
			if e = root.MkdirAll(rel, 0700); e != nil {
				return e
			}
			continue
		}
		if h.Typeflag == tar.TypeSymlink && h.Size == 0 && bottleUserAlias(rel, h.Linkname) {
			aliases[rel] = h.Linkname
			continue
		}
		if h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > MaxSnapshotBytes-total {
			return errors.New("unsupported prefix member")
		}
		total += h.Size
		if e = root.MkdirAll(filepath.Dir(rel), 0700); e != nil {
			return e
		}
		out, e := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode)&0777)
		if e != nil {
			return e
		}
		_, e = io.Copy(out, contextReader{ctx, tr})
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
	}
	// Hash the very stream that supplied extracted bytes, not a later pathname
	// read: replacing/mutating the archive cannot authenticate different payloads.
	padding := make([]byte, 32<<10)
	var trailing int64
	for {
		n, readErr := gz.Read(padding)
		trailing += int64(n)
		if trailing > 1<<20 {
			return errors.New("excessive tar padding")
		}
		for _, b := range padding[:n] {
			if b != 0 {
				return errors.New("trailing tar content")
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if hex.EncodeToString(streamHash.Sum(nil)) != digest {
		return errors.New("extracted archive digest mismatch")
	}
	if e = validateBottleAliases(aliases, members); e != nil {
		return e
	}
	// Publish the one bounded relative alias only after all regular payload bytes
	// have been authenticated. No archive member is ever written through it.
	for name, link := range aliases {
		if e = root.Symlink(link, name); e != nil {
			return e
		}
	}

	return syncDirectory(target)
}
