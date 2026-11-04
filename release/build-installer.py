#!/usr/bin/env python3
"""Build a per-user Z1/Focal installer from current npm build outputs."""
import hashlib,json,os,shutil,stat,tarfile,tempfile
from pathlib import Path
ROOT=Path(__file__).resolve().parent.parent
VERSION='0.1.0-preview.20261104.4'
OUT=ROOT/'release'/('ZaH-'+VERSION+'-ubuntu20.04-amd64.run')
LAUNCH='''#!/usr/bin/python3
import os,sys
from pathlib import Path
root=Path(__file__).resolve().parent
sys.path.insert(0,str(root/'scripts'))
import private_runtime as private
profile=private.PROFILE
if profile.exists():
    private.check_commits(private.load_profile(profile))
env=dict(os.environ)
env['PATH']=str(root/'wrappers')+':'+str(Path.home()/'.local/bin')+':'+env.get('PATH','/usr/bin:/bin')
os.execve(str(root/'zah-desktop'),[str(root/'zah-desktop')]+sys.argv[1:],env)
'''
WRAPPER='''#!/usr/bin/python3
import os,sys
from pathlib import Path
script=Path(__file__).resolve().parent.parent/'scripts/private_runtime.py'
os.execv('/usr/bin/python3',['/usr/bin/python3',str(script),'flatpak','--']+sys.argv[1:])
'''
INSTALL='''#!/usr/bin/python3
import os,shutil,sys
from pathlib import Path
src=Path(__file__).resolve().parent/'app'
home=Path.home(); base=home/'.local/opt/zah'; target=base/'VERSION_VALUE'
binpath=home/'.local/bin/zah'; desktop=home/'.local/share/applications/zah.desktop'
if os.getuid()==0: raise SystemExit('Run this installer as your normal desktop user, not root.')
for p in (target,):
    if p.exists() or p.is_symlink(): raise SystemExit('Destination already exists; retained without changes: '+str(p))
if binpath.exists() or binpath.is_symlink():
    if not binpath.is_symlink() or binpath.resolve().parent.parent != base or binpath.resolve().name != 'launch.py': raise SystemExit('Existing launcher is not owned by this installer: '+str(binpath))
if desktop.exists() and 'Comment=Application workbench' not in desktop.read_text(): raise SystemExit('Existing desktop entry is not owned by this installer: '+str(desktop))
base.mkdir(parents=True,exist_ok=True)
shutil.copytree(src,target,symlinks=True)
binpath.parent.mkdir(parents=True,exist_ok=True)
if binpath.is_symlink(): binpath.unlink()
binpath.symlink_to(target/'launch.py')
desktop.parent.mkdir(parents=True,exist_ok=True)
launcher=str(target/'launch.py').replace('\\\\','\\\\\\\\').replace('"','\\\\"').replace('`','\\\\`').replace('$','\\\\$').replace('%','%%')
desktop.write_text('[Desktop Entry]\\nType=Application\\nName=ZaH\\nComment=Application workbench\\nExec="'+launcher+'"\\nIcon=applications-system\\nTerminal=false\\nCategories=Utility;\\nStartupNotify=true\\n')
desktop.chmod(0o644)
print('Installed: '+str(target));print('Start: '+str(binpath))
print('Existing application data and Windows runtime were retained.')
'''.replace('VERSION_VALUE',VERSION)
HEADER='''#!/bin/sh
set -eu
if [ "${1:-}" = "--help" ]; then
  echo 'ZaH per-user installer for Ubuntu 20.04 amd64. Run without sudo.'
  echo 'Installs ~/.local/opt/zah and a user application-menu entry.'
  echo 'Windows dependencies: use the existing dependency-install screen; downloads and build prerequisites are required.'
  exit 0
fi
if [ "$#" -ne 0 ]; then echo 'Usage: this-installer [--help]' >&2; exit 2; fi
stage=$(mktemp -d "${TMPDIR:-/tmp}/zah-install.XXXXXXXX")
trap 'rm -rf -- "$stage"' EXIT HUP INT TERM
line=$(awk '/^__ZAH_PAYLOAD__$/ { print NR + 1; exit }' "$0")
tail -n +"$line" "$0" | tar -xz -C "$stage"
python3 "$stage/install.py"
exit 0
__ZAH_PAYLOAD__
'''
with tempfile.TemporaryDirectory(prefix='zah-package-') as temp:
    stage=Path(temp); app=stage/'app'
    shutil.copytree(ROOT/'node_modules/electron/dist',app,symlinks=True)
    (app/'electron').rename(app/'zah-desktop')
    default=app/'resources/default_app.asar'
    if default.exists(): default.unlink()
    resource=app/'resources/app'; resource.mkdir(parents=True)
    (resource/'package.json').write_text(json.dumps({'name':'zah','version':'0.1.0','main':'desktop/dist/main/index.js'}))
    for sub in ('main','preload','protocol','shared'):
        p=ROOT/'desktop/dist'/sub
        if p.exists(): shutil.copytree(p,resource/'desktop/dist'/sub)
    shutil.copytree(ROOT/'frontend/dist',resource/'frontend/dist')
    shutil.copy2(ROOT/'bin/zah',app/'resources/zah')
    (app/'scripts').mkdir();(app/'wrappers').mkdir()
    for name in ('private_runtime.py','private_web_portal.py','focal-runtime.py','focal-runtime.lock.json','prepare-windows.py'):
        shutil.copy2(ROOT/'scripts'/name,app/'scripts'/name)
    (app/'launch.py').write_text(LAUNCH);(app/'wrappers/flatpak').write_text(WRAPPER)
    (stage/'install.py').write_text(INSTALL)
    (app/'INSTALLATION.txt').write_text('Z1 Ubuntu 20.04 amd64 preview. Bundles Electron, static Go backend, compiled UI, fonts and private runtime adapters. The existing dependency-install screen prepares private Flatpak/Bottles/Soda/NVIDIA dependencies when prerequisites and official downloads are available, or shows actionable errors. Existing profiles are verified and retained. No user app data, credentials or test bottles are bundled. Does not install system packages or alter networking. Existing data remains in normal XDG paths. Not a clean-machine offline Windows-dependency bundle. PotPlayer first-open and embedded Wanda GraphicsCapture limitations remain; see bundled acceptance notes.\n')
    shutil.copy2(ROOT/'docs/windows-user-acceptance-20261104.md',app/'ACCEPTANCE.md')
    shutil.copytree(ROOT/'docs/licenses',app/'licenses')
    (app/'licenses/fonts').mkdir()
    for name in ('LICENSE.txt','DejaVu-LICENSE.txt','SOURCE.txt'):
        shutil.copy2(ROOT/'internal/backend/fonts'/name,app/'licenses/fonts'/name)
    for p in app.rglob('*'):
        if p.is_symlink(): continue
        if p.is_dir(): p.chmod(0o755)
        else: p.chmod(0o755 if p.stat().st_mode & 0o111 or p.name=='launch.py' or p==app/'wrappers/flatpak' else 0o644)
    payload=stage/'payload.tar.gz'
    with tarfile.open(payload,'w:gz',compresslevel=6) as tar:
        tar.add(app,arcname='app');tar.add(stage/'install.py',arcname='install.py')
    with OUT.open('wb') as f:
        f.write(HEADER.encode())
        with payload.open('rb') as src: shutil.copyfileobj(src,f)
OUT.chmod(0o755)
h=hashlib.sha256(OUT.read_bytes()).hexdigest()
OUT.with_suffix('.run.sha256').write_text(h+'  '+OUT.name+'\n')
print(OUT);print(OUT.stat().st_size);print(h)
