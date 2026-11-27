// scripts/pack-desktop.js is the electron-builder call release.yml's three
// desktop jobs and forge's reliant-desktop ShellBuild share. These pin the
// command it runs to the one each job ran before it existed:
//
//   macOS    npx electron-builder --config "$CONFIG" --mac --publish $PUBLISH_FLAG
//   Linux    npx electron-builder --config "$CONFIG" --linux --publish $PUBLISH_FLAG
//   Windows  & .\node_modules\.bin\electron-builder.cmd --config "$CONFIG" --win --publish $PUBLISH
//
// and prove, with a fake npx, that the real spawn passes the environment
// through untouched and hands back electron-builder's exit code.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const SCRIPT = path.join(__dirname, '..', 'scripts', 'pack-desktop.js');
const { parseArgs, packCommand, missingPrerequisites, main, PLATFORMS } = require(SCRIPT);

test('each platform runs the electron-builder command its release job ran', () => {
  for (const publish of ['always', 'never']) {
    for (const config of ['electron-builder.js', 'electron-builder.dev.js']) {
      for (const platform of ['mac', 'linux']) {
        assert.deepEqual(packCommand(parseArgs([platform, config, publish])), {
          command: 'npx',
          args: ['electron-builder', '--config', config, `--${platform}`, '--publish', publish],
          shell: false,
        });
      }
      assert.deepEqual(packCommand(parseArgs(['win', config, publish])), {
        command: `.\\node_modules\\.bin\\electron-builder.cmd --config ${config} --win --publish ${publish}`,
        args: [],
        shell: true,
      });
    }
  }
});

test('host names the platform this machine packages', () => {
  assert.equal(parseArgs(['host', 'electron-builder.js', 'never'], 'darwin').platform, 'mac');
  assert.equal(parseArgs(['host', 'electron-builder.js', 'never'], 'linux').platform, 'linux');
  assert.equal(parseArgs(['host', 'electron-builder.js', 'never'], 'win32').platform, 'win');
  assert.throws(() => parseArgs(['host', 'electron-builder.js', 'never'], 'freebsd'), /host platform 'freebsd'/);
});

test('arguments outside the plain token alphabet are refused, so cmd.exe has nothing to reinterpret', () => {
  const bad = [
    [],
    ['mac', 'electron-builder.js'],
    ['mac', 'electron-builder.js', 'never', 'extra'],
    ['macos', 'electron-builder.js', 'never'],
    ['mac', 'electron-builder.js & calc', 'never'],
    ['win', 'electron-builder.js"', 'never'],
    ['win', '..\\electron-builder.js', 'never'],
    ['mac', 'electron-builder.json', 'never'],
    ['mac', 'electron-builder.js', 'sometimes'],
    ['mac', 'electron-builder.js', ''],
  ];
  for (const argv of bad) {
    const errors = [];
    const code = main(argv, { spawn: () => assert.fail('spawned'), log: { error: (m) => errors.push(m), log() {} } });
    assert.equal(code, 2, `argv ${JSON.stringify(argv)}`);
    assert.match(errors.join('\n'), /usage:/);
  }
});

// A temp electron/ holding every prerequisite for every platform.
function fakeElectronDir() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pack-desktop-'));
  const electronDir = path.join(root, 'electron');
  const files = [
    'electron-builder.js',
    'node_modules/.bin/electron-builder',
    'node_modules/.bin/electron-builder.cmd',
    'src/build-config.js',
    'src/release-renderer-config.js',
    '../frontends/web/dist/index.html',
  ];
  for (const { backends, exe } of Object.values(PLATFORMS)) {
    for (const dir of backends) files.push(`resources/server/${dir}/${exe}`);
  }
  for (const rel of files) {
    const file = path.join(electronDir, rel);
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.writeFileSync(file, '');
  }
  return { root, electronDir };
}

function quiet() {
  const lines = [];
  return { lines, log: { log: (m) => lines.push(m), error: (m) => lines.push(m) } };
}

test('main spawns in electron/ with inherited stdio and returns the exit code', (t) => {
  const { root, electronDir } = fakeElectronDir();
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  for (const [platform, status] of [['mac', 0], ['linux', 7], ['win', 3]]) {
    const calls = [];
    const spawn = (command, args, opts) => {
      calls.push({ command, args, opts });
      return { status, signal: null };
    };
    const code = main([platform, 'electron-builder.js', 'never'], { electronDir, spawn, ...quiet() });
    assert.equal(code, status);
    assert.equal(calls.length, 1);
    const want = packCommand({ platform, config: 'electron-builder.js', publish: 'never' });
    assert.equal(calls[0].command, want.command);
    assert.deepEqual(calls[0].args, want.args);
    // No env option: the child inherits this process's environment as is.
    assert.deepEqual(calls[0].opts, { cwd: electronDir, stdio: 'inherit', shell: want.shell });
  }
});

test('a signal or a spawn failure is a failure', (t) => {
  const { root, electronDir } = fakeElectronDir();
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const argv = ['linux', 'electron-builder.js', 'never'];
  assert.equal(main(argv, { electronDir, spawn: () => ({ status: null, signal: 'SIGTERM' }), ...quiet() }), 1);
  assert.equal(main(argv, { electronDir, spawn: () => ({ error: new Error('ENOENT') }), ...quiet() }), 1);
});

test('a missing prerequisite refuses before electron-builder runs, naming it', (t) => {
  const { root, electronDir } = fakeElectronDir();
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const request = { platform: 'linux', config: 'electron-builder.js', publish: 'never' };
  assert.deepEqual(missingPrerequisites(request, electronDir), []);
  for (const rel of ['resources/server/linux-arm64/reliant-backend', '../frontends/web/dist/index.html', 'src/build-config.js']) {
    const file = path.join(electronDir, rel);
    fs.rmSync(file);
    const { lines, log } = quiet();
    const code = main(['linux', 'electron-builder.js', 'never'], { electronDir, spawn: () => assert.fail('spawned'), log });
    assert.equal(code, 1);
    assert.match(lines.join('\n'), new RegExp(path.normalize(rel).replace(/[.\\]/g, '\\$&')));
    fs.writeFileSync(file, '');
  }
  // Windows checks its own backend names and the .cmd it runs.
  fs.rmSync(path.join(electronDir, 'resources/server/win32-amd64/reliant-backend.exe'));
  assert.deepEqual(missingPrerequisites({ ...request, platform: 'win' }, electronDir), [
    path.join('resources', 'server', 'win32-amd64', 'reliant-backend.exe'),
  ]);
});

test('end to end with a fake npx: argv, cwd, environment and exit code', { skip: process.platform === 'win32' && 'the fake npx is a POSIX script' }, (t) => {
  const { root, electronDir } = fakeElectronDir();
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  // The script resolves electron/ from its own location, so run a copy that
  // lives in the fake tree.
  fs.mkdirSync(path.join(electronDir, 'scripts'));
  fs.copyFileSync(SCRIPT, path.join(electronDir, 'scripts', 'pack-desktop.js'));
  const bin = path.join(root, 'bin');
  const record = path.join(root, 'record');
  fs.mkdirSync(bin);
  fs.writeFileSync(
    path.join(bin, 'npx'),
    '#!/bin/sh\n' +
      `{ pwd -P; for a in "$@"; do printf '%s\\n' "$a"; done; printf 'CSC_LINK=%s\\n' "$CSC_LINK"; } > '${record}'\n` +
      'exit "$FAKE_STATUS"\n',
    { mode: 0o755 },
  );
  const run = spawnSync(process.execPath, ['scripts/pack-desktop.js', 'host', 'electron-builder.js', 'never'], {
    cwd: electronDir,
    env: { ...process.env, PATH: `${bin}${path.delimiter}${process.env.PATH}`, CSC_LINK: 'secret-link', FAKE_STATUS: '5' },
    encoding: 'utf8',
  });
  assert.equal(run.status, 5, run.stderr);
  const host = process.platform === 'darwin' ? 'mac' : 'linux';
  assert.deepEqual(fs.readFileSync(record, 'utf8').trimEnd().split('\n'), [
    fs.realpathSync(electronDir),
    'electron-builder',
    '--config',
    'electron-builder.js',
    `--${host}`,
    '--publish',
    'never',
    'CSC_LINK=secret-link',
  ]);
});
