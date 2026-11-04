#!/usr/bin/env python3
"""One development entry: optional build, verified private dependencies, Electron."""
import argparse
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import private_runtime as private

ROOT = Path(__file__).resolve().parent.parent


def configured_environment(profile, system=False):
    env = dict(os.environ)
    if not system and (profile.exists() or profile.is_symlink()):
        p = private.load_profile(profile)
        private.check_commits(p)
        wrapper = profile.parent / 'bin/flatpak'
        if not wrapper.is_file() or wrapper.is_symlink():
            raise ValueError('private Flatpak wrapper missing; prepare the development profile')
        env['PATH'] = str(wrapper.parent) + ':' + env.get('PATH', '/usr/bin:/bin')
        print('ZaH 使用已验证的私有 Windows 依赖：' + p['runtime'], flush=True)
    elif not system and profile != private.PROFILE:
        raise ValueError('selected runtime profile does not exist')
    if not env.get('GO_BINARY') and not shutil.which('go', path=env.get('PATH')):
        for line in (ROOT / 'go.mod').read_text().splitlines():
            if line.startswith('go '):
                candidate = Path.home() / '.local/opt' / ('go' + line.split()[1]) / 'go/bin/go'
                if candidate.is_file(): env['GO_BINARY'] = str(candidate)
                break
    return env


def refuse_active_manager(env):
    binary = ROOT / 'bin/zah'
    if not binary.exists(): return
    result = subprocess.run([str(binary), 'paths'], env=env, capture_output=True, text=True, timeout=10, check=True)
    endpoint = Path(json.loads(result.stdout)['Runtime']) / 'manager.sock'
    client = socket.socket(socket.AF_UNIX); client.settimeout(1)
    try:
        client.connect(str(endpoint))
    except (FileNotFoundError, ConnectionRefusedError): return
    else:
        raise RuntimeError('当前 ZaH 管理服务仍在运行。请在原窗口停止应用并正常关闭，等待约 30 秒后重试；不会覆盖构建或结束已有服务。')
    finally: client.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--build', action='store_true')
    parser.add_argument('--runtime-profile', type=Path, default=private.PROFILE)
    parser.add_argument('--system-runtime', action='store_true')
    parser.add_argument('--check', action='store_true', help='validate configuration only; no build, manager or window')
    args = parser.parse_args()
    env = configured_environment(args.runtime_profile, args.system_runtime)
    if args.check:
        print('开发配置校验通过；未构建或启动窗口。'); return 0
    refuse_active_manager(env)
    if args.build:
        result = subprocess.run(['npm', 'run', 'build'], cwd=ROOT, env=env)
        if result.returncode: return result.returncode
    if not (ROOT / 'bin/zah').is_file() or not (ROOT / 'desktop/dist/main/index.js').is_file() or not (ROOT / 'frontend/dist/index.html').is_file():
        raise RuntimeError('开发产物缺失，请运行 npm run dev 完整构建。')
    return subprocess.call([str(ROOT / 'node_modules/.bin/electron'), '.'], cwd=ROOT, env=env)


if __name__ == '__main__':
    try: sys.exit(main())
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as e:
        print('ZaH: ' + str(e), file=sys.stderr); sys.exit(1)
