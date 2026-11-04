// Build source only; this script does not install or start the application.
const { spawnSync } = require('node:child_process');
const { mkdirSync } = require('node:fs');
mkdirSync('bin', { recursive: true });
const result = spawnSync(process.env.GO_BINARY || 'go', ['build', '-trimpath', '-o', 'bin/zah', './cmd/zah'], {
  stdio: 'inherit', env: { ...process.env, CGO_ENABLED: '0', GOOS: 'linux', GOARCH: 'amd64', GOAMD64: 'v1' }
});
if (result.error) { console.error('Go toolchain unavailable. Put Go on PATH or set GO_BINARY to its absolute path.'); process.exit(1); }
process.exit(result.status ?? 1);
