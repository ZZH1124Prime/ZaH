"""Development entry regression tests. No real build, install or GUI launch."""
import json
import os
from pathlib import Path
import socket
import tempfile
import unittest
from unittest.mock import patch, Mock
import private_runtime as private
import private_web_portal as portal
import dev


class DevelopmentRuntime(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='zah-dev-entry-')
        self.root = Path(self.temp.name)

    def tearDown(self):
        self.temp.cleanup()

    def test_runtime_arguments_set_data_inside_sandbox(self):
        original = ['run', '--filesystem=/tmp/owned:rw', '--command=python3', private.APP, '-I', '-c', 'pass']
        result = private.flatpak_args({'data': '/private/data'}, original)
        self.assertIn('--filesystem=/tmp/owned:rw', result)
        self.assertIn('--nofilesystem=~/Games/umu', result)
        self.assertEqual(result[result.index(private.APP)+1:], [
            'XDG_DATA_HOME=/private/data', 'XDG_CONFIG_HOME=/private/data/config',
            'XDG_CACHE_HOME=/private/data/cache', 'GSETTINGS_BACKEND=memory', 'python3', '-I', '-c', 'pass'])
        self.assertEqual(original[2], '--command=python3')

    def test_reject_unintended_app_or_command(self):
        for args in (['run', '--command=sh', private.APP], ['run', '--command=python3', 'other.app'],
                     ['run', '--command=python3', '--command=bottles-cli', private.APP]):
            with self.assertRaises(ValueError): private.flatpak_args({'data': '/private'}, args)

    def test_explicit_missing_profile_fails(self):
        with self.assertRaises(ValueError): dev.configured_environment(self.root / 'missing.json')

    def test_private_environment_keeps_desktop_bus_and_proxy(self):
        profile = self.root / 'profile.json'; profile.write_text('{}')
        wrapper = self.root / 'bin/flatpak'; wrapper.parent.mkdir(); wrapper.write_text('fixture')
        with patch.dict(os.environ, {'DBUS_SESSION_BUS_ADDRESS': 'unix:desktop', 'https_proxy': 'fixture', 'PATH': '/usr/bin'}), \
             patch.object(private, 'load_profile', return_value={'runtime': '/private'}), \
             patch.object(private, 'check_commits'):
            env = dev.configured_environment(profile)
        self.assertEqual(env['DBUS_SESSION_BUS_ADDRESS'], 'unix:desktop')
        self.assertEqual(env['https_proxy'], 'fixture')
        self.assertTrue(env['PATH'].startswith(str(wrapper.parent)+':'))

    def test_invalid_auto_profile_never_silently_uses_system(self):
        profile = self.root / 'profile.json'; profile.write_text('{}')
        with patch.object(private, 'load_profile', side_effect=ValueError('tampered')):
            with self.assertRaisesRegex(ValueError, 'tampered'): dev.configured_environment(profile)

    def test_broken_default_profile_link_never_falls_back(self):
        profile = self.root / 'profile.json'; profile.symlink_to(self.root / 'missing')
        with patch.object(private, 'PROFILE', profile):
            with self.assertRaises(ValueError): dev.configured_environment(profile)

    def test_private_bus_is_temporary_and_failure_propagates(self):
        p = {'runtime':'/private/runtime','flatpak':'/private/repo','data':'/private/data'}
        observed = []
        def invoke(config, command, env):
            observed.append(config)
            self.assertTrue(config.is_file())
            self.assertIn('/private/runtime/share/dbus-1/services', config.read_text())
            self.assertEqual(command[0], '/private/runtime/bin/flatpak')
            return 7
        with patch.object(private, 'load_profile', return_value=p), patch.object(private, 'check_commits'), patch.object(private, 'check_graphics'), \
             patch.object(portal, 'run', side_effect=invoke):
            self.assertEqual(private.invoke_flatpak(self.root/'profile', ['run','--command=python3',private.APP,'-c','pass']), 7)
        self.assertFalse(observed[0].parent.exists())

    def test_graphics_requires_matching_private_nvidia_extension(self):
        profile = {'runtime': '/private/runtime', 'flatpak': '/private/repo'}
        driver = Mock(stdout='nvidia-570-133-07\ndefault\nhost\n', returncode=0)
        with patch.object(private, 'environment', return_value={}), \
             patch.object(private.subprocess, 'run', side_effect=[driver, Mock(returncode=1, stdout='')]) as run:
            with self.assertRaisesRegex(ValueError, 'GL.nvidia-570-133-07/x86_64/1.4'):
                private.check_graphics(profile)
            self.assertEqual(run.call_args.args[0][1:4], ['--user', 'info', '--show-commit'])
        with patch.object(private, 'environment', return_value={}), \
             patch.object(private.subprocess, 'run', side_effect=[driver, Mock(returncode=0, stdout='commit')]):
            private.check_graphics(profile)
        with patch.object(private, 'environment', return_value={}), \
             patch.object(private.subprocess, 'run', return_value=Mock(stdout='default\nhost\n', returncode=0)) as run:
            private.check_graphics(profile)
            self.assertEqual(run.call_count, 1)

    def test_active_manager_prevents_build_or_launch(self):
        (self.root / 'bin').mkdir(); (self.root / 'bin/zah').touch()
        endpoint = self.root / 'manager.sock'
        server = socket.socket(socket.AF_UNIX); server.bind(str(endpoint)); server.listen(1)
        try:
            with patch.object(dev, 'ROOT', self.root), patch.object(dev.subprocess, 'run', return_value=Mock(stdout=json.dumps({'Runtime':str(self.root)}))):
                with self.assertRaisesRegex(RuntimeError, '仍在运行'): dev.refuse_active_manager({})
        finally: server.close()

    def test_check_does_not_build_launch_or_contact_manager(self):
        with patch('sys.argv', ['dev.py', '--check']), patch.object(dev, 'configured_environment', return_value={}), \
             patch.object(dev, 'refuse_active_manager') as manager, patch.object(dev.subprocess, 'call') as call:
            self.assertEqual(dev.main(), 0)
            manager.assert_not_called(); call.assert_not_called()

    def test_prepare_creates_empty_prefix_root_and_excludes_test_data(self):
        cache = self.root / 'cache'; (cache / 'flatpak').mkdir(parents=True)
        src = cache / 'bottles-data/bottles'; (src / 'runners').mkdir(parents=True)
        (src / 'runners/runner').write_text('dependency')
        (src / 'bottles/test-prefix').mkdir(parents=True)
        (src / 'data.yml').write_text('private test metadata')
        target = self.root / 'new'
        with patch.object(private.focal, 'verify', return_value=self.root), patch.object(private, 'load_profile'), patch.object(private, 'check_commits'):
            private.prepare(self.root, cache, target)
            self.assertEqual(list((target / 'data/bottles/bottles').iterdir()), [])
            self.assertTrue((target / 'data/bottles/runners/runner').is_file())
            self.assertFalse((target / 'data/bottles/data.yml').exists())
            with self.assertRaisesRegex(ValueError, 'overwrite'): private.prepare(self.root, cache, target)

    def test_symlink_profile_and_unreviewed_commit_rejected(self):
        p = self.root / 'profile.json'; p.symlink_to(self.root / 'missing')
        with self.assertRaises(ValueError): private.load_profile(p)
        p.unlink(); p.write_text(json.dumps({'schema':1,'runtime':'/unused','flatpak':'/unused','data':'/unused',
            'runner':private.RUNNER,'app_commit':'changed','gnome_commit':private.GNOME_COMMIT})); p.chmod(0o600)
        with self.assertRaisesRegex(ValueError, 'unreviewed'): private.load_profile(p)


if __name__ == '__main__': unittest.main()
