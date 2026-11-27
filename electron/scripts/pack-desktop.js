#!/usr/bin/env node
// Packages the desktop app for one platform with electron-builder: the ONE
// packaging step release.yml's three desktop jobs (release-macos,
// release-linux, release-windows) and forge's `reliant-desktop` ShellBuild
// (deploy/kcl/workloads.k) both run.
//
//   node scripts/pack-desktop.js <mac|linux|win|host> <config> <publish>
//
//   config   an electron-builder config in electron/: electron-builder.js
//            (the latest channel, the only one published)
//   publish  electron-builder's --publish: always | never | onTag | onTagOrDraft
//   host     the platform this machine packages natively (darwin -> mac,
//            linux -> linux, win32 -> win): what forge's build passes, because
//            a render cannot know which machine will run it
//
// ## What it owns, and what it does not
//
// It owns the electron-builder invocation and nothing around it. Checkout,
// artifact downloads, `pnpm install`, the release config, secrets, caches and the
// prebuild publish stay with the caller, because they differ per caller: CI
// downloads the backend and renderer other jobs built, a local build makes
// them (see "Prerequisites"). Every environment variable electron-builder
// reads (signing, notarization, R2, Trusted Signing) reaches it unchanged:
// the child inherits this process's environment and nothing is added.
//
// ## Each platform keeps the launcher it had
//
//   mac, linux  `npx electron-builder ...`, no shell, exactly what the bash
//               steps ran.
//   win         `.\node_modules\.bin\electron-builder.cmd ...` through
//               cmd.exe, exactly what the PowerShell step ran: PowerShell
//               starts a .cmd through cmd.exe too, and the package manager's .cmd shim is
//               what picks node and adjusts PATHEXT for electron-builder. It
//               is not launched through npx (the step avoided npx on Windows)
//               or through a POSIX shell: Trusted Signing runs inside
//               electron-builder's own PowerShell call, which inherits this
//               environment, and a bash in between would rewrite it.
//
// That is why this is a node script and not a shell one: node runs natively
// on all three runners, so the Windows job calls it from the same PowerShell
// step, and the signing environment is the one that step always had.
//
// The arguments are checked against a plain token alphabet, so the Windows
// command line, which cmd.exe parses, needs no quoting and cannot be
// reinterpreted.
//
// ## Prerequisites
//
// It refuses, running nothing, unless what electron-builder packages is on
// disk; electron-builder only warns about a missing extraResources source and
// ships an app without it:
//
//   node_modules/.bin/electron-builder   `pnpm install` in electron/
//   resources/server/<plat>-<arch>/      both arches of the backend
//     reliant-backend[.exe]              (release.yml's build-backend, or
//                                        ../scripts/build-electron.sh <plat>)
//   ../frontends/web/dist/index.html     the renderer (release.yml's web-dist
//                                        artifact, or `pnpm install` and
//                                        `pnpm run build` in frontends/web)
//   src/build-config.js,                 generate-build-config.mjs (the
//   src/release-renderer-config.js       generate-build-config action, or
//                                        `node scripts/with-release-config.mjs
//                                        pnpm run build:config`)
//
// release.yml's earlier steps produce each of these, so in CI the check never
// fires; it is what turns a forge build on a fresh checkout into a clear
// refusal instead of an installer without a backend.
//
// Self-test: test/pack-desktop.test.js (part of `pnpm test`).

const { spawnSync } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');

const ELECTRON_DIR = path.join(__dirname, '..');

// electron-builder's own --publish values.
const PUBLISH_VALUES = ['always', 'never', 'onTag', 'onTagOrDraft'];

// The backend directories each platform packages (electron-builder.common.js
// copies resources/server whole), named as release.yml downloads them and
// scripts/build-electron.sh writes them.
const PLATFORMS = {
  mac: { flag: '--mac', backends: ['mac-x64', 'mac-arm64'], exe: 'reliant-backend' },
  linux: { flag: '--linux', backends: ['linux-amd64', 'linux-arm64'], exe: 'reliant-backend' },
  win: { flag: '--win', backends: ['win32-amd64', 'win32-arm64'], exe: 'reliant-backend.exe' },
};

const HOST_PLATFORMS = { darwin: 'mac', linux: 'linux', win32: 'win' };

const TOKEN = /^[A-Za-z0-9._-]+$/;

class UsageError extends Error {}

/** The platform `host` names on this machine. */
function hostPlatform(nodePlatform = process.platform) {
  const platform = HOST_PLATFORMS[nodePlatform];
  if (!platform) {
    throw new UsageError(`no desktop build for host platform '${nodePlatform}' (mac, linux and win only)`);
  }
  return platform;
}

/** Validates the arguments and resolves `host`. */
function parseArgs(argv, nodePlatform = process.platform) {
  if (argv.length !== 3) {
    throw new UsageError(`expected 3 arguments, got ${argv.length}`);
  }
  const [requested, config, publish] = argv;
  const platform = requested === 'host' ? hostPlatform(nodePlatform) : requested;
  if (!Object.hasOwn(PLATFORMS, platform)) {
    throw new UsageError(`unknown platform '${requested}' (mac, linux, win or host)`);
  }
  if (!TOKEN.test(config) || !config.endsWith('.js')) {
    throw new UsageError(`config '${config}' is not an electron-builder config file name in electron/`);
  }
  if (!PUBLISH_VALUES.includes(publish)) {
    throw new UsageError(`publish '${publish}' is not one of ${PUBLISH_VALUES.join(', ')}`);
  }
  return { platform, config, publish };
}

/**
 * The electron-builder invocation, as release.yml's steps spelled it:
 * mac/linux exec npx with an argv, win hands cmd.exe one command line.
 */
function packCommand({ platform, config, publish }) {
  const args = ['--config', config, PLATFORMS[platform].flag, '--publish', publish];
  if (platform === 'win') {
    return { command: ['.\\node_modules\\.bin\\electron-builder.cmd', ...args].join(' '), args: [], shell: true };
  }
  return { command: 'npx', args: ['electron-builder', ...args], shell: false };
}

/** The prerequisites (see the header) missing from electronDir, relative to it. */
function missingPrerequisites({ platform, config }, electronDir = ELECTRON_DIR) {
  const { backends, exe } = PLATFORMS[platform];
  const builder = platform === 'win' ? 'electron-builder.cmd' : 'electron-builder';
  const required = [
    config,
    path.join('node_modules', '.bin', builder),
    ...backends.map((dir) => path.join('resources', 'server', dir, exe)),
    path.join('..', 'frontends', 'web', 'dist', 'index.html'),
    path.join('src', 'build-config.js'),
    path.join('src', 'release-renderer-config.js'),
  ];
  return required.filter((rel) => !fs.existsSync(path.join(electronDir, rel)));
}

/** Packages; returns the exit code (2 usage, 1 refused or not started, else electron-builder's). */
function main(argv, { electronDir = ELECTRON_DIR, nodePlatform = process.platform, spawn = spawnSync, log = console } = {}) {
  let request;
  try {
    request = parseArgs(argv, nodePlatform);
  } catch (err) {
    if (!(err instanceof UsageError)) throw err;
    log.error(`pack-desktop: ${err.message}`);
    log.error('usage: node scripts/pack-desktop.js <mac|linux|win|host> <config> <publish>');
    return 2;
  }
  const missing = missingPrerequisites(request, electronDir);
  if (missing.length > 0) {
    log.error(`pack-desktop: refusing to package ${request.platform}; missing in ${electronDir}:`);
    for (const rel of missing) log.error(`  ${rel}`);
    log.error('(the Prerequisites section of electron/scripts/pack-desktop.js says what makes each)');
    return 1;
  }
  const { command, args, shell } = packCommand(request);
  log.log(`pack-desktop: ${[command, ...args].join(' ')}`);
  const result = spawn(command, args, { cwd: electronDir, stdio: 'inherit', shell });
  if (result.error) {
    log.error(`pack-desktop: could not start ${command}: ${result.error.message}`);
    return 1;
  }
  if (result.status === null) {
    log.error(`pack-desktop: electron-builder was killed by ${result.signal}`);
    return 1;
  }
  return result.status;
}

if (require.main === module) process.exitCode = main(process.argv.slice(2));
module.exports = { PLATFORMS, PUBLISH_VALUES, hostPlatform, parseArgs, packCommand, missingPrerequisites, main };
