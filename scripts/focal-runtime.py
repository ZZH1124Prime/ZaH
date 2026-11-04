#!/usr/bin/env python3
"""Build/check ZaH's Focal Flatpak dependencies without host installation.

Build directories are immutable locations: upstream RPATH/service files contain
absolute paths. This is a developer prerequisite builder, not a ZaH installer.
No sudo, apt install, remote-add, service registration, or app launch occurs.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import posixpath
import shutil
import stat
import subprocess
import sys
import tarfile
import urllib.request

LOCK = Path(__file__).with_name('focal-runtime.lock.json')


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            h.update(chunk)
    return h.hexdigest()


def extract(source, destination):
    """Allow upstream relative symlinks, never archive escapes or link parents."""
    destination.mkdir(parents=True, exist_ok=False)
    with tarfile.open(source) as archive:
        entries = []
        links = set()
        total = 0
        for member in archive.getmembers():
            original = Path(member.name)
            if original.is_absolute() or '..' in original.parts:
                raise ValueError('unsafe archive path')
            parts = original.parts[1:]
            if not parts:
                continue
            if not (member.isfile() or member.isdir() or member.issym()):
                raise ValueError('unsupported archive member')
            member.name = str(Path(*parts))
            total += member.size
            if total > 1024 * 1024 * 1024:
                raise ValueError('source archive too large')
            if member.issym():
                target = posixpath.normpath(posixpath.join(posixpath.dirname(member.name), member.linkname))
                if member.linkname.startswith('/') or target == '..' or target.startswith('../'):
                    raise ValueError('unsafe archive symlink')
                links.add(member.name)
            entries.append(member)
        names = set()
        for member in entries:
            if member.name in names:
                raise ValueError('duplicate archive path')
            names.add(member.name)
            if any(str(p) in links for p in Path(member.name).parents):
                raise ValueError('archive member below symlink')
        for member in entries:
            if not member.issym():
                archive.extract(member, destination)
        for member in entries:
            if member.issym():
                archive.extract(member, destination)


def run(argv, root, name, env=None, cwd=None):
    print(name, flush=True)
    with (root / (name + '.log')).open('w') as log:
        result = subprocess.run([str(x) for x in argv], env=env, cwd=cwd,
                                stdout=log, stderr=subprocess.STDOUT)
    if result.returncode:
        raise RuntimeError('{} failed; see {}'.format(name, root / (name + '.log')))


def base_env():
    env = dict(os.environ)
    for key in list(env):
        if key.startswith(('LD_', 'DYLD_')) or key in ('PYTHONPATH', 'PYTHONHOME'):
            del env[key]
    return env


def inventory(prefix):
    records = {}
    for p in sorted(prefix.rglob('*')):
        name = str(p.relative_to(prefix))
        if name == 'zah-runtime.json':
            continue
        if p.is_symlink():
            target = os.readlink(p)
            resolved = p.resolve(strict=True)
            if prefix not in resolved.parents:
                raise ValueError('runtime link escapes prefix: ' + name)
            records[name] = {'link': target}
        elif p.is_file():
            records[name] = {'sha256': digest(p), 'mode': stat.S_IMODE(p.stat().st_mode)}
    return records


def seal(prefix):
    # Called only after a successful local build; this is an integrity inventory,
    # not a vendor signature or permission to execute somebody else's runtime.
    manifest = {'schema': 1, 'target': 'ubuntu-20.04-x86_64',
                'prefix': str(prefix), 'source_lock_sha256': digest(LOCK),
                'files': inventory(prefix)}
    (prefix / 'zah-runtime.json').write_text(json.dumps(manifest, indent=2) + '\n')
    (prefix / 'zah-runtime.json').chmod(0o600)


def verify(prefix):
    prefix = prefix.absolute()
    for p in (prefix,) + tuple(prefix.parents):
        info = p.lstat()
        if stat.S_ISLNK(info.st_mode) or info.st_uid not in (0, os.getuid()):
            raise ValueError('unsafe runtime directory ownership/link')
        if info.st_mode & 0o022 and not (info.st_mode & stat.S_ISVTX):
            raise ValueError('runtime directory is writable by other users')
    manifest = json.loads((prefix / 'zah-runtime.json').read_text())
    if manifest.get('schema') != 1 or manifest.get('target') != 'ubuntu-20.04-x86_64':
        raise ValueError('unsupported runtime manifest')
    if manifest.get('prefix') != str(prefix) or manifest.get('source_lock_sha256') != digest(LOCK):
        raise ValueError('runtime moved or source lock differs; rebuild it')
    if inventory(prefix) != manifest.get('files'):
        raise ValueError('runtime content changed; do not execute it')
    for p in prefix.rglob('*'):
        info = p.lstat()
        if info.st_uid not in (0, os.getuid()) or (not p.is_symlink() and info.st_mode & 0o022):
            raise ValueError('unsafe runtime file permissions')
    return prefix


def build(root, cache):
    raw = Path('/etc/os-release').read_text()
    if 'ID=ubuntu\n' not in raw or 'VERSION_ID="20.04"' not in raw or platform.machine() != 'x86_64':
        raise RuntimeError('This fixed build is for Ubuntu 20.04 x86_64 only')
    for name in ('cc', 'make', 'ninja', 'pkg-config', 'dpkg-deb', 'apt-get', 'msgfmt', 'nice'):
        if not shutil.which(name):
            raise RuntimeError('Missing build prerequisite: ' + name + '; no host installation performed')
    if not root.is_absolute() or any(c in str(root) for c in '\n\r\x00 :'):
        raise ValueError('Use a new absolute build directory without spaces or colon')
    root.mkdir(parents=False, exist_ok=False)
    root.chmod(0o700)
    os.umask(0o022)  # Upstream installs must not inherit a group-writable umask.
    sources = root / 'sources'; sources.mkdir()
    downloads = root / 'downloads'; downloads.mkdir()
    sysroot = root / 'sysroot'; sysroot.mkdir()
    runtime = root / 'runtime'
    lock = json.loads(LOCK.read_text())
    for item in lock['sources']:
        dest = downloads / item['file']
        cached = cache / item['file'] if cache else None
        if cached and cached.is_file():
            shutil.copyfile(cached, dest)
        else:
            print('download ' + item['url'], flush=True)
            with urllib.request.urlopen(item['url'], timeout=60) as response, dest.open('wb') as output:
                shutil.copyfileobj(response, output)
        if digest(dest) != item['sha256']:
            raise ValueError('source checksum mismatch: ' + item['name'])
        extract(dest, sources / item['destination'])
    for item in lock['build_packages']:
        dest = downloads / item['file']
        cached = cache / 'build-debs' / item['file'] if cache else None
        if cached and cached.is_file():
            shutil.copyfile(cached, dest)
        else:
            run(['apt-get', 'download', item['name'] + '=' + item['version']], root,
                'download-' + item['name'], cwd=downloads)
        if digest(dest) != item['sha256']:
            raise ValueError('build package checksum mismatch: ' + item['name'])
        run(['dpkg-deb', '-x', dest, sysroot], root, 'unpack-' + item['name'])
    for p in sysroot.rglob('*.pc'):
        p.write_text(p.read_text().replace('prefix=/usr', 'prefix=' + str(sysroot / 'usr'))
                     .replace('libdir=/usr', 'libdir=' + str(sysroot / 'usr'))
                     .replace('libdir=/lib', 'libdir=' + str(sysroot / 'lib')))
    for p in sysroot.rglob('*.so'):
        if p.is_symlink() and not p.exists():
            for base in ('/usr/lib/x86_64-linux-gnu', '/lib/x86_64-linux-gnu'):
                target = Path(base) / Path(os.readlink(p)).name
                if target.exists():
                    p.unlink(); p.symlink_to(target); break
    env = base_env()
    env['PATH'] = str(sysroot / 'usr/bin') + ':' + env.get('PATH', '/usr/bin:/bin')
    env['BISON_PKGDATADIR'] = str(sysroot / 'usr/share/bison')
    env['PKG_CONFIG_PATH'] = ':'.join(str(p) for p in (
        runtime / 'lib/pkgconfig', sysroot / 'usr/lib/x86_64-linux-gnu/pkgconfig',
        sysroot / 'usr/share/pkgconfig'))
    meson = [sys.executable, str(sources / 'meson-1.6.1/meson.py')]
    run(meson + ['setup', root / 'build-glib', sources / 'glib-2.88.4',
                 '--prefix=' + str(runtime), '--libdir=lib', '-Dtests=false',
                 '-Dinstalled_tests=false', '-Ddocumentation=false', '-Dintrospection=disabled'], root, 'glib-configure', env)
    run(['nice', '-n', '10', 'ninja', '-C', root / 'build-glib', '-j2'], root, 'glib-build', env)
    run(meson + ['install', '-C', root / 'build-glib', '--no-rebuild'], root, 'glib-install', env)
    env['LD_LIBRARY_PATH'] = str(runtime / 'lib') + ':' + str(sysroot / 'usr/lib/x86_64-linux-gnu')
    env['LDFLAGS'] = '-Wl,-rpath,{} -L{}'.format(runtime / 'lib', runtime / 'lib')
    ostree = root / 'build-ostree'; ostree.mkdir()
    run([sources / 'libostree-2026.5/configure', '--prefix=' + str(runtime), '--libdir=' + str(runtime / 'lib'),
         '--disable-man', '--disable-gtk-doc', '--enable-introspection=no', '--with-curl',
         '--without-soup', '--without-soup3', '--without-avahi', '--without-libsystemd',
         '--with-systemdsystemunitdir=' + str(runtime / 'lib/systemd/system'),
         '--with-systemdsystemgeneratordir=' + str(runtime / 'lib/systemd/system-generators')], root, 'ostree-configure', env, ostree)
    run(['nice', '-n', '10', 'make', '-j2'], root, 'ostree-build', env, ostree)
    run(['make', 'install', 'DESTDIR=' + str(root / 'ostree-stage')], root, 'ostree-install', env, ostree)
    shutil.copytree(root / 'ostree-stage' / str(runtime).lstrip('/'), runtime, dirs_exist_ok=True, symlinks=True)
    link_args = ['-L' + str(sysroot / 'usr/lib/x86_64-linux-gnu'), '-Wl,-rpath,' + str(runtime / 'lib')]
    run(meson + ['setup', root / 'build-flatpak', sources / 'flatpak-1.18.4', '--prefix=' + str(runtime),
                 '--libdir=lib', '--wrap-mode=nodownload', '-Dsystem_helper=disabled', '-Dseccomp=enabled',
                 '-Dgir=disabled', '-Dgtkdoc=disabled', '-Ddocbook_docs=disabled', '-Dman=disabled',
                 '-Dtests=true', '-Dc_args=-I' + str(sysroot / 'usr/include'), '-Dc_link_args=' + json.dumps(link_args)], root, 'flatpak-configure', env)
    run(['nice', '-n', '10', 'ninja', '-C', root / 'build-flatpak', '-j2'], root, 'flatpak-build', env)
    stage_env = dict(env, DESTDIR=str(root / 'flatpak-stage'))
    run(meson + ['install', '-C', root / 'build-flatpak', '--no-rebuild'], root, 'flatpak-install', stage_env)
    shutil.copytree(root / 'flatpak-stage' / str(runtime).lstrip('/'), runtime, dirs_exist_ok=True, symlinks=True)
    seal(runtime)
    verify(runtime)
    checks = base_env()
    for key, directory in [('XDG_DATA_HOME', 'data'), ('XDG_CONFIG_HOME', 'config'),
                           ('XDG_CACHE_HOME', 'cache'), ('XDG_RUNTIME_DIR', 'run')]:
        target = root / 'selftest' / directory
        target.mkdir(parents=True, mode=0o700)
        checks[key] = str(target)
    for binary in ['bin/flatpak', 'bin/ostree', 'libexec/flatpak-bwrap', 'libexec/flatpak-dbus-proxy']:
        run([runtime / binary, '--version'], root, 'version-' + Path(binary).name, checks)
    checks['G_TEST_SRCDIR'] = str(sources / 'flatpak-1.18.4/tests')
    for name in ['test-context', 'test-ref-utils', 'test-exports']:
        run([root / 'build-flatpak/tests' / name], root, name, checks)
    run([runtime / 'libexec/flatpak-bwrap', '--unshare-all', '--die-with-parent', '--new-session',
         '--ro-bind', '/usr', '/usr', '--ro-bind', '/lib', '/lib', '--ro-bind', '/lib64', '/lib64',
         '--proc', '/proc', '--dev', '/dev', '/usr/bin/true'], root, 'sandbox-selftest', checks)
    print('Built private runtime: ' + str(runtime))
    print('Bottles, Flathub, runner and session services are NOT installed or activated.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='action', required=True)
    b = sub.add_parser('build'); b.add_argument('--work-dir', type=Path, required=True); b.add_argument('--cache', type=Path)
    v = sub.add_parser('verify'); v.add_argument('--prefix', type=Path, required=True)
    e = sub.add_parser('exec'); e.add_argument('--prefix', type=Path, required=True); e.add_argument('command', nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if args.action == 'build':
        build(args.work_dir, args.cache)
    else:
        prefix = verify(args.prefix)
        if args.action == 'verify':
            print('Runtime inventory verified (this does not verify Bottles/app compatibility).')
        else:
            command = args.command
            if command[:1] == ['--']: command = command[1:]
            if not command: raise ValueError('command required')
            env = base_env()
            env['PATH'] = str(prefix / 'bin') + ':' + env.get('PATH', '/usr/bin:/bin')
            os.execvpe(command[0], command, env)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
