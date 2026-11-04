package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// LinuxProgramCandidate identifies an installed file, never a transient list ID.
// For desktop entries Path+SHA256 retain the original Exec/Path/field-code
// semantics. Args is empty: callers must launch the desktop file natively, not
// reinterpret its Exec as a shell command or replace it with a guessed binary.
type LinuxProgramCandidate struct {
	Name   string   `json:"name"`
	Kind   string   `json:"kind"`
	Path   string   `json:"path"`
	CWD    string   `json:"cwd,omitempty"`
	Args   []string `json:"args,omitempty"`
	SHA256 string   `json:"sha256"`
}

// LinuxProgramCandidates reads installed entries in an already prepared/running
// owned container. It never starts a stopped container intentionally, installs
// dependencies, executes a candidate, or chooses one on the user's behalf.
// The service must hold its existing environment/lifecycle lock throughout.
func (a Adapter) LinuxProgramCandidates(ctx context.Context, r InstallRequest, resourceID string) ([]LinuxProgramCandidate, error) {
	if err := a.linuxInteractiveContext(ctx, r, resourceID); err != nil {
		return nil, err
	}
	roots := linuxDesktopRoots(r)
	appImage := ""
	if r.Recipe.Strategy == "appimage" {
		appImage = filepath.Join(r.PrivateHome, ".zah-install", "squashfs-root", "AppRun")
	}
	config, _ := json.Marshal(map[string]any{"roots": roots, "appimage": appImage})
	c := enter(r, "/usr/bin/python3", "-I", "-c", linuxInventoryScript, string(config))
	c.MaxOutput = 1 << 20
	out, err := a.runner().Run(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("read installed Linux entries (container Python 3 required): %w", err)
	}
	if out.Truncated {
		return nil, errors.New("Linux program inventory exceeds limit")
	}
	var candidates []LinuxProgramCandidate
	if err := json.Unmarshal([]byte(out.Stdout), &candidates); err != nil {
		return nil, errors.New("invalid Linux program inventory")
	}
	if len(candidates) > 256 {
		return nil, errors.New("too many installed Linux programs")
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if err := validateLinuxCandidate(r, candidate); err != nil {
			return nil, err
		}
		if seen[candidate.Path] {
			return nil, errors.New("duplicate Linux candidate path")
		}
		seen[candidate.Path] = true
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Name == candidates[j].Name {
			return candidates[i].Path < candidates[j].Path
		}
		return candidates[i].Name < candidates[j].Name
	})
	if candidates == nil {
		candidates = []LinuxProgramCandidate{}
	}
	return candidates, nil
}

func linuxDesktopRoots(r InstallRequest) []string {
	return []string{filepath.Join(r.PrivateHome, ".local/share/applications"), "/usr/local/share/applications", "/usr/share/applications"}
}

func validateLinuxCandidate(r InstallRequest, v LinuxProgramCandidate) error {
	if v.Name == "" || len(v.Name) > 1024 || strings.ContainsAny(v.Name, "\x00\r\n") || !filepath.IsAbs(v.Path) || filepath.Clean(v.Path) != v.Path || strings.ContainsAny(v.Path, "\x00\r\n") || !digestPattern.MatchString(v.SHA256) || len(v.Args) != 0 {
		return errors.New("invalid installed Linux entry")
	}
	if v.CWD != "" && (!filepath.IsAbs(v.CWD) || filepath.Clean(v.CWD) != v.CWD || strings.ContainsAny(v.CWD, "\x00\r\n")) {
		return errors.New("invalid Linux entry working directory")
	}
	switch v.Kind {
	case "desktop":
		for _, root := range linuxDesktopRoots(r) {
			if strings.HasPrefix(v.Path, root+"/") && strings.HasSuffix(v.Path, ".desktop") {
				return nil
			}
		}
	case "executable":
		if r.Recipe.Strategy == "appimage" && v.Path == filepath.Join(r.PrivateHome, ".zah-install/squashfs-root/AppRun") && v.CWD == filepath.Dir(v.Path) {
			return nil
		}
	}
	return errors.New("entry is outside the installed Linux application locations")
}

func (a Adapter) linuxInteractiveContext(ctx context.Context, r InstallRequest, id string) error {
	if r.Recipe.Platform != "linux" {
		return errors.New("Linux environment required")
	}
	proof, err := a.Inspect(ctx, r, id)
	if err != nil {
		return err
	}
	if proof.State != "running" {
		return fmt.Errorf("%w: start the prepared Linux environment before browsing or maintaining it", ErrManual)
	}
	return a.verifyRuntimeBindings(ctx, r, id)
}

// Fixed, read-only helper executed inside the verified container. Python's
// standard library is sufficient; no desktop executable or Exec text is run.
const linuxInventoryScript = `import configparser, hashlib, json, os, pathlib, stat, sys
cfg=json.loads(sys.argv[1]); result=[]; count=0

def digest(path, limit):
    h=hashlib.sha256()
    with open(path,'rb') as stream:
        size=os.fstat(stream.fileno()).st_size
        if size>limit: raise ValueError('entry size limit')
        for chunk in iter(lambda:stream.read(65536),b''): h.update(chunk)
    return h.hexdigest()

def inside(path, root):
    return os.path.commonpath([os.path.realpath(path),root])==root

for root in cfg['roots']:
    if not os.path.isdir(root) or os.path.realpath(root)!=root: continue
    for directory, dirs, files in os.walk(root,followlinks=False):
        dirs[:]=[d for d in dirs if not os.path.islink(os.path.join(directory,d))]
        for filename in sorted(files):
            if not filename.endswith('.desktop'): continue
            count+=1
            if count>4096: raise ValueError('desktop scan limit')
            path=os.path.join(directory,filename)
            if not inside(path,root) or not os.path.isfile(path): continue
            if os.stat(path).st_size>262144: continue
            try:
                raw=pathlib.Path(path).read_bytes()
                parser=configparser.ConfigParser(interpolation=None,strict=True)
                parser.optionxform=str; parser.read_string(raw.decode('utf-8'))
                item=parser['Desktop Entry']
                if item.get('Type')!='Application' or item.get('Hidden')=='true' or item.get('NoDisplay')=='true': continue
                if not item.get('Exec') and item.get('DBusActivatable')!='true': continue
                label=item.get('Name','').strip()
                if not label: continue
                cwd=item.get('Path','')
                if cwd and (not os.path.isabs(cwd) or os.path.normpath(cwd)!=cwd): continue
                result.append(dict(name=label,kind='desktop',path=path,cwd=cwd,sha256=hashlib.sha256(raw).hexdigest()))
            except (UnicodeError,configparser.Error,KeyError): continue
            if len(result)>256: raise ValueError('program inventory limit')
app=cfg['appimage']
if app:
    root=os.path.dirname(app)
    if os.path.isdir(root) and os.path.realpath(root)==root and inside(app,root) and os.path.isfile(app) and os.access(app,os.X_OK):
        result.append(dict(name='AppImage',kind='executable',path=app,cwd=root,sha256=digest(app,4<<30)))
print(json.dumps(result,ensure_ascii=False))
`
