// Copyright (c) 2025 Reliant Labs
//
// CLI installer: makes the `reliant` command available on the user's $PATH
// after the Electron desktop app is installed.
//
// The same Go binary (`./cmd/reliant/`) is shipped inside the app bundle as
// `reliant-backend` (spawned by the GUI) and exposed on $PATH as `reliant`
// (the CLI users invoke from a terminal). We do NOT ship a second copy — we
// either symlink the embedded binary or, when symlinks are awkward (Windows,
// signed/sandboxed bundles), copy it into a user-writable bin dir.
//
// Cross-platform strategy:
//   macOS  : resolve the user's effective shell $PATH (Electron inherits
//            launchd's stripped PATH, so we spawn `$SHELL -lc 'echo $PATH'`).
//            Walk a tiered preference list, picking the first directory that
//            is BOTH on the user's $PATH AND writable without sudo:
//              1) /opt/homebrew/bin  (Apple Silicon Homebrew)
//              2) /usr/local/bin     (Intel Homebrew / classic Unix)
//              3) ~/.local/bin       (XDG user-local)
//              4) ~/bin              (legacy user)
//            If no tier is on PATH-and-writable, fall back to the first
//            writable tier and warn the user to add it to PATH. If nothing
//            is writable, defer to the osascript sudo path (Settings → About).
//   Linux  : try `/usr/local/bin/reliant` symlink (no sudo if writable),
//            fallback to `~/.local/bin/reliant` (no sudo).
//            .deb post-install handles `/usr/bin/reliant` directly.
//   Windows: copy the bundled binary to `%LOCALAPPDATA%\Reliant\bin\reliant.exe`
//            and add that directory to the user PATH (HKCU registry).
//            NSIS installer also performs this at install time.
//
// First-run behaviour:
//   On every app launch we check whether the CLI is installed for the current
//   app version. If not, we silently install via the no-sudo path. The
//   richer `install-cli` IPC handler (which can request sudo on macOS via
//   osascript / pkexec on Linux) remains available from Settings → About for
//   users who want the system-wide symlink.

const fs = require("fs");
const os = require("os");
const path = require("path");
const { exec, spawnSync } = require("child_process");
const { app } = require("electron");
const log = require("./logger");

/** Architectural folder name inside electron/resources/server. */
function resourceArchDir() {
  const platform = process.platform;
  const arch = process.arch;
  if (platform === "darwin") return `mac-${arch === "x64" ? "x64" : "arm64"}`;
  if (platform === "win32") return `win32-${arch === "arm64" ? "arm64" : "amd64"}`;
  if (platform === "linux") return `linux-${arch === "arm64" ? "arm64" : "amd64"}`;
  return null;
}

/**
 * Absolute path to the embedded backend binary that doubles as the CLI.
 * Returns null when running in development mode (we deliberately do not
 * meddle with $PATH in dev — devs build their own `dist/reliant`).
 */
function getEmbeddedBinaryPath() {
  if (!app.isPackaged) return null;

  const dir = resourceArchDir();
  if (!dir) return null;

  const ext = process.platform === "win32" ? ".exe" : "";
  const binaryPath = path.join(process.resourcesPath, "server", dir, `reliant-backend${ext}`);

  return fs.existsSync(binaryPath) ? binaryPath : null;
}

/** Path of the marker file we write after a successful install for this version. */
function markerPath() {
  return path.join(app.getPath("userData"), "cli-installed.json");
}

/** Returns the version we last successfully installed the CLI for, or null. */
function readInstallMarker() {
  try {
    if (!fs.existsSync(markerPath())) return null;
    const data = JSON.parse(fs.readFileSync(markerPath(), "utf8"));
    return data && typeof data.version === "string" ? data : null;
  } catch (err) {
    log.warn("[CLIInstaller] Could not read install marker:", err.message);
    return null;
  }
}

/** Records a successful install in the marker file. */
function writeInstallMarker(target) {
  try {
    const data = {
      version: app.getVersion(),
      target,
      installedAt: new Date().toISOString(),
    };
    fs.writeFileSync(markerPath(), JSON.stringify(data, null, 2), "utf8");
  } catch (err) {
    log.warn("[CLIInstaller] Could not write install marker:", err.message);
  }
}

// ---------------------------------------------------------------------------
// macOS / Linux helpers
// ---------------------------------------------------------------------------

/**
 * Attempt to create a symlink at `target` pointing at `source`.
 * Returns true on success, false otherwise. Replaces any existing link/file
 * at the target if writable.
 */
function trySymlink(source, target) {
  try {
    const parent = path.dirname(target);
    if (!fs.existsSync(parent)) {
      fs.mkdirSync(parent, { recursive: true });
    }
    // Remove existing entry (symlink or regular file) so we can refresh it.
    try {
      const st = fs.lstatSync(target);
      if (st.isSymbolicLink()) {
        try {
          const existing = fs.readlinkSync(target);
          const absoluteExisting = path.isAbsolute(existing)
            ? existing
            : path.resolve(path.dirname(target), existing);
          if (absoluteExisting === source) {
            return true; // already correctly installed
          }
        } catch (_) { /* fall through to unlink+recreate */ }
        fs.unlinkSync(target);
      } else if (st.isFile()) {
        fs.unlinkSync(target);
      }
    } catch (_) { /* not present — fine */ }

    fs.symlinkSync(source, target);
    return true;
  } catch (err) {
    log.debug(`[CLIInstaller] symlink ${source} -> ${target} failed: ${err.message}`);
    return false;
  }
}

/** True if `dir` appears in `pathEnv`. Pure helper. */
function pathEnvContains(pathEnv, dir) {
  const sep = process.platform === "win32" ? ";" : ":";
  return (pathEnv || "")
    .split(sep)
    .some((p) => p && path.normalize(p) === path.normalize(dir));
}

/** True if `dir` appears in the current process $PATH. Best-effort. */
function pathContains(dir) {
  return pathEnvContains(process.env.PATH || "", dir);
}

/**
 * Resolve the user's effective shell $PATH. Electron inherits $PATH from
 * launchd on macOS, which is typically the bare `/usr/bin:/bin:/usr/sbin:/sbin`
 * and misses Homebrew + user-local dirs. Spawning the user's login shell
 * (`bash -lc` / `zsh -lc`) gives us what they'd actually see in Terminal.
 *
 * Falls back to `process.env.PATH` on any failure. Only invoked on
 * macOS/Linux; Windows uses a different model.
 */
function resolveUserShellPath() {
  // Allow tests / users to override.
  if (process.env.RELIANT_USER_PATH) return process.env.RELIANT_USER_PATH;

  const shell = process.env.SHELL || "/bin/bash";
  try {
    const result = spawnSync(shell, ["-lc", "echo $PATH"], {
      encoding: "utf8",
      timeout: 3000,
    });
    if (result.status === 0 && result.stdout) {
      const out = result.stdout.trim();
      if (out) return out;
    }
    log.debug(`[CLIInstaller] '${shell} -lc echo $PATH' returned no output; falling back to process.env.PATH`);
  } catch (err) {
    log.debug(`[CLIInstaller] failed to spawn ${shell} for $PATH: ${err.message}`);
  }
  return process.env.PATH || "";
}

/**
 * True if we can write into `dir`. If `dir` doesn't exist, we check the
 * deepest existing ancestor (the install step will create the missing
 * directory tree later via `fs.mkdirSync(..., { recursive: true })`).
 */
function dirIsWritable(dir) {
  let cur = dir;
  // Walk up until we hit something that exists.
  while (cur && !fs.existsSync(cur)) {
    const parent = path.dirname(cur);
    if (parent === cur) return false;
    cur = parent;
  }
  try {
    fs.accessSync(cur, fs.constants.W_OK);
    return true;
  } catch (_) {
    return false;
  }
}

/**
 * Tiered preference list for the macOS / Linux `reliant` symlink directory.
 * Highest preference first. Ordering rationale:
 *   1. `/opt/homebrew/bin` — Apple Silicon Homebrew, almost always first on PATH
 *      and writable by the Homebrew user.
 *   2. `/usr/local/bin`    — Intel Homebrew + classic Unix system bindir.
 *   3. `~/.local/bin`      — XDG user-local; modern conventional spot.
 *   4. `~/bin`             — Legacy user bindir; still added to PATH by many
 *      shell-rc snippets.
 */
function preferredBinDirs() {
  const home = os.homedir();
  return [
    "/opt/homebrew/bin",
    "/usr/local/bin",
    path.join(home, ".local", "bin"),
    path.join(home, "bin"),
  ];
}

/**
 * Pick the best bin dir to install `reliant` into.
 *
 * Returns `{ dir, onPath, reason }`. `reason` is one of:
 *   - "path-and-writable"  — first tier that is on $PATH AND writable.
 *   - "writable-not-on-path" — first tier that is writable but NOT on $PATH
 *      (caller should warn the user). Only used if no tier is on PATH.
 *   - null when nothing is writable (caller must escalate / give up).
 *
 * Also logs each tier's (onPath, writable) status for debugging.
 */
function pickBinDir(userPath) {
  const tiers = preferredBinDirs();
  const status = tiers.map((dir) => ({
    dir,
    onPath: pathEnvContains(userPath, dir),
    writable: dirIsWritable(dir),
  }));

  for (const s of status) {
    log.info(`[CLIInstaller] tier ${s.dir}: onPath=${s.onPath} writable=${s.writable}`);
  }

  // First pass: on PATH and writable.
  const ideal = status.find((s) => s.onPath && s.writable);
  if (ideal) return { dir: ideal.dir, onPath: true, reason: "path-and-writable" };

  // Second pass: writable but not on PATH (warn the user).
  const fallback = status.find((s) => s.writable);
  if (fallback) return { dir: fallback.dir, onPath: false, reason: "writable-not-on-path" };

  return null;
}

// ---------------------------------------------------------------------------
// Windows helpers
// ---------------------------------------------------------------------------

/**
 * Append `dir` to the *user* PATH (HKCU\Environment\Path) via PowerShell.
 * Idempotent: skips if already present. Broadcasts WM_SETTINGCHANGE so new
 * terminals pick it up without a logout.
 */
function appendUserPathWindows(dir) {
  return new Promise((resolve) => {
    const escaped = dir.replace(/'/g, "''");
    const ps = [
      "$ErrorActionPreference='Stop';",
      "$dir='" + escaped + "';",
      "$cur=[Environment]::GetEnvironmentVariable('Path','User');",
      "if (-not $cur) { $cur='' };",
      "$parts = $cur.Split(';') | Where-Object { $_ -ne '' };",
      "if ($parts -notcontains $dir) {",
      "  $new = ($parts + $dir) -join ';';",
      "  [Environment]::SetEnvironmentVariable('Path', $new, 'User');",
      "}",
    ].join(" ");

    exec(`powershell -NoProfile -ExecutionPolicy Bypass -Command "${ps}"`, (err, stdout, stderr) => {
      if (err) {
        log.warn("[CLIInstaller] Failed to update Windows user PATH:", stderr || err.message);
        resolve(false);
      } else {
        resolve(true);
      }
    });
  });
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Try to make `reliant` available on $PATH without prompting the user.
 *
 * Returns one of:
 *   { success: true,  target: "<absolute path>", method: "..." }
 *   { success: false, error: "..." }
 *
 * Does NOT escalate privileges. The Settings → About page still exposes the
 * sudo-capable IPC handler for users who want a system-wide install.
 */
async function installSilently() {
  const source = getEmbeddedBinaryPath();
  if (!source) {
    return { success: false, error: "embedded backend binary not found (dev mode or build issue)" };
  }

  if (process.platform === "darwin") {
    // Walk a tiered preference list, picking the first directory that is BOTH
    // on the user's effective $PATH AND writable without sudo. If nothing on
    // PATH is writable, fall back to the first writable tier and warn that
    // the user must add it to PATH manually. If nothing is writable at all,
    // give up here — the Settings → About sudo path (osascript) handles it.
    const userPath = resolveUserShellPath();
    log.debug(`[CLIInstaller] resolved user $PATH: ${userPath}`);

    const pick = pickBinDir(userPath);
    if (!pick) {
      return {
        success: false,
        error: "no writable bin directory found in preferred tiers; sudo install required",
      };
    }

    const target = path.join(pick.dir, "reliant");
    if (!trySymlink(source, target)) {
      return { success: false, error: `failed to symlink ${target}` };
    }
    log.info(`[CLIInstaller] Symlinked ${target} -> ${source} (tier=${pick.dir}, reason=${pick.reason})`);

    return {
      success: true,
      target,
      method: `symlink-${pick.reason}`,
      warning: pick.onPath
        ? null
        : `${pick.dir} is not on your $PATH. Add it to use the 'reliant' command.`,
    };
  }

  if (process.platform === "linux") {
    // 1) /usr/local/bin (only works without sudo if writable, e.g. Homebrew users)
    const sysTarget = "/usr/local/bin/reliant";
    if (trySymlink(source, sysTarget)) {
      log.info(`[CLIInstaller] Symlinked ${sysTarget} -> ${source}`);
      return { success: true, target: sysTarget, method: "symlink-usr-local-bin" };
    }

    // 2) ~/.local/bin (always writable; conventional on Linux, accepted by
    //    macOS users who run modern shells). We do NOT modify the user's shell
    //    rc files — that's brittle. Instead we surface a warning if the dir
    //    isn't on $PATH so the UI can prompt.
    const userBin = path.join(os.homedir(), ".local", "bin");
    const userTarget = path.join(userBin, "reliant");
    if (trySymlink(source, userTarget)) {
      log.info(`[CLIInstaller] Symlinked ${userTarget} -> ${source}`);
      const onPath = pathContains(userBin);
      return {
        success: true,
        target: userTarget,
        method: "symlink-user-local-bin",
        warning: onPath ? null : `${userBin} is not on your $PATH. Add it to use the 'reliant' command.`,
      };
    }

    return { success: false, error: "could not create symlink in /usr/local/bin or ~/.local/bin" };
  }

  if (process.platform === "win32") {
    const userBinDir = path.join(
      process.env.LOCALAPPDATA || path.join(os.homedir(), "AppData", "Local"),
      "Reliant",
      "bin",
    );
    const target = path.join(userBinDir, "reliant.exe");

    try {
      if (!fs.existsSync(userBinDir)) {
        fs.mkdirSync(userBinDir, { recursive: true });
      }
      fs.copyFileSync(source, target);
    } catch (err) {
      log.warn("[CLIInstaller] Failed to copy CLI on Windows:", err.message);
      return { success: false, error: err.message };
    }

    const pathOk = await appendUserPathWindows(userBinDir);
    return {
      success: true,
      target,
      method: "copy-localappdata",
      warning: pathOk ? null : `Installed to ${target} but could not update user PATH. Add "${userBinDir}" to PATH manually.`,
    };
  }

  return { success: false, error: `unsupported platform: ${process.platform}` };
}

/**
 * First-run hook: install (silently) if we haven't already done so for this
 * app version. Safe to call from `app.whenReady`. Never throws — logs and
 * returns instead. Intended to run in the background; do not await before
 * showing UI.
 */
async function ensureInstalledOnce() {
  try {
    if (!app.isPackaged) {
      log.debug("[CLIInstaller] skipping (development mode)");
      return;
    }

    const marker = readInstallMarker();
    const currentVersion = app.getVersion();
    if (marker && marker.version === currentVersion && marker.target && fs.existsSync(marker.target)) {
      log.debug(`[CLIInstaller] CLI already installed for v${currentVersion} at ${marker.target}`);
      return;
    }

    log.info("[CLIInstaller] Installing reliant CLI on $PATH (first run for this version)...");
    const result = await installSilently();
    if (result.success) {
      writeInstallMarker(result.target);
      log.info(`[CLIInstaller] CLI install OK via ${result.method}: ${result.target}`);
      if (result.warning) log.warn(`[CLIInstaller] ${result.warning}`);
    } else {
      log.warn(`[CLIInstaller] Silent install failed: ${result.error}. User can retry from Settings → About.`);
    }
  } catch (err) {
    log.warn("[CLIInstaller] ensureInstalledOnce crashed:", err.message);
  }
}

/**
 * Returns the absolute path of the currently-installed CLI, or null.
 * Used by the renderer to decide whether the onboarding "run this command"
 * step is needed.
 */
function getInstalledCliPath() {
  const marker = readInstallMarker();
  if (marker && marker.target && fs.existsSync(marker.target)) {
    return marker.target;
  }
  // Fall back to probing the conventional locations in case install happened
  // via the NSIS / .deb installer (which doesn't write our marker file).
  const candidates = process.platform === "win32"
    ? [
      path.join(process.env.LOCALAPPDATA || "", "Reliant", "bin", "reliant.exe"),
      path.join(process.env.ProgramFiles || "", "Reliant", "bin", "reliant.exe"),
    ]
    : [
      "/opt/homebrew/bin/reliant",
      "/usr/local/bin/reliant",
      "/usr/bin/reliant",
      path.join(os.homedir(), ".local", "bin", "reliant"),
      path.join(os.homedir(), "bin", "reliant"),
    ];
  for (const c of candidates) {
    try {
      if (c && fs.existsSync(c)) return c;
    } catch (_) { /* ignore */ }
  }
  return null;
}

module.exports = {
  installSilently,
  ensureInstalledOnce,
  getInstalledCliPath,
  getEmbeddedBinaryPath,
};
