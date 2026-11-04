#!/usr/bin/env python3
"""Ephemeral HTTP(S)-only portal for private_runtime.py, not a host bus proxy."""
import ipaddress
import json
import sys
import os
import re
import signal
import selectors
import time
import subprocess
import uuid
from urllib.parse import urlsplit

import gi
gi.require_version('Gio', '2.0')
from gi.repository import Gio, GLib

NAME = 'org.freedesktop.portal.Desktop'
PATH = '/org/freedesktop/portal/desktop'
OPEN = 'org.freedesktop.portal.OpenURI'
REQUEST = 'org.freedesktop.portal.Request'
TIMEOUT_SECONDS = 30
MAX_PENDING = 16
IMPLEMENTATION = 'direct-http-v2'
XML = '''<node><interface name="org.freedesktop.portal.OpenURI">
<method name="OpenURI"><arg type="s" direction="in"/><arg type="s" direction="in"/>
<arg type="a{sv}" direction="in"/><arg type="o" direction="out"/></method>
<property name="version" type="u" access="read"/>
</interface><interface name="org.freedesktop.portal.Request">
<method name="Close"/><signal name="Response"><arg type="u"/><arg type="a{sv}"/></signal>
</interface></node>'''
INFO = Gio.DBusNodeInfo.new_for_xml(XML)


def validate_uri(uri):
    """Keep the exact reviewed bytes. No domain allowlist, rewriting or shell."""
    if not isinstance(uri, str) or not 1 <= len(uri) <= 8192:
        raise ValueError('HTTP(S) address required (maximum 8192 characters)')
    if not re.fullmatch(r"[A-Za-z0-9:/?#\[\]@!$&'()*+,;=._~%+-]+", uri):
        raise ValueError('Use an ASCII URI with percent-encoded non-ASCII characters')
    if re.search(r'%(?![0-9A-Fa-f]{2})|%(?:0[0-9A-Fa-f]|1[0-9A-Fa-f]|7[fF])', uri):
        raise ValueError('Invalid escaping or encoded control character')
    parts = urlsplit(uri)
    if parts.scheme.lower() not in ('http', 'https') or not parts.netloc or not parts.hostname:
        raise ValueError('Only absolute HTTP(S) addresses are supported')
    if parts.username is not None or parts.password is not None or '%' in parts.netloc:
        raise ValueError('Credentials and escaped host names are not supported')
    host = parts.hostname
    if ':' in host:
        ipaddress.IPv6Address(host)
        if not re.fullmatch(r'\[[0-9a-fA-F:.]+\](?::[0-9]+)?', parts.netloc):
            raise ValueError('Invalid IPv6 authority')
    else:
        if not re.fullmatch(r'[A-Za-z0-9.-]+(?::[0-9]+)?', parts.netloc):
            raise ValueError('Invalid host or port')
        if len(host) > 253 or any(not re.fullmatch(r'[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?', label)
                                  for label in host.rstrip('.').split('.')):
            raise ValueError('Invalid host name')
    if parts.port is not None and not 1 <= parts.port <= 65535:
        raise ValueError('Invalid port')
    return uri


def audit(event, **fields):
    # Never log the URI, query, fragment, caller token, or arbitrary error text.
    print('ZaH web portal: ' + json.dumps(dict(event=event, **fields), ensure_ascii=True),
          file=sys.stderr, flush=True)


def error_fields(error):
    result = {'error_type': type(error).__name__}
    if isinstance(error, GLib.Error):
        result.update(error_domain=error.domain, error_code=error.code)
    return result


def launch_uri(uri, cancellable, callback):
    # GIO uses this host process's HOME/XDG/desktop bus, never Flatpak --env.
    # Resolve the existing handler for diagnostics only; do not change defaults.
    uri = validate_uri(uri)
    app = Gio.AppInfo.get_default_for_uri_scheme(urlsplit(uri).scheme.lower())
    if app is None:
        audit('host-launch-failed', reason='no-default-http-handler')
        callback(False)
        return
    audit('host-dispatch', handler=app.get_id())
    def complete(_source, result):
        try:
            success = Gio.AppInfo.launch_default_for_uri_finish(result)
        except GLib.Error as error:
            audit('host-launch-failed', **error_fields(error))
            success = False
        callback(success)
    Gio.AppInfo.launch_default_for_uri_async(uri, None, cancellable, complete)


class WebPortal:
    def __init__(self, connection, launch=launch_uri, timeout=TIMEOUT_SECONDS):
        self.connection, self.launch, self.timeout = connection, launch, timeout
        self.counter = 0
        self.pending = {}
        self.registration = connection.register_object(PATH, INFO.interfaces[0], self.open_uri,
                                                       lambda *_args: GLib.Variant('u', 1), None)
        self.subscription = connection.signal_subscribe('org.freedesktop.DBus', 'org.freedesktop.DBus',
            'NameOwnerChanged', '/org/freedesktop/DBus', None, Gio.DBusSignalFlags.NONE, self.owner_changed)
        result = connection.call_sync('org.freedesktop.DBus', '/org/freedesktop/DBus',
            'org.freedesktop.DBus', 'RequestName', GLib.Variant('(su)', (NAME, 4)),
            GLib.VariantType.new('(u)'), Gio.DBusCallFlags.NONE, 5000, None)
        if result.unpack()[0] != 1:
            self.shutdown()
            raise RuntimeError('Private portal name already owned; refusing replacement')
        audit('ready', implementation=IMPLEMENTATION, pid=os.getpid(), interface_version=1)

    def open_uri(self, _connection, sender, _path, _interface, _method, parameters, invocation):
        try:
            _parent, uri, options = parameters.unpack()
            validate_uri(uri)
            if set(options) - {'handle_token', 'ask', 'writable'}:
                raise ValueError('Unsupported OpenURI option')
            for name in ('ask', 'writable'):
                if name in options and type(options[name]) is not bool:
                    raise ValueError('Invalid option type')
            if options.get('writable', False):
                raise ValueError('Writable resources are not supported')
            token = options.get('handle_token', 'zah_' + uuid.uuid4().hex)
            if not isinstance(token, str) or not re.fullmatch(r'[A-Za-z0-9_]{1,128}', token):
                raise ValueError('Invalid request token')
        except (ValueError, TypeError):
            audit('request-rejected', reason='invalid-uri-or-options')
            invocation.return_dbus_error('org.freedesktop.portal.Error.InvalidArgument', 'Only valid HTTP(S) web requests are supported')
            return
        if len(self.pending) >= MAX_PENDING:
            audit('request-rejected', reason='too-many-pending-requests')
            invocation.return_dbus_error('org.freedesktop.portal.Error.NotAllowed', 'Too many pending web requests')
            return
        path = PATH + '/request/' + sender[1:].replace('.', '_') + '/' + token
        if path in self.pending:
            invocation.return_dbus_error('org.freedesktop.portal.Error.InvalidArgument', 'Request token already in use')
            return
        self.counter += 1
        request = {'id': self.counter, 'sender': sender, 'connected': True, 'uri': uri, 'idle': 0, 'timer': 0,
                   'cancellable': Gio.Cancellable(), 'opening': False}
        request['registration'] = self.connection.register_object(path, INFO.interfaces[1], self.close_request, None, None)
        self.pending[path] = request
        request['timer'] = GLib.timeout_add_seconds(self.timeout, self.expire, path)
        invocation.return_value(GLib.Variant('(o)', (path,)))
        audit('request-received', request=request['id'], scheme=urlsplit(uri).scheme.lower())
        request['idle'] = GLib.idle_add(self.dispatch, path)

    def dispatch(self, path):
        request = self.pending.get(path)
        if request is None or request['opening']:
            return False
        request['idle'] = 0
        request['opening'] = True
        audit('launch-started', request=request['id'])
        try:
            self.launch(validate_uri(request['uri']), request['cancellable'],
                        lambda success: self.finish(path, 0 if success else 2))
        except Exception as error:
            audit('launch-exception', request=request['id'], **error_fields(error))
            self.finish(path, 2)
        return False

    def close_request(self, _connection, sender, path, _interface, _method, _parameters, invocation):
        request = self.pending.get(path)
        if request is None or sender != request['sender']:
            invocation.return_dbus_error('org.freedesktop.portal.Error.NotAllowed', 'Only the requesting connection may close this request')
            return
        invocation.return_value(None)
        audit('request-closed', request=request['id'])
        self.finish(path, None)  # Request.Close has no Response signal.

    def expire(self, path):
        request = self.pending.get(path)
        if request:
            request['timer'] = 0
            audit('request-timeout', request=request['id'])
        self.finish(path, 2)
        return False

    def finish(self, path, response):
        request = self.pending.pop(path, None)
        if request is None:
            return
        request['cancellable'].cancel()
        for key in ('idle', 'timer'):
            if request[key]:
                GLib.source_remove(request[key])
        self.connection.unregister_object(request['registration'])
        audit('request-finished', request=request['id'], response=response)
        if response is not None and request['connected'] and not self.connection.is_closed():
            self.connection.emit_signal(request['sender'], path, REQUEST, 'Response', GLib.Variant('(ua{sv})', (response, {})))

    def owner_changed(self, _connection, _sender, _path, _interface, _signal, parameters):
        name, _old, new = parameters.unpack()
        if not new:
            for path, request in list(self.pending.items()):
                if request['sender'] == name:
                    # flatpak-xdg-utils exits after the method reply; that is
                    # delivery, not cancellation. Keep the accepted launch alive.
                    request['connected'] = False
                    audit('caller-exited', request=request['id'], action='continue-launch')

    def shutdown(self):
        for path in list(self.pending):
            self.finish(path, 2)
        self.connection.signal_unsubscribe(self.subscription)
        self.connection.unregister_object(self.registration)


def connect(address):
    return Gio.DBusConnection.new_for_address_sync(address,
        Gio.DBusConnectionFlags.AUTHENTICATION_CLIENT | Gio.DBusConnectionFlags.MESSAGE_BUS_CONNECTION, None, None)


def child_environment(environment):
    # Drop every inherited DBus address before starting any private process.
    return {key: value for key, value in environment.items() if not key.startswith('DBUS_')}


def start_bus(config, environment):
    daemon = subprocess.Popen(['/usr/bin/dbus-daemon', '--nofork', '--nopidfile',
        '--config-file=' + str(config), '--print-address=1'], env=child_environment(environment),
        stdout=subprocess.PIPE, close_fds=True)
    try:
        deadline = time.monotonic() + 5
        address = bytearray()
        with selectors.DefaultSelector() as selector:
            selector.register(daemon.stdout, selectors.EVENT_READ)
            while len(address) <= 4096:
                remaining = deadline - time.monotonic()
                if remaining <= 0 or not selector.select(remaining):
                    raise RuntimeError('Private bus startup timed out')
                value = os.read(daemon.stdout.fileno(), 1)
                if not value:
                    raise RuntimeError('Private bus exited before ready')
                if value == b'\n':
                    result = address.decode('utf-8')
                    if not result.startswith('unix:'):
                        raise RuntimeError('Private bus returned an unexpected address')
                    return daemon, result
                address.extend(value)
        raise RuntimeError('Private bus address exceeds limit')
    except BaseException:
        stop_process(daemon)
        raise
    finally:
        daemon.stdout.close()


def stop_process(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def run(config, command, environment):
    # The caller remains on the original desktop. There is no host-address pipe,
    # forwarding connection, or host bus variable in either private child.
    daemon, private_address = start_bus(config, environment)
    private_env = child_environment(environment)
    private_env['DBUS_SESSION_BUS_ADDRESS'] = private_address
    connection = portal = child = None
    sources = []
    loop = GLib.MainLoop()
    stopping = False
    kill_source = 0
    bus_failed = False
    def stop(signum):
        nonlocal stopping, kill_source
        if not stopping:
            stopping = True
            for path in list(portal.pending):
                portal.finish(path, 2)
            if child.poll() is None:
                child.send_signal(signum)
                kill_source = GLib.timeout_add_seconds(5, kill_child)
        return True
    def kill_child():
        nonlocal kill_source
        kill_source = 0
        if child.poll() is None:
            child.kill()
        return False
    def poll():
        nonlocal bus_failed
        if daemon.poll() is not None:
            bus_failed = True
            stop(signal.SIGTERM)
        if child.poll() is not None and not portal.pending:
            loop.quit()
        return True
    try:
        connection = connect(private_address)
        connection.set_exit_on_close(False)
        portal = WebPortal(connection)
        audit('host-context', home=os.environ.get('HOME'),
              xdg_data_home=os.environ.get('XDG_DATA_HOME'),
              xdg_config_home=os.environ.get('XDG_CONFIG_HOME'),
              host_bus_present=bool(os.environ.get('DBUS_SESSION_BUS_ADDRESS')),
              display_present=bool(os.environ.get('DISPLAY') or os.environ.get('WAYLAND_DISPLAY')))
        child = subprocess.Popen(command, env=private_env, close_fds=True)
        sources.append(GLib.timeout_add(100, poll))
        for signum in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
            sources.append(GLib.unix_signal_add(GLib.PRIORITY_DEFAULT, signum, stop, signum))
        connection.connect('closed', lambda *_args: stop(signal.SIGTERM))
        loop.run()
        if bus_failed:
            return 1
        return child.returncode if child.returncode >= 0 else 128 - child.returncode
    finally:
        if portal is not None:
            portal.shutdown()
        for source in sources + ([kill_source] if kill_source else []):
            GLib.source_remove(source)
        if child is not None:
            stop_process(child)
        if connection is not None and not connection.is_closed():
            connection.flush_sync(None)
            connection.close_sync(None)
        stop_process(daemon)
