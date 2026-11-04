#!/usr/bin/python3
"""Prepare the existing private Focal backend; no host package/network changes."""
import json, os, platform, shlex, shutil, subprocess, sys, urllib.request
from pathlib import Path
import private_runtime as private

SCRIPT = Path(__file__).resolve()
PREREQUISITES = 'build-essential ninja-build pkg-config gettext python3-gi libglib2.0-dev libffi-dev libpcre2-dev zlib1g-dev libmount-dev libselinux1-dev libcurl4-openssl-dev libgpgme-dev liblzma-dev libzstd-dev liblz4-dev libxml2-dev libgdk-pixbuf2.0-dev libxau-dev dbus-x11 ca-certificates libappstream4 libjson-glib-1.0-0 libarchive13 libseccomp2 libsoup2.4-1'.split()

def run(args, env=None):
    print('+ ' + shlex.join([str(a) for a in args]), flush=True)
    subprocess.run([str(a) for a in args], env=env, check=True)

def main():
    if sys.argv[1:] != ['--install']:
        raise ValueError('Usage: python3 ' + shlex.quote(str(SCRIPT)) + ' --install')
    if os.getuid() == 0:
        raise ValueError('请以普通桌面用户运行，不要 sudo 整个脚本。')
    raw = Path('/etc/os-release').read_text()
    if 'ID=ubuntu\n' not in raw or 'VERSION_ID="20.04"' not in raw or platform.machine() != 'x86_64':
        raise ValueError('此准备命令仅适用于 Ubuntu 20.04 amd64。')
    os.umask(0o022)
    if private.PROFILE.exists():
        p = private.load_profile(private.PROFILE)
        private.check_commits(p); private.check_graphics(p)
        if not (Path(p['data']) / 'bottles/runners' / private.RUNNER / 'bin/wine').is_file():
            raise ValueError('已有配置缺少 Soda；保留已有环境，未覆盖。')
        print('已有私有 Flatpak、Bottles、Soda 和显卡扩展检查通过；未重装。', flush=True)
        return
    missing = []
    for package in PREREQUISITES:
        r = subprocess.run(['dpkg-query', '-W', '-f=${Status}', package], capture_output=True, text=True)
        if r.returncode or r.stdout != 'install ok installed': missing.append(package)
    if missing:
        print('缺少系统编译依赖。请在终端执行以下命令，检查系统提示后再重试：', flush=True)
        print('sudo apt-get --no-remove --no-upgrade --no-install-recommends install ' + ' '.join(missing), flush=True)
        raise ValueError('未自动修改系统软件包；若软件源失效，请自行处理。')
    root = Path.home() / '.local/share/zah-focal-runtime-1.18.4'
    root.parent.mkdir(parents=True, exist_ok=True)
    private.owned_directory(root.parent)
    if not root.exists():
        private.focal.build(root, None)
    runtime = private.focal.verify(root / 'runtime')
    base = private.BASE
    marker = base / '.zah-setup'
    if base.exists() and not marker.is_file():
        raise ValueError('目标目录已有数据，拒绝覆盖：' + str(base))
    base.mkdir(mode=0o700, exist_ok=True)
    private.owned_directory(base)
    marker.touch(mode=0o600)
    p = {'schema': 1, 'runtime': str(runtime), 'flatpak': str(base/'flatpak'), 'data': str(base/'data'),
         'app_commit': private.APP_COMMIT, 'gnome_commit': private.GNOME_COMMIT, 'runner': private.RUNNER}
    for sub in ('flatpak', 'data', 'data/config', 'data/cache', 'data/bottles', 'data/bottles/bottles', 'data/bottles/runners', 'data/bottles/temp', 'bin'):
        (base/sub).mkdir(parents=True, mode=0o700, exist_ok=True)
    env = private.environment(p)
    fp = [runtime/'bin/flatpak', '--user']
    run(fp + ['remote-add', '--if-not-exists', 'flathub', 'https://dl.flathub.org/repo/flathub.flatpakrepo'], env)
    for ref, commit in [('org.gnome.Platform/x86_64/50', private.GNOME_COMMIT),
                        ('com.usebottles.bottles/x86_64/stable', private.APP_COMMIT)]:
        run(fp + ['install', '--noninteractive', '-y', 'flathub', ref], env)
        run(fp + ['update', '--noninteractive', '-y', '--commit='+commit, ref], env)
    drivers = subprocess.check_output([str(runtime/'bin/flatpak'), '--gl-drivers'], env=env, text=True).split()
    for driver in drivers:
        if driver.startswith('nvidia-') and all(x.isdigit() for x in driver[7:].split('-')):
            run(fp + ['install', '--noninteractive', '-y', 'flathub', 'org.freedesktop.Platform.GL.'+driver+'/x86_64/1.4'], env)
    runner = base/'data/bottles/runners'/private.RUNNER
    if not runner.exists():
        archive = base/'data/bottles/temp/soda-11.0-10-x86_64.tar.xz'
        digest = '14633fabd180d308d2c1ebcb84a16f36d0484dded5809c64f8a7c93e654d7296'
        if not archive.exists() or private.focal.digest(archive) != digest:
            print('下载官方 Soda 11.0-10（约 174 MB）…', flush=True)
            with urllib.request.urlopen('https://github.com/bottlesdevs/wine/releases/download/soda-11.0-10/soda-11.0-10-x86_64.tar.xz', timeout=60) as src, archive.open('wb') as dst:
                shutil.copyfileobj(src, dst)
        if private.focal.digest(archive) != digest: raise ValueError('Soda 下载摘要不匹配。')
        private.focal.extract(archive, runner)
    if not (runner/'bin/wine').is_file(): raise ValueError('Soda 目录不完整；未宣布安装成功。')
    pending = base/'profile.pending.json'
    pending.write_text(json.dumps(p, indent=2)+'\n'); pending.chmod(0o600)
    private.load_profile(pending); private.check_commits(p); private.check_graphics(p)
    if private.invoke_flatpak(pending, ['run', '--command=bottles-cli', private.APP, 'list', 'bottles']):
        raise ValueError('Bottles CLI 启动失败；请检查上方日志。')
    wrapper = base/'bin/flatpak'
    wrapper.write_text('#!/usr/bin/python3\nimport os,sys\nos.execv("/usr/bin/python3",["/usr/bin/python3",'+repr(str(SCRIPT.with_name('private_runtime.py')))+',"flatpak","--"]+sys.argv[1:])\n')
    wrapper.chmod(0o700)
    pending.rename(private.PROFILE)
    print('基础运行时准备完成。请正常关闭并重新打开 ZaH；创建应用环境时 Bottles 仍可能下载图形组件、Mono、Gecko 等。任意应用兼容性不在此检查范围。', flush=True)

if __name__ == '__main__':
    try: main()
    except Exception as error:
        print('准备未完成：'+str(error), file=sys.stderr, flush=True)
        print('终端重试命令：python3 '+shlex.quote(str(SCRIPT))+' --install', file=sys.stderr, flush=True)
        sys.exit(1)
