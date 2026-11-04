#!/usr/bin/env python3
"""Use the verified Focal runtime without changing the host desktop session.

This is a development adapter, not an installer or a relocatable distribution.
Only explicit prepare copies an already authorized cache; launch never downloads.
"""
import argparse
import configparser
import importlib.util
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile
from xml.sax.saxutils import escape

BASE = Path.home() / '.local/share/zah-focal-desktop'
PROFILE = BASE / 'profile.json'
APP = 'com.usebottles.bottles'
APP_COMMIT = '252c59e4451929e2e18e788f196a892c3d3c8c85231869c5fed0992f99fa016e'
GNOME_COMMIT = '4337a389125f814785bd83f4ace01da5b6baa80fdcf5a9c81e60dcd03896c067'
RUNNER = 'soda-11.0-10'
spec = importlib.util.spec_from_file_location('focal_runtime', Path(__file__).with_name('focal-runtime.py'))
focal = importlib.util.module_from_spec(spec)
spec.loader.exec_module(focal)


def owned_directory(path):
    if not path.is_absolute() or path.resolve() != path:
        raise ValueError('dependency path must be absolute and have no symlink ancestors')
    for p in (path,) + tuple(path.parents):
        s = p.lstat()
        if not stat.S_ISDIR(s.st_mode) or s.st_uid not in (0, os.getuid()):
            raise ValueError('unsafe dependency directory owner')
        if s.st_mode & 0o022 and not s.st_mode & stat.S_ISVTX:
            raise ValueError('dependency directory is writable by other users: ' + str(p))


def load_profile(path):
    owned_directory(path.parent)
    s = path.lstat()
    if not stat.S_ISREG(s.st_mode) or s.st_uid != os.getuid() or s.st_mode & 0o077 or s.st_size > 16384:
        raise ValueError('unsafe development runtime profile')
    p = json.loads(path.read_text())
    if set(p) != {'schema', 'runtime', 'flatpak', 'data', 'app_commit', 'gnome_commit', 'runner'} or p['schema'] != 1:
        raise ValueError('unsupported development runtime profile')
    if (p['app_commit'], p['gnome_commit'], p['runner']) != (APP_COMMIT, GNOME_COMMIT, RUNNER):
        raise ValueError('unreviewed application/runtime version; do not silently switch')
    focal.verify(Path(p['runtime']))
    for key in ('flatpak', 'data'):
        owned_directory(Path(p[key]))
    cfg = configparser.ConfigParser(interpolation=None)
    cfg.read(Path(p['flatpak']) / 'repo/config')
    remote = cfg['remote "flathub"']
    if remote.get('url') != 'https://dl.flathub.org/repo/' or not remote.getboolean('gpg-verify') or not remote.getboolean('gpg-verify-summary'):
        raise ValueError('official repository verification must remain enabled')
    return p


def environment(p):
    env = focal.base_env()
    env['FLATPAK_USER_DIR'] = p['flatpak']
    return env


def check_commits(p):
    for ref, expected in ((APP, p['app_commit']), ('org.gnome.Platform//50', p['gnome_commit'])):
        result = subprocess.run([p['runtime'] + '/bin/flatpak', '--user', 'info', '--show-commit', ref],
                                env=environment(p), capture_output=True, text=True, timeout=30, check=True)
        if result.stdout.strip() != expected:
            raise ValueError('installed commit changed for ' + ref)


def check_graphics(p):
    # NVIDIA userspace must match the loaded host driver. Never download or
    # fall back to another installation during launch; report the exact missing ref.
    exe = p['runtime'] + '/bin/flatpak'
    drivers = subprocess.run([exe, '--gl-drivers'], env=environment(p),
                             capture_output=True, text=True, timeout=30, check=True)
    for driver in drivers.stdout.split():
        if not driver.startswith('nvidia-'):
            continue
        if not all(part.isdigit() for part in driver[7:].split('-')):
            raise ValueError('unrecognized NVIDIA driver identifier')
        ref = 'org.freedesktop.Platform.GL.' + driver + '/x86_64/1.4'
        installed = subprocess.run([exe, '--user', 'info', '--show-commit', ref],
                                   env=environment(p), capture_output=True, text=True, timeout=30)
        if installed.returncode or not installed.stdout.strip():
            raise ValueError('Private Flatpak NVIDIA extension missing: ' + ref +
                             '; prepare this matching extension before launching Windows apps')


def flatpak_args(p, args):
    if not args or args[0] != 'run':
        return args
    if args.count(APP) != 1:
        raise ValueError('private adapter runs only Bottles')
    index = args.index(APP)
    options = args[1:index]
    commands = [x for x in options if x.startswith('--command=')]
    if len(commands) != 1 or commands[0].split('=', 1)[1] not in ('python3', 'bottles-cli'):
        raise ValueError('explicit supported Bottles command required')
    command = commands[0].split('=', 1)[1]
    options = [x for x in options if x != commands[0]]
    data = p['data']
    return ['run', '--nofilesystem=~/Games/umu', '--nofilesystem=xdg-data/umu',
            '--filesystem=' + data + ':rw'] + options + ['--command=env', APP,
            'XDG_DATA_HOME=' + data, 'XDG_CONFIG_HOME=' + data + '/config',
            'XDG_CACHE_HOME=' + data + '/cache', 'GSETTINGS_BACKEND=memory', command] + args[index+1:]


def invoke_flatpak(profile, args):
    p = load_profile(profile)
    command = [p['runtime'] + '/bin/flatpak', '--user'] + flatpak_args(p, args)
    env = environment(p)
    if not args or args[0] != 'run':
        return subprocess.call(command, env=env)
    check_commits(p)
    check_graphics(p)
    if not Path('/usr/bin/dbus-daemon').is_file():
        raise RuntimeError('dbus-daemon missing; no system software was installed')
    try:
        import private_web_portal
    except ImportError as error:
        raise RuntimeError('existing system Python Gio bindings required for web opening') from error
    # The bus belongs to this invocation only. Electron and the host desktop
    # retain their original DBUS_SESSION_BUS_ADDRESS; no activation file is installed.
    with tempfile.TemporaryDirectory(prefix='zah-private-bus-') as temp:
        config = Path(temp) / 'bus.conf'
        config.write_text('<busconfig><type>session</type><listen>unix:tmpdir=' + escape(temp) +
            '</listen><auth>EXTERNAL</auth><servicedir>' + escape(p['runtime'] + '/share/dbus-1/services') +
            '</servicedir><policy context="default"><allow send_destination="*"/>' +
            '<allow receive_sender="*"/><allow own="*"/></policy></busconfig>')
        return private_web_portal.run(config, command, env)


def prepare(runtime, cache, destination):
    runtime = focal.verify(runtime)
    owned_directory(destination.parent)
    if destination.exists():
        raise ValueError('destination exists; refusing overwrite')
    # Cache has already been downloaded/authorized. Copy links as links: Flatpak
    # payloads contain sandbox-absolute /app and /usr links, never follow them here.
    destination.mkdir(mode=0o700)
    shutil.copytree(cache / 'flatpak', destination / 'flatpak', symlinks=True)
    data = destination / 'data'
    (data / 'bottles').mkdir(parents=True, mode=0o700)
    for name in ('runners', 'dxvk', 'vkd3d', 'nvapi', 'latencyflex', 'd7vk', 'runtimes', 'winebridge', 'templates', 'temp'):
        source = cache / 'bottles-data/bottles' / name
        if source.is_dir() and not source.is_symlink():
            shutil.copytree(source, data / 'bottles' / name, symlinks=True)
    # Never copy test Bottle prefixes, user profiles, journal or restoration stages.
    for name in ('config', 'cache'):
        (data / name).mkdir(mode=0o700)
    # Bottles CLI assumes this directory exists even for an empty installation.
    # Create an empty root; never copy validation prefixes into the user profile.
    (data / 'bottles/bottles').mkdir(mode=0o700)
    for root in (destination / 'flatpak', data, data / 'bottles'):
        root.chmod(0o700)
    p = {'schema': 1, 'runtime': str(runtime), 'flatpak': str(destination / 'flatpak'),
         'data': str(data), 'app_commit': APP_COMMIT, 'gnome_commit': GNOME_COMMIT, 'runner': RUNNER}
    profile = destination / 'profile.json'
    profile.write_text(json.dumps(p, indent=2) + '\n'); profile.chmod(0o600)
    load_profile(profile); check_commits(p)
    bin_dir = destination / 'bin'; bin_dir.mkdir(mode=0o700)
    wrapper = bin_dir / 'flatpak'
    wrapper.write_text('#!' + sys.executable + '\nimport os,sys\nos.execv(' + repr(sys.executable) +
        ',[' + repr(sys.executable) + ',' + repr(str(Path(__file__).resolve())) +
        ',"flatpak","--profile",' + repr(str(profile)) + ',"--"]+sys.argv[1:])\n')
    wrapper.chmod(0o700)
    print('Prepared fixed development profile: ' + str(profile))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='action', required=True)
    setup = sub.add_parser('prepare'); setup.add_argument('--runtime', type=Path, required=True)
    setup.add_argument('--cache', type=Path, required=True); setup.add_argument('--destination', type=Path, default=BASE)
    run = sub.add_parser('flatpak'); run.add_argument('--profile', type=Path, default=PROFILE); run.add_argument('args', nargs=argparse.REMAINDER)
    check = sub.add_parser('check'); check.add_argument('--profile', type=Path, default=PROFILE)
    args = parser.parse_args()
    if args.action == 'prepare': prepare(args.runtime, args.cache, args.destination)
    elif args.action == 'check':
        p = load_profile(args.profile); check_commits(p); print(json.dumps(p, indent=2))
    else:
        argv = args.args[1:] if args.args[:1] == ['--'] else args.args
        return invoke_flatpak(args.profile, argv)
    return 0


if __name__ == '__main__':
    try: sys.exit(main())
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.SubprocessError) as e:
        print('ZaH private runtime: ' + str(e), file=sys.stderr); sys.exit(1)
