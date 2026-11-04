package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/host"
)

// WineControl pins a regular managed Wine runner in the Bottles Flatpak. Steam,
// Proton, system runners and nested per-bottle sandboxes are intentionally excluded.
// Prefix device/inode is also checked against the independently selected host root.
type WineControl struct {
	Server         string `json:"server"`
	SHA256         string `json:"sha256"`
	WineSHA256     string `json:"wine_sha256"`
	Prefix         string `json:"prefix"`
	Device         uint64 `json:"device"`
	Inode          uint64 `json:"inode"`
	UID            uint64 `json:"uid"`
	TmpDevice      uint64 `json:"tmp_device"`
	TmpInode       uint64 `json:"tmp_inode"`
	SharedTmpScope string `json:"shared_tmp_scope,omitempty"`
}

// Flatpak's per-app temporary directory is GC-able only after every instance
// exits. A verified shared scope pins the policy and host runtime directory;
// its temporary inode is operation evidence, not permanent registration data.
// Unrecognized/legacy namespaces keep the strict inode comparison.
func (w WineControl) persistentIdentity() WineControl {
	if w.SharedTmpScope != "" {
		w.TmpDevice, w.TmpInode = 0, 0
	}
	return w
}

// Start and stop use the same cleaned Wine control namespace. This changes only
// the dedicated child environment; desktop/proxy/network variables are retained.
const controlledBottlesLaunchHelper = `import os,sys
bottle,program=sys.argv[1:]
for key in list(os.environ):
 if key.startswith('WINE') or key in ('LD_PRELOAD','LD_LIBRARY_PATH','LD_AUDIT','TMPDIR'): os.environ.pop(key,None)
os.execvp('bottles-cli',['bottles-cli','run','-b',bottle,'-p',program])
`

// This constant helper uses Python already bundled with Bottles. It neither
// imports Bottles private APIs nor searches/kills host PIDs. Only Wine's documented
// prefix-scoped shutdown protocol and server wait are invoked. No caller-controlled shell is used.
const wineControlHelper = `import configparser,hashlib,json,os,stat,subprocess,sys
mode,root,prefix,runner,expected_json=sys.argv[1:6]
host_scope=sys.argv[6] if len(sys.argv)>6 else ''
expected=json.loads(expected_json)
if mode not in ('probe','stop','wait'): raise ValueError('invalid operation')
if not root.startswith('/') or os.path.basename(root)!='bottles': raise ValueError('unsupported Bottles root')
if not runner or '/' in runner or '\\' in runner or runner.startswith('sys-') or 'proton' in runner.lower(): raise ValueError('unsupported runner')
base=os.path.join(os.path.dirname(root),'runners',runner)
if os.path.realpath(base)!=base or not os.path.isdir(base): raise ValueError('runner path changed')
if os.path.realpath(prefix)!=prefix or not os.path.isdir(prefix): raise ValueError('prefix path changed')
def digest(filename):
 real=os.path.realpath(filename)
 if not real.startswith(base+os.sep): raise ValueError('runner file escaped')
 fd=os.open(real,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
 try:
  s=os.fstat(fd)
  if not stat.S_ISREG(s.st_mode) or s.st_size>64*1024*1024 or not s.st_mode&0o111: raise ValueError('invalid runner binary')
  with os.fdopen(fd,'rb',closefd=False) as f:
   if f.read(4)!=b'\x7fELF': raise ValueError('runner must be native ELF')
   f.seek(0);h=hashlib.sha256()
   while True:
    b=f.read(65536)
    if not b: break
    h.update(b)
  return real,h.hexdigest()
 finally: os.close(fd)
server,sha=digest(os.path.join(base,'bin','wineserver'))
wine=os.path.join(base,'bin','wine64')
if not os.path.exists(wine): wine=os.path.join(base,'bin','wine')
wine,wine_sha=digest(wine)
p=os.stat(prefix);tmp=os.stat('/tmp')
if p.st_uid!=os.getuid(): raise ValueError('prefix owner differs')
proof=dict(server=server,sha256=sha,wine_sha256=wine_sha,prefix=prefix,device=p.st_dev,inode=p.st_ino,uid=os.getuid(),tmp_device=tmp.st_dev,tmp_inode=tmp.st_ino)
# Verified against Flatpak 1.18 flatpak-run.c and flatpak-instance.c: all
# non-nested instances share /tmp, protected from GC by the per-app read lock.
# Pin effective policy/runtime/installation as well as the host runtime root.
if host_scope and os.path.isfile('/.flatpak-info'):
 info=configparser.ConfigParser(interpolation=None);info.read('/.flatpak-info')
 version=info.get('Instance','flatpak-version',fallback='').split('.')
 if len(version)>=2 and all(x.isdigit() for x in version[:2]) and tuple(map(int,version[:2])) >= (1,18) and info.get('Application','name',fallback='')=='com.usebottles.bottles' and not info.getboolean('Instance','sandbox',fallback=False):
  policy={section:dict(info.items(section)) for section in info.sections()}
  policy['Instance'].pop('instance-id',None)
  policy['HostRuntime']=host_scope
  proof['shared_tmp_scope']=hashlib.sha256(json.dumps(policy,sort_keys=True,separators=(',',':')).encode()).hexdigest()
def persistent(p):
 p=p.copy()
 if p.get('shared_tmp_scope'):
  p['tmp_device']=0;p['tmp_inode']=0
 return p
if mode!='probe':
 if persistent(proof)!=persistent(expected): raise ValueError('Wine control identity changed; re-register')
 env=os.environ.copy()
 for key in list(env):
  if key.startswith('WINE') or key in ('LD_PRELOAD','LD_LIBRARY_PATH','LD_AUDIT','TMPDIR'): env.pop(key,None)
 env['WINEPREFIX']=prefix
 env['PATH']=os.path.join(base,'bin')+':/usr/bin:/bin'
 if mode=='stop':
  # Shared /tmp does not imply a shared PID namespace. wineserver -k
  # signals the F_GETLK owner, which is not addressable across Flatpak instances.
  # Wineboot sends prefix-scoped process termination through the Wine protocol;
  # --shutdown prevents initialization/startup hooks from running afterwards.
  subprocess.run([wine,'wineboot.exe','--kill','--shutdown'],env=env,cwd=prefix,check=True,timeout=20,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 subprocess.run([server,'-w'],env=env,cwd=prefix,check=True,timeout=20,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 # Detect replacement while the control operation was in flight.
 p2=os.stat(prefix);tmp2=os.stat('/tmp')
 if (p2.st_dev,p2.st_ino)!=(p.st_dev,p.st_ino) or (tmp2.st_dev,tmp2.st_ino)!=(tmp.st_dev,tmp.st_ino): raise ValueError('namespace changed')
print(json.dumps(proof,separators=(',',':')))
`

func (a Adapter) wineControl(ctx context.Context, r InstallRequest, root, mode string, expected *WineControl) (*WineControl, error) {
	raw := "null"
	if expected != nil {
		b, _ := json.Marshal(expected)
		raw = string(b)
	}
	// Bind the shared namespace to this host runtime root, so changing
	// XDG_RUNTIME_DIR cannot make an unrelated namespace appear stopped.
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	hostScope := ""
	if b, err := host.InspectBinding(runtimeDir, true); err == nil {
		hostScope = fmt.Sprintf("%s:%d:%d", b.Path, b.Device, b.Inode)
	}
	c := command("flatpak", "run", "--command=python3", "com.usebottles.bottles", "-I", "-c", wineControlHelper, mode, root, r.Windows.Bottle.Path, r.Recipe.Runner, raw, hostScope)
	// stop helper has bounded child timeouts. Never terminate it halfway through
	// mutation and release its management lock while completion is uncertain.
	c.SafeCancel = mode == "probe"
	c.Timeout = 0
	if c.SafeCancel {
		c.Timeout = 15 * time.Second
	}
	out, err := a.runner().Run(ctx, c)
	if err != nil {
		return nil, err
	}
	if out.Truncated {
		return nil, errors.New("truncated Wine control proof")
	}
	var proof WineControl
	if json.Unmarshal([]byte(out.Stdout), &proof) != nil || !filepath.IsAbs(proof.Server) || !digestPattern.MatchString(proof.SHA256) || !digestPattern.MatchString(proof.WineSHA256) || proof.Prefix != r.Windows.Bottle.Path || proof.Device != r.Windows.Bottle.Device || proof.Inode != r.Windows.Bottle.Inode || proof.TmpInode == 0 {
		return nil, errors.New("invalid or mismatched Wine control proof")
	}
	if proof.SharedTmpScope != "" && !digestPattern.MatchString(proof.SharedTmpScope) {
		return nil, errors.New("invalid shared Wine namespace proof")
	}
	if expected != nil && proof.persistentIdentity() != expected.persistentIdentity() {
		return nil, errors.New("Wine control evidence changed")
	}
	return &proof, nil
}
func (a Adapter) probeWineControl(ctx context.Context, r InstallRequest, cfg bottleConfig) (*WineControl, string, error) {
	var params map[string]json.RawMessage
	if json.Unmarshal(cfg.Parameters, &params) != nil || string(params["sandbox"]) != "false" || strings.HasPrefix(cfg.Runner, "sys-") || strings.Contains(strings.ToLower(cfg.Runner), "proton") || strings.ContainsAny(cfg.Runner, "/\\") {
		return nil, "", fmt.Errorf("%w: regular managed runner with sandbox=false required for exact Wine control", ErrUnsupported)
	}
	// A different TMPDIR/WINEPREFIX or custom runner launch overrides would select
	// another namespace. Conservative rejection preserves the exact-prefix contract.
	if len(cfg.EnvironmentVariables) > 0 && string(cfg.EnvironmentVariables) != "null" && string(cfg.EnvironmentVariables) != "{}" {
		return nil, "", fmt.Errorf("%w: custom Wine environment requires review", ErrUnsupported)
	}
	root, err := a.runner().Run(ctx, bottles("info", "bottles-path"))
	if err != nil {
		return nil, "", err
	}
	base := strings.TrimSpace(root.Stdout)
	if root.Truncated || !filepath.IsAbs(base) || filepath.Clean(base) != base || filepath.Base(base) != "bottles" {
		return nil, "", errors.New("unsupported Bottles data root")
	}
	proof, err := a.wineControl(ctx, r, base, "probe", nil)
	return proof, base, err
}
func (a Adapter) stopBottles(ctx context.Context, r InstallRequest, id string) (Evidence, error) {
	ev := Evidence{ResourceID: id, State: "unknown"}
	if r.Windows == nil || r.Windows.Control == nil {
		return ev, fmt.Errorf("%w: re-register a supported Bottle to pin its Wine control identity", ErrUnsupported)
	}
	if err := a.verifyBottlesControlTarget(ctx, r, id); err != nil {
		return ev, err
	}
	if _, err := a.wineControl(ctx, r, r.Windows.ControlRoot, "stop", r.Windows.Control); err != nil {
		return ev, err
	}
	ev.State = "stopped"
	return ev, nil
}

// Direct Wine tracking must not delegate to an already-running WineBridge or
// arbitrary environment/launch wrapper. Missing metadata is not proof of false.
func directWineProgram(cfg bottleConfig, program map[string]json.RawMessage) bool {
	var params map[string]json.RawMessage
	if json.Unmarshal(cfg.Parameters, &params) != nil {
		return false
	}
	bridge := params["winebridge"]
	if v, ok := program["winebridge"]; ok && string(v) != "null" {
		bridge = v
	}
	if string(bridge) != "false" {
		return false
	}
	if v := program["environment"]; len(v) > 0 && string(v) != "null" && string(v) != "{}" {
		return false
	}
	for _, key := range []string{"pre_script", "post_script", "launch_options"} {
		if v := program[key]; len(v) > 0 && string(v) != "null" && string(v) != `""` {
			return false
		}
	}
	return true
}

// An application uninstaller can remove the registered EXE. That must not make
// its still-owned Wine environment impossible to stop. Only missing-file drift
// has this fallback; the Wine helper still pins prefix inode, UID, runner hashes
// and Flatpak namespace. It never makes the missing application launchable.
func (a Adapter) verifyBottlesControlTarget(ctx context.Context, r InstallRequest, id string) error {
	_, err := a.InspectBottles(ctx, r, id)
	if err == nil {
		return nil
	}
	old := r.Windows
	if !os.IsNotExist(err) || old == nil || old.Control == nil || old.SchemaVersion != 1 || old.InstanceID != r.InstanceID || old.BottleName != name(r.InstanceID) || old.ResourceIdentity() != id {
		return err
	}
	if _, e := os.Stat(old.ExecutablePath); !os.IsNotExist(e) {
		return err
	}
	return host.VerifyBinding(old.Bottle)
}
