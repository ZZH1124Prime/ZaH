"""Targeted private portal checks: isolated buses, fake opener, no real apps."""
import os
import io
from contextlib import redirect_stderr
from pathlib import Path
import signal
import subprocess
import tempfile
import time
import unittest
from unittest.mock import patch
from xml.sax.saxutils import escape

import private_web_portal as portal
from private_web_portal import Gio, GLib


def bus_config(root):
    config = root / 'bus.conf'
    config.write_text('<busconfig><type>session</type><listen>unix:tmpdir=' + escape(str(root)) +
        '</listen><auth>EXTERNAL</auth><policy context="default"><allow send_destination="*"/>'
        '<allow receive_sender="*"/><allow own="*"/></policy></busconfig>')
    return config


def wait_for(predicate, timeout=3):
    deadline = time.monotonic() + timeout
    context = GLib.MainContext.default()
    while not predicate():
        if time.monotonic() >= deadline:
            raise AssertionError('Timed out waiting for test event')
        context.iteration(False)
        time.sleep(0.002)
    while context.pending():
        context.iteration(False)


class Validation(unittest.TestCase):
    def test_regular_sites_queries_ipv6_and_no_domain_allowlist(self):
        for uri in ('https://example.com/a?x=1&redirect=http%3A%2F%2Flocalhost%3A3123#part',
                    'http://127.0.0.1:3123/callback?code=fixture', 'https://[::1]:443/',
                    'https://arbitrary-site.test/%E4%B8%AD%E6%96%87', 'https://xn--fiqs8s.test/'):
            self.assertEqual(portal.validate_uri(uri), uri)

    def test_files_commands_custom_schemes_and_ambiguous_addresses_rejected(self):
        for uri in ('file:///etc/passwd', 'javascript:alert(1)', 'qqmusic://login', 'mailto:a@b',
                    'https://good.test\\@evil.test', 'https://user:pw@host.test/',
                    'https:///path', '//example.com', 'https://host:99999', 'https://host:',
                    'https://[::1]evil', 'https://host/%0a', 'https://host/%zz',
                    'https://host/\n', 'https://host/\u202e', 'https://host/<b>', 'https://' + 'a'*8192):
            with self.subTest(uri=uri[:100]), self.assertRaises(ValueError):
                portal.validate_uri(uri)

    def test_child_environment_strips_all_host_bus_addresses(self):
        source = {'DBUS_SESSION_BUS_ADDRESS': 'host', 'DBUS_STARTER_ADDRESS': 'host',
                  'DBUS_SOMETHING': 'host', 'https_proxy': 'unchanged', 'DISPLAY': ':0'}
        self.assertEqual(portal.child_environment(source), {'https_proxy': 'unchanged', 'DISPLAY': ':0'})
        self.assertEqual(source['DBUS_SESSION_BUS_ADDRESS'], 'host')

    def test_launcher_keeps_exact_uri_and_async_failure(self):
        uri = 'https://arbitrary.test/a?x=1&y=2#z'
        results = []
        with patch.object(Gio.AppInfo, 'launch_default_for_uri_async') as launch, \
             patch.object(Gio.AppInfo, 'launch_default_for_uri_finish', return_value=True):
            cancellable = Gio.Cancellable()
            portal.launch_uri(uri, cancellable, results.append)
            self.assertEqual(launch.call_args[0][:3], (uri, None, cancellable))
            launch.call_args[0][3](None, None)
            self.assertEqual(results, [True])


class Protocol(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='zah-portal-test-')
        self.daemon, self.address = portal.start_bus(bus_config(Path(self.temp.name)), dict(os.environ))
        self.server = portal.connect(self.address)
        self.client = portal.connect(self.address)
        self.launched, self.responses = [], []
        def launch(uri, cancellable, callback):
            self.launched.append((uri, cancellable, callback))
        self.service = portal.WebPortal(self.server, launch=launch)
        self.subscription = self.client.signal_subscribe(None, portal.REQUEST, 'Response', None, None,
            Gio.DBusSignalFlags.NONE, lambda _c, _s, path, _i, _n, params: self.responses.append((path, params.unpack()[0])))

    def tearDown(self):
        self.service.shutdown()
        self.client.signal_unsubscribe(self.subscription)
        if not self.client.is_closed():
            self.client.close_sync(None)
        self.server.close_sync(None)
        portal.stop_process(self.daemon)
        self.temp.cleanup()

    def call(self, method, params, interface=portal.OPEN, path=portal.PATH, client=None):
        client = client or self.client
        result = []
        def done(connection, value):
            try:
                result.append(connection.call_finish(value))
            except GLib.Error as error:
                result.append(error)
        client.call(portal.NAME, path, interface, method, params, None,
                    Gio.DBusCallFlags.NONE, 2000, None, done)
        wait_for(lambda: result)
        if isinstance(result[0], GLib.Error):
            raise result[0]
        return result[0].unpack()

    def request(self, uri='https://example.test/a?x=1&y=2#part', token='test_token'):
        result = self.call('OpenURI', GLib.Variant('(ssa{sv})', ('', uri, {'handle_token': GLib.Variant('s', token)})))
        wait_for(lambda: self.launched)
        expected = portal.PATH + '/request/' + self.client.get_unique_name()[1:].replace('.', '_') + '/' + token
        self.assertEqual(result, (expected,))
        return expected

    def test_direct_exact_address_only_once_and_success_response(self):
        path = self.request()
        self.service.dispatch(path)
        self.assertEqual(len(self.launched), 1)
        self.assertEqual(self.launched[0][0], 'https://example.test/a?x=1&y=2#part')
        self.launched[0][2](True)
        wait_for(lambda: self.responses)
        self.assertEqual(self.responses, [(path, 0)])
        self.assertFalse(self.service.pending)

    def test_request_close_cancels_and_has_no_response(self):
        path = self.request()
        self.call('Close', GLib.Variant('()', ()), portal.REQUEST, path)
        self.assertTrue(self.launched[0][1].is_cancelled())
        self.launched[0][2](True)
        self.assertEqual(self.responses, [])
        self.assertFalse(self.service.pending)

    def test_other_connection_cannot_close_request(self):
        path = self.request()
        other = portal.connect(self.address)
        try:
            with self.assertRaisesRegex(GLib.Error, 'NotAllowed'):
                self.call('Close', GLib.Variant('()', ()), portal.REQUEST, path, other)
            self.assertTrue(self.service.pending)
        finally:
            other.close_sync(None)

    def test_requester_disconnect_keeps_accepted_launch_alive(self):
        path = self.request()
        self.client.close_sync(None)
        wait_for(lambda: not self.service.pending[path]['connected'])
        self.assertFalse(self.launched[0][1].is_cancelled())
        self.launched[0][2](True)
        self.assertFalse(self.service.pending)
        self.assertEqual(self.responses, [])

    def test_timeout_and_late_completion(self):
        self.service.timeout = 1
        path = self.request()
        wait_for(lambda: self.responses, 4)
        self.launched[0][2](True)
        self.assertEqual(self.responses, [(path, 2)])
        self.assertTrue(self.launched[0][1].is_cancelled())

    def test_browser_failure_is_not_success(self):
        path = self.request()
        self.launched[0][2](False)
        wait_for(lambda: self.responses)
        self.assertEqual(self.responses, [(path, 2)])

    def test_query_fragment_and_request_token_are_not_logged(self):
        output = io.StringIO()
        with redirect_stderr(output):
            self.request('https://example.test/private?token=SECRET_QUERY#SECRET_FRAGMENT', 'SECRET_TOKEN')
            self.launched[0][2](True)
        self.assertNotIn('SECRET', output.getvalue())
        self.assertNotIn('/private', output.getvalue())
        self.assertIn('request-received', output.getvalue())
        self.assertIn('request-finished', output.getvalue())

    def test_invalid_uri_options_and_non_web_interfaces_rejected(self):
        for uri, options in (('file:///etc/passwd', {}), ('qqmusic://login', {}),
                ('https://example.test/', {'handle_token': GLib.Variant('s', '../bad')}),
                ('https://example.test/', {'writable': GLib.Variant('b', True)}),
                ('https://example.test/', {'command': GLib.Variant('s', 'sh')})):
            with self.assertRaisesRegex(GLib.Error, 'InvalidArgument'):
                self.call('OpenURI', GLib.Variant('(ssa{sv})', ('', uri, options)))
        with self.assertRaises(GLib.Error):
            self.call('OpenFile', GLib.Variant('(sha{sv})', ('', 0, {})))
        with self.assertRaises(GLib.Error):
            self.call('OpenDirectory', GLib.Variant('(sha{sv})', ('', 0, {})))
        self.assertEqual(self.launched, [])
        self.assertEqual(self.call('Get', GLib.Variant('(ss)', (portal.OPEN, 'version')),
                                  'org.freedesktop.DBus.Properties'), (1,))

    def test_concurrent_requests_are_allowed_and_bounded(self):
        for i in range(portal.MAX_PENDING):
            self.request(token='request_' + str(i))
        with self.assertRaisesRegex(GLib.Error, 'NotAllowed'):
            self.call('OpenURI', GLib.Variant('(ssa{sv})', ('', 'https://other.test/', {})))
        self.assertEqual(len(self.launched), portal.MAX_PENDING)

    def test_real_single_shot_gdbus_client_can_exit_before_browser_completion(self):
        env = portal.child_environment(os.environ)
        env['DBUS_SESSION_BUS_ADDRESS'] = self.address
        child = subprocess.Popen(['/usr/bin/gdbus', 'call', '--session', '--dest', portal.NAME,
            '--object-path', portal.PATH, '--method', portal.OPEN + '.OpenURI', '',
            'https://example.invalid/?code=fixture', '{}'], env=env,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            wait_for(lambda: child.poll() is not None)
            self.assertEqual(child.returncode, 0)
            wait_for(lambda: self.launched)
            path = next(iter(self.service.pending))
            wait_for(lambda: not self.service.pending[path]['connected'])
            self.assertFalse(self.launched[0][1].is_cancelled())
            self.launched[0][2](True)
            self.assertFalse(self.service.pending)
        finally:
            portal.stop_process(child)
            child.communicate()


class Lifecycle(unittest.TestCase):
    def test_supervisor_termination_stops_its_fixture_child_and_bus(self):
        with tempfile.TemporaryDirectory(prefix='zah-portal-signal-') as temp:
            root = Path(temp)
            config = bus_config(root)
            output = root / 'address'
            child_pid = root / 'child-pid'
            fixture = "import os,sys,time;from pathlib import Path;Path(sys.argv[1]).write_text(os.environ['DBUS_SESSION_BUS_ADDRESS']);Path(sys.argv[2]).write_text(str(os.getpid()));time.sleep(30)"
            supervisor = "import os,sys;import private_web_portal as p;sys.exit(p.run(sys.argv[1], ['/usr/bin/python3','-B','-c',sys.argv[2],sys.argv[3],sys.argv[4]],dict(os.environ)))"
            process = subprocess.Popen(['/usr/bin/python3', '-B', '-c', supervisor, str(config), fixture,
                                        str(output), str(child_pid)], cwd=Path(__file__).parent)
            try:
                wait_for(lambda: child_pid.exists())
                process.send_signal(signal.SIGTERM)
                self.assertEqual(process.wait(timeout=8), 143)
                self.assertFalse(Path('/proc/' + child_pid.read_text()).exists())
                with self.assertRaises(GLib.Error):
                    portal.connect(output.read_text())
            finally:
                portal.stop_process(process)

    def test_setup_failure_stops_private_daemon(self):
        with tempfile.TemporaryDirectory(prefix='zah-portal-failure-') as temp:
            config = bus_config(Path(temp))
            started = []
            real_start = portal.start_bus
            def start(*args):
                result = real_start(*args)
                started.append(result)
                return result
            with patch.object(portal, 'start_bus', side_effect=start), \
                 patch.object(portal, 'WebPortal', side_effect=RuntimeError('fixture failure')):
                with self.assertRaisesRegex(RuntimeError, 'fixture failure'):
                    portal.run(config, ['/usr/bin/false'], dict(os.environ))
            self.assertIsNotNone(started[0][0].poll())
            with self.assertRaises(GLib.Error):
                portal.connect(started[0][1])

    def test_real_child_gets_only_private_bus_and_exit_is_preserved(self):
        with tempfile.TemporaryDirectory(prefix='zah-portal-life-') as temp:
            root = Path(temp)
            config = bus_config(root)
            output = root / 'address'
            script = """import os,sys
from pathlib import Path
assert not any('FAKE_HOST' in value for value in os.environ.values())
assert os.environ['DBUS_SESSION_BUS_ADDRESS'].startswith('unix:')
assert 'DBUS_STARTER_ADDRESS' not in os.environ
Path(sys.argv[1]).write_text(os.environ['DBUS_SESSION_BUS_ADDRESS'])
sys.exit(7)
"""
            env = dict(os.environ, DBUS_SESSION_BUS_ADDRESS='FAKE_HOST', DBUS_STARTER_ADDRESS='FAKE_HOST')
            with patch.object(Gio.AppInfo, 'launch_default_for_uri_async', side_effect=AssertionError('no browser')):
                self.assertEqual(portal.run(config, ['/usr/bin/python3', '-B', '-c', script, str(output)], env), 7)
            self.assertEqual(env['DBUS_SESSION_BUS_ADDRESS'], 'FAKE_HOST')
            with self.assertRaises(GLib.Error):
                portal.connect(output.read_text())


if __name__ == '__main__':
    unittest.main()
