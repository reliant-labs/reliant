const { spawn } = require('child_process');
const path = require('path');
const fs = require('fs');
const { app } = require('electron');
const log = require('./logger');
const dotenv = require('dotenv');
const daemonCreds = require('./daemon-creds');
const {
  DAEMON_NON_INTERACTIVE_FLAG,
  DAEMON_VERBOSE_FLAG,
  DAEMON_STATE_STREAM_FIELD,
  DAEMON_STREAM_AWAITING_CREDENTIALS,
  DAEMON_STATE_CONNECTED_AT_FIELD,
  DAEMON_STATE_PID_FIELD,
  DAEMON_STREAM_NOTICE_PREFIX,
} = require('./daemon-contract');

// Default API URL — NEUTRAL (localhost) for the OSS build.
//
// The OSS app must ship NO Reliant-hosted-specific config. The packaged
// commercial build injects the hosted endpoint at build time via
// build-config.js (loadEnvironment() projects it into process.env.
// RELIANT_SERVER_URL before this default is ever consulted), exactly as the web
// build injects hosted VITE_*. An un-injected OSS build with no env therefore
// points at a local self-hosted stack instead of silently at Reliant's hosted
// API. resolveDaemonServerURL()'s precedence is env > this neutral default.
const DEFAULT_API_URL = 'http://localhost:8080';
const DEFAULT_GATEWAY_URL = ''; // Empty means the daemon will derive from the API URL

class BackendManager {
  /**
   * @param {{ authStorage?: { loadStoredAuth: () => object|null } }} [opts]
   *   `authStorage` is the Electron-side encrypted Supabase session store
   *   (see auth-storage.js). When provided, `ensureDaemonCreds()` will
   *   auto-mint a daemon PAT before the daemon binary spawns so the daemon
   *   skips its own (headless-broken) interactive registration flow.
   *   When null, `ensureDaemonCreds()` is a no-op — leaves daemon to do
   *   whatever it would normally do.
   */
  constructor(opts) {
    this.process = null;
    this.daemonPort = null;
    this.useTLS = process.env.DISABLE_TLS !== 'true'; // TLS enabled by default unless DISABLE_TLS=true
    this.isRunning = false;
    this.isShuttingDown = false;
    this.startupTimeout = 30000; // 30 seconds - daemon startup is simpler than monolith
    this.shutdownTimeout = 25000; // 25 seconds - must be longer than Go's total shutdown (15s)
    this.isDevelopment = process.env.NODE_ENV === 'development'; // Added for development mode
    this.restartAttempts = 0;
    this.maxRestartAttempts = 5;
    this.restartDelay = 1000; // Start with 1 second delay
    this.intentionalShutdown = false; // Track if shutdown was intentional
    this.instanceId = null; // Unique identifier for this instance (used in dev mode for process identification)
    this.devBinaryPath = null; // Resolved dev-mode binary path (set by resolveDevBinary / getBinaryPath)
    this.devProcessSearchPattern = null; // Pattern used by orphan cleanup to grep ps output in dev mode
    this.lastDaemonState = null; // Last runtime record checkHealth() read (see daemon-state.json)

    // Whether this process has already swept for orphaned daemons.
    //
    // The sweep exists for daemons left behind by a PREVIOUS app run — a crash,
    // a force-quit, a failed update. That is a cold-start condition: once we
    // have started and cleanly stopped our own daemon in this process, there is
    // no earlier run left to have orphaned anything, and re-running the sweep
    // costs ~93ms of blocking `ps aux` (measured) on the sign-in restart path
    // for a question whose answer we already know.
    //
    // Set only after a sweep completes, and cleared by an UNCLEAN exit (see
    // handleCrash), because a daemon that died badly may have left children.
    this.hasSweptOrphans = false;

    // Optional auth-session source for the pre-spawn PAT mint preflight.
    // Null is fine — ensureDaemonCreds() short-circuits silently in that case.
    this.authStorage = opts?.authStorage ?? null;

    // Hosted API configuration. There are TWO logical URLs here, and in
    // a packaged commercial build they collapse to the same hosted hostname
    // (injected via build-config.js), but in cloud-dev they're distinct
    // processes on different ports:
    //
    //   apiUrl         (RELIANT_SERVER_URL) — the daemon's --server target.
    //                    In cloud-dev this is admin-server; the daemon hits
    //                    admin-server's forwarder for CreateDaemonToken.
    //
    //   rendererApiUrl (RELIANT_API_URL)    — the URL the renderer's MAIN
    //                    Connect transport hits for reliant.v1.* services
    //                    (ProjectService, ChatService, WorktreeService, …).
    //                    Those live on reliant api-server, not admin-server.
    //                    Conflating the two routes the renderer at admin-server
    //                    and every non-daemon-token reliant.v1 RPC 404s.
    //
    // Default rendererApiUrl to apiUrl so prod (single URL) stays unchanged.
    this.apiUrl = BackendManager.resolveDaemonServerURL();
    this.rendererApiUrl = process.env.RELIANT_API_URL || this.apiUrl;
    this.gatewayUrl = process.env.RELIANT_GATEWAY_URL || DEFAULT_GATEWAY_URL;
    this.controlPlaneUrl = process.env.RELIANT_CONTROL_PLANE_URL || '';

    // Load environment variables on initialization
    this.loadEnvironment();
  }

  loadEnvironment() {
    // Try to load root env files (.env first, then .env.local overrides)
    const envPath = path.join(__dirname, '../../.env');
    if (fs.existsSync(envPath)) {
      const result = dotenv.config({ path: envPath });
      if (result.parsed) {
        log.info('[BackendManager] Loaded environment from .env file');
        log.info('[BackendManager] Loaded vars:', Object.keys(result.parsed).join(', '));
      }
    }

    const envLocalPath = path.join(__dirname, '../../.env.local');
    if (fs.existsSync(envLocalPath)) {
      const result = dotenv.config({ path: envLocalPath, override: true });
      if (result.parsed) {
        log.info('[BackendManager] Loaded environment from .env.local file');
        log.info('[BackendManager] Loaded vars:', Object.keys(result.parsed).join(', '));
      }
    }

    // Load build-time config (generated by CI, contains runtime secrets for packaged app)
    try {
      const buildConfig = require('./build-config');
      let injected = [];
      for (const [key, value] of Object.entries(buildConfig)) {
        if (!process.env[key] && value) {
          process.env[key] = value;
          injected.push(key);
        }
      }
      if (injected.length > 0) {
        log.info('[BackendManager] Injected build config vars:', injected.join(', '));
      }
    } catch (e) {
      // build-config.js doesn't exist in development — this is expected
      log.debug('[BackendManager] No build-config.js found (expected in dev mode)');
    }

    // Re-read API/gateway URLs after environment is loaded (env files may set them)
    this.apiUrl = BackendManager.resolveDaemonServerURL();
    this.rendererApiUrl = process.env.RELIANT_API_URL || this.apiUrl;
    this.gatewayUrl = process.env.RELIANT_GATEWAY_URL || DEFAULT_GATEWAY_URL;
    // The admin-server origin, surfaced to the renderer as
    // window.RELIANT_CONFIG.controlPlaneURL. This read has to happen HERE,
    // after build-config.js has been projected into process.env above —
    // in a packaged app that file is the only source for it.
    this.controlPlaneUrl = process.env.RELIANT_CONTROL_PLANE_URL || '';
  }

  /**
   * Resolve the URL the daemon should connect to (--server).
   *
   * Reads RELIANT_SERVER_URL — the canonical name the daemon binary itself
   * reads via root.go and that dev-start.sh / dev-k3d.sh export with that
   * name. With no env set it falls back to DEFAULT_API_URL, which is NEUTRAL
   * localhost in the OSS build (the packaged commercial build injects the
   * hosted endpoint into RELIANT_SERVER_URL via build-config.js).
   *
   * Note: RELIANT_API_URL is a different concept — it's the URL of reliant's
   * api-server (where non-daemon reliant.v1.* services live). In production
   * the two collapse to the same hostname; in cloud-dev they're distinct.
   * `this.rendererApiUrl` is the field that uses RELIANT_API_URL.
   */
  static resolveDaemonServerURL() {
    if (process.env.RELIANT_SERVER_URL) {
      return process.env.RELIANT_SERVER_URL;
    }
    // Back-compat: older dev-electron.sh / shells may have only set
    // RELIANT_API_URL. Use it but flag once so we eventually migrate.
    if (process.env.RELIANT_API_URL) {
      if (!BackendManager._warnedLegacyAPIURL) {
        BackendManager._warnedLegacyAPIURL = true;
        log.warn(
          '[BackendManager] RELIANT_SERVER_URL unset; falling back to RELIANT_API_URL ' +
            'for the daemon --server URL. These are distinct in cloud-dev — set ' +
            'RELIANT_SERVER_URL explicitly to silence this warning.'
        );
      }
      return process.env.RELIANT_API_URL;
    }
    return DEFAULT_API_URL;
  }

  /**
   * Build the daemon start args array with --server and --gateway flags.
   */
  buildDaemonArgs() {
    const args = ['daemon', 'start'];

    // Electron owns the whole sign-in flow (login page, ensureDaemonCreds
    // pre-mint, restart-on-sign-in). The daemon's own interactive
    // registration — opening a localhost browser tab — is broken under
    // headless Electron (no TTY) and must never run; this flag tells it to
    // idle and publish stream: "awaiting_credentials" instead. See
    // daemon-contract.js.
    args.push(DAEMON_NON_INTERACTIVE_FLAG);

    // --verbose selects MACHINE output on the daemon's stdout, which is what
    // this parent needs.
    //
    // Without it the daemon assumes a human is watching the terminal: it sends
    // its structured log to the rotating file only and prints a few short
    // status lines instead — and it SUPPRESSES the `@@RELIANT_STREAM <state>`
    // notices, because they are unreadable noise to a person. Those notices are
    // the push path that tells us a connect happened the moment it does.
    //
    // Dropping this flag does not break the app: watchDaemonConnection also
    // stat-polls daemon-state.json every 250ms, and that record stays
    // authoritative. It just makes every connect up to a poll interval later
    // (146ms in a measured dev sign-in) for news the daemon already had.
    // backend-manager-daemon-stream-notice.test.js pins the flag so that
    // regression fails a test rather than quietly costing latency.
    args.push(DAEMON_VERBOSE_FLAG);

    // Pass hosted API URL
    args.push('--server', this.apiUrl);

    // Pass gateway URL if explicitly configured
    if (this.gatewayUrl) {
      args.push('--gateway', this.gatewayUrl);
    }

    // Pass the daemon port
    if (this.daemonPort) {
      args.push('--port', this.daemonPort.toString());
    }

    // Pass data directory
    args.push('--data-dir', this.daemonDataDir());

    return args;
  }

  /**
   * The daemon's data directory — the exact path handed to `--data-dir` above.
   * Single definition so every reader of what the daemon writes there (the
   * runtime record, orphan cleanup) is resolved from the same value the daemon
   * was told to write to, and cannot drift from it.
   */
  daemonDataDir() {
    return this.isDevelopment
      ? './data'
      : path.join(app.getPath('userData'), 'data');
  }

  /**
   * Read the daemon's runtime record, <data-dir>/daemon-state.json, written by
   * internal/toolexec/daemonstate. Returns null when it is absent, truncated,
   * or unparseable — a record that cannot be read is simply not evidence of a
   * running daemon, and the caller polls again.
   */
  readDaemonState() {
    try {
      const raw = fs.readFileSync(
        path.join(this.daemonDataDir(), 'daemon-state.json'),
        'utf8'
      );
      return JSON.parse(raw);
    } catch (error) {
      return null;
    }
  }

  /**
   * Watch the daemon's runtime record and invoke `onConnected` the moment it
   * reports a live stream.
   *
   * WHY A WATCHER AND NOT A POLL. The renderer's daemon list is a 5s
   * React Query poll with `refetchIntervalInBackground: false`, so it STOPS
   * while the window is backgrounded — which is exactly what OAuth does when
   * it hands consent to the system browser. Measured on a real prod sign-in:
   * the daemon registered at 02:20:11.2 and reported connected at 02:20:12.4,
   * but onboarding sat on the daemon step for roughly a minute, because
   * nothing told the renderer to look again.
   *
   * The daemon publishes `stream: "connected"` to daemon-state.json on the
   * LOCAL filesystem the instant its gateway stream is up, so a file watch
   * turns a ≤60s wait into a sub-second one with no new RPC, no streaming
   * endpoint, and no extra network traffic. The 1.1s gap measured between
   * "registered" (queryable via ListDaemons) and "connected" (this event) is
   * why the renderer still refetches on the event rather than trusting it as
   * data — the event is a TRIGGER, not a source of truth.
   *
   * fs.watchFile (stat polling) rather than fs.watch: the file is rewritten
   * by a separate process, and fs.watch's rename/atomic-replace semantics
   * differ per platform, which is precisely the kind of thing that works on
   * one developer's macOS and silently never fires on Windows.
   */
  watchDaemonConnection(onConnected) {
    const statePath = path.join(this.daemonDataDir(), 'daemon-state.json');
    this.stopWatchingDaemonConnection();

    // Dedupe on the IDENTITY of the connection, not on the stream string.
    //
    // Keying on `stream` alone silently drops the event that matters most.
    // The post-sign-in restart takes the daemon connected → awaiting_credentials
    // → connected, and that middle state is routinely shorter than the 250ms
    // stat interval below, so the watcher observes "connected" both times,
    // concludes nothing changed, and never announces the NEW daemon. Measured:
    // a restart whose intermediate state lasted 80ms produced exactly one event
    // — the one for the daemon that had already gone away.
    //
    // `connected_at` is restamped on every re-establishment and `pid` changes
    // on every respawn, so together they change whenever there is genuinely a
    // new connection to report, and stay put across the repeated writes the
    // daemon makes to the same record (session counts, heartbeats).
    // Held on the instance, not in a closure, because the stdout push path
    // (handleDaemonStreamNotice) dedupes against the same value. Two paths
    // reporting the same connection must produce ONE event.
    this._onDaemonConnected = onConnected;
    const check = () => {
      const state = this.readDaemonState();
      const stream = state ? state[DAEMON_STATE_STREAM_FIELD] : null;
      const identity = state
        ? `${stream}|${state[DAEMON_STATE_CONNECTED_AT_FIELD] || ''}|${
            state[DAEMON_STATE_PID_FIELD] || ''
          }`
        : null;
      if (identity === this._lastStreamIdentity) return;
      this._lastStreamIdentity = identity;
      // Anything that is not "awaiting credentials" means the daemon has a
      // credential and has reached the gateway. Matching on the negative
      // rather than a literal "connected" keeps this working if the daemon
      // gains further stream states later.
      if (stream && stream !== DAEMON_STREAM_AWAITING_CREDENTIALS) {
        log.info('[BackendManager] Daemon reported connected; notifying renderer', {
          atMs: Date.now(),
          stream,
        });
        onConnected(stream);
      }
    };

    // 250ms is well inside a human's perception of "instant" and costs one
    // stat() — cheap next to the 5s network poll it replaces.
    fs.watchFile(statePath, { interval: 250 }, check);
    this._daemonStateWatchPath = statePath;
    // Fire once immediately: the daemon may already be connected by the time
    // a listener attaches, and a watcher that only reports future changes
    // would miss it and wait forever.
    check();
  }

  /**
   * Whether the daemon currently reports a live stream.
   *
   * Exists because an EVENT alone is not sufficient here. The renderer
   * RELOADS after the post-sign-in daemon restart, and watchDaemonConnection
   * de-duplicates on the stream value — so the "connected" event fires at the
   * outgoing renderer, and the freshly-mounted one that actually needs it
   * never receives anything. Measured: the daemon was listable at
   * 22:20:11.233 but the UI only learned at 22:20:15.289, on the next 5s
   * poll tick, with zero event deliveries in between.
   *
   * A renderer therefore ASKS on mount rather than relying on having been
   * listening at the right moment. The event stays as the fast path for a
   * renderer that is already up; this closes the reload gap.
   */
  isDaemonConnected() {
    const state = this.readDaemonState();
    const stream = state ? state[DAEMON_STATE_STREAM_FIELD] : null;
    return Boolean(stream && stream !== DAEMON_STREAM_AWAITING_CREDENTIALS);
  }

  /**
   * Parse a chunk of the daemon's stdout: surface stream notices immediately,
   * log the rest.
   *
   * Chunks are NOT lines. A pipe delivers whatever has been written when the
   * reader wakes, so a notice can arrive split across two chunks, or share a
   * chunk with several log lines. Buffering the tail until a newline is what
   * makes the prefix match reliable — matching on raw chunks drops any notice
   * unlucky enough to straddle a boundary, which would show up as an
   * occasional, unreproducible return of the slow path.
   */
  consumeDaemonStdout(chunk) {
    this._stdoutTail = (this._stdoutTail || '') + chunk;
    const lines = this._stdoutTail.split('\n');
    // Last element is the partial line (empty when the chunk ended cleanly).
    this._stdoutTail = lines.pop() ?? '';

    for (const line of lines) {
      const trimmed = line.trim();
      if (!trimmed) continue;

      if (trimmed.startsWith(DAEMON_STREAM_NOTICE_PREFIX)) {
        const stream = trimmed.slice(DAEMON_STREAM_NOTICE_PREFIX.length).trim();
        this.handleDaemonStreamNotice(stream);
        continue;
      }

      // Every mode: dev no longer inherits the daemon's stdout fd (see the
      // stdio comment in start()), so this is the only path that surfaces it.
      log.info(`[Daemon]: ${trimmed}`);
    }
  }

  /**
   * A stream transition the daemon announced on stdout.
   *
   * Routed through the SAME callback and the same dedupe as the file watcher,
   * so the push path and the poll path cannot disagree or double-fire: whichever
   * observes a given connection first reports it, and the other sees the
   * identity unchanged and stays quiet.
   *
   * Deliberately does NOT synthesise a daemon for the renderer. Registration
   * (which makes the daemon listable via ListDaemons) and the connect notice
   * were measured 1.1s apart on prod, so the notice is a REFETCH TRIGGER, not
   * data — same contract the IPC event has always had.
   */
  handleDaemonStreamNotice(stream) {
    if (!stream || stream === DAEMON_STREAM_AWAITING_CREDENTIALS) return;
    if (!this._onDaemonConnected) return;

    // Build the SAME identity the file watcher builds, by reading the record
    // the daemon wrote immediately before printing this notice.
    //
    // The two paths must produce comparable strings or they do not dedupe
    // against each other, and every connection is announced twice — once by
    // whichever path is faster and again by the other. That is not harmful
    // (the renderer treats the event as a refetch trigger, and React Query
    // coalesces), but it doubles the RPCs and makes the logs lie about what
    // happened.
    //
    // If the record is not readable yet — the notice can win the race against
    // the rename — fall back to a process-keyed identity. That degrades to at
    // most one duplicate for that connection, never to a missed one.
    const state = this.readDaemonState();
    const identity =
      state && state[DAEMON_STATE_STREAM_FIELD] === stream
        ? `${stream}|${state[DAEMON_STATE_CONNECTED_AT_FIELD] || ''}|${
            state[DAEMON_STATE_PID_FIELD] || ''
          }`
        : `${stream}||${this.process ? this.process.pid : ''}`;
    if (identity === this._lastStreamIdentity) return;
    this._lastStreamIdentity = identity;

    log.info('[BackendManager] Daemon announced stream on stdout; notifying renderer', {
      atMs: Date.now(),
      stream,
    });
    this._onDaemonConnected(stream);
  }

  stopWatchingDaemonConnection() {
    if (this._daemonStateWatchPath) {
      fs.unwatchFile(this._daemonStateWatchPath);
      this._daemonStateWatchPath = null;
    }
  }

  /**
   * Resolve dev-mode binary path, instanceId, and the ps search pattern used
   * for orphan cleanup. Idempotent — safe to call from cleanupOrphanedProcesses
   * (which runs before getBinaryPath) and again from getBinaryPath itself.
   *
   * Two modes:
   *   1. RELIANT_BACKEND_BIN env var set → use that absolute path directly.
   *      The full path is used as the ps grep pattern, so each cloud-dev /
   *      worktree instance stays uniquely identifiable without symlinks.
   *   2. Unset → fall back to ./dist/reliant + a per-worktree symlink named
   *      reliant-{instanceId} (legacy production-Electron behavior).
   */
  resolveDevBinary() {
    if (!this.isDevelopment || this.devBinaryPath) {
      return;
    }

    const envBin = process.env.RELIANT_BACKEND_BIN;
    if (envBin) {
      this.devBinaryPath = envBin;
      // Derive instanceId from the binary's enclosing dev state dir.
      // e.g. /…/control-plane/.cloud-dev-feat-foo/bin/reliant → .cloud-dev-feat-foo
      this.instanceId = path.basename(path.dirname(path.dirname(envBin)));
      // Use the absolute binary path as the ps search pattern — uniquely
      // identifies this instance's daemon across parallel worktrees.
      this.devProcessSearchPattern = envBin;
      return;
    }

    const binaryName = process.platform === 'win32' ? 'reliant.exe' : 'reliant';
    this.devBinaryPath = path.join(__dirname, '../../dist', binaryName);

    const projectRoot = path.join(__dirname, '../..');
    const worktreeMatch = projectRoot.match(/\.reliant\/worktrees\/([^/]+)/);
    this.instanceId = worktreeMatch ? worktreeMatch[1] : path.basename(projectRoot);
    this.devProcessSearchPattern = `reliant-${this.instanceId}`;
  }

  /**
   * Look up a pid's parent.
   *
   * Returns one of:
   *   { status: 'ok', ppid }  — the process exists and this is its parent
   *   { status: 'gone' }      — the process no longer exists
   *   { status: 'unknown' }   — we could not find out
   *
   * 'unknown' is a distinct answer on purpose. Every caller treats it as
   * "leave this process alone": a lineage probe that fails must not degrade
   * into the old behaviour of killing whatever matched a pattern.
   */
  readProcessParent(pid) {
    const { execSync } = require('child_process');

    if (process.platform === 'win32') {
      try {
        const out = execSync(
          `powershell -NoProfile -Command "(Get-CimInstance Win32_Process -Filter \\"ProcessId=${pid}\\").ParentProcessId"`,
          { encoding: 'utf8', timeout: 5000 }
        ).trim();
        if (!out) {
          return { status: 'gone' };
        }
        const ppid = parseInt(out, 10);
        return Number.isNaN(ppid) ? { status: 'unknown' } : { status: 'ok', ppid };
      } catch (e) {
        return { status: 'unknown' };
      }
    }

    try {
      const out = execSync(`ps -o ppid= -p ${pid}`, { encoding: 'utf8', timeout: 5000 }).trim();
      if (!out) {
        return { status: 'gone' };
      }
      const ppid = parseInt(out, 10);
      return Number.isNaN(ppid) ? { status: 'unknown' } : { status: 'ok', ppid };
    } catch (e) {
      // ps exits non-zero when the pid does not exist.
      return { status: 'gone' };
    }
  }

  /**
   * Is `pid` a live process?  Used only to ask whether a daemon's PARENT is
   * still around, so a failure to answer is reported as "alive" — the answer
   * that keeps us from killing anything.
   */
  isProcessAlive(pid) {
    const { execSync } = require('child_process');
    try {
      if (process.platform === 'win32') {
        const out = execSync(`tasklist /FI "PID eq ${pid}" /NH`, {
          encoding: 'utf8',
          timeout: 5000
        });
        return out.includes(String(pid));
      }
      execSync(`kill -0 ${pid}`, { timeout: 1000 });
      return true;
    } catch (e) {
      return false;
    }
  }

  /**
   * Decide whether a daemon pid is genuinely ORPHANED, i.e. nobody is left to
   * shut it down.
   *
   * This is the whole point of the module and the thing a `ps | grep` cannot
   * do. A command line does not say who owns the process, so pattern matching
   * cannot tell a crashed run's leftover daemon from the healthy daemon of a
   * dev stack that is running right now. Two Electron stacks in the SAME
   * worktree share a binary path, a data dir and a `--data-dir ./data`
   * argument; every byte the old grep looked at is identical between them.
   * The only thing that differs is lineage, so lineage is what we read.
   *
   * A daemon is an orphan only when its parent is provably gone: on Unix the
   * kernel reparents it to init/launchd (ppid 1) the instant its Electron
   * exits. Anything else — a live parent, a pid we cannot resolve, our own
   * child — is NOT an orphan and is left strictly alone. Killing a live
   * daemon strands a colleague's or an agent's running session; leaving a
   * stale one costs a little memory until the next launch. The asymmetry is
   * the reason every uncertain branch below answers "not an orphan".
   */
  classifyDaemonProcess(pid) {
    const numericPid = parseInt(pid, 10);

    if (Number.isNaN(numericPid) || numericPid <= 0) {
      return { orphaned: false, reason: `unparseable pid ${pid}` };
    }

    if (numericPid === process.pid) {
      return { orphaned: false, reason: 'this Electron process' };
    }

    if (this.process && numericPid === this.process.pid) {
      return { orphaned: false, reason: 'our own daemon' };
    }

    const parent = this.readProcessParent(numericPid);

    if (parent.status === 'gone') {
      return { orphaned: false, reason: 'already exited' };
    }

    if (parent.status === 'unknown') {
      return { orphaned: false, reason: 'could not determine owner' };
    }

    if (parent.ppid === process.pid) {
      // Spawned by us earlier in this same launch — start() may have raced
      // cleanup, or a restart is in flight. Either way it is not abandoned.
      return { orphaned: false, reason: 'child of this Electron process' };
    }

    if (parent.ppid <= 1) {
      return { orphaned: true, reason: 'reparented to init — its Electron is gone' };
    }

    if (this.isProcessAlive(parent.ppid)) {
      return { orphaned: false, reason: `owned by live pid ${parent.ppid}` };
    }

    // Parent is recorded but no longer alive: a dying stack we caught mid-exit.
    return { orphaned: true, reason: `owner pid ${parent.ppid} is dead` };
  }

  /**
   * On startup, terminate daemons left behind by a run that died without
   * shutting its daemon down — a crash, a force-quit, a failed update.
   *
   * Two steps, and the split matters: a `ps` pattern (or tasklist, or the
   * data-dir's runtime record) only proposes CANDIDATES, and
   * classifyDaemonProcess() alone decides. Nothing here is killed for looking
   * like a daemon; it is killed only for having no live owner.
   *
   * The comment this replaces said the dev-mode binary path scoped cleanup
   * "per-worktree", which is true and insufficient: it separates different
   * worktrees, while two dev stacks launched from the SAME worktree share one
   * RELIANT_BACKEND_BIN, one data dir, and identical daemon argv. Under the
   * old pattern-only logic a starting Electron reaped the healthy, connected
   * daemon of a stack running right beside it — SIGTERM, 2s, then SIGKILL,
   * stranding whatever tool calls were in flight. That is the regression the
   * ownership test and backend-manager-orphan-cleanup.test.js exist to stop.
   */
  async cleanupOrphanedProcesses() {
    const { execSync } = require('child_process');

    this.resolveDevBinary();

    log.info(`[BackendManager] Checking for orphaned processes (instanceId: ${this.instanceId || 'production'})`);

    try {
      if (process.platform === 'darwin' || process.platform === 'linux') {
        // The ps pattern only NARROWS the candidate set — it never decides
        // anything. It is anchored to the `daemon` subcommand because matching
        // the bare binary path would sweep in cloud-dev's air-managed
        // `reliant server api` / `server gateway` / `server worker`, which
        // share the binary; killing those made the auto-mint that runs ~2s
        // later 502 against a dead downstream. Every candidate that survives
        // the pattern is then put to classifyDaemonProcess(), which is what
        // actually establishes whether it is abandoned.
        const binaryPart = this.isDevelopment && this.devProcessSearchPattern
          ? this.devProcessSearchPattern
          : 'reliant';
        const searchPattern = `${binaryPart} daemon`;

        try {
          // Get all matching processes
          const psResult = execSync(`ps aux | grep "${searchPattern}" | grep -v grep`, {
            encoding: 'utf8',
            timeout: 5000
          }).trim();

          if (!psResult) {
            log.debug('[BackendManager] No orphaned processes found');
            return;
          }

          const lines = psResult.split('\n').filter(line => line.trim());
          const pidsToKill = [];

          for (const line of lines) {
            // Parse PID from ps output (format: "user PID %CPU %MEM ... command")
            const parts = line.trim().split(/\s+/);
            const pid = parts[1];

            // Defense in depth: even though the grep pattern includes
            // " daemon", a binary path that happens to contain "daemon" as a
            // substring (unlikely but possible) would slip through. Require
            // the `daemon` token to appear as a standalone word in the args.
            if (!/\bdaemon\b/.test(line)) {
              continue;
            }

            const { orphaned, reason } = this.classifyDaemonProcess(pid);
            if (!orphaned) {
              log.debug(`[BackendManager] Leaving daemon PID ${pid} alone (${reason})`);
              continue;
            }

            log.info(`[BackendManager] Found orphaned process: PID ${pid} (${reason})`);
            pidsToKill.push(pid);
          }

          if (pidsToKill.length > 0) {
            log.warn(`[BackendManager] Found ${pidsToKill.length} orphaned backend process(es): ${pidsToKill.join(', ')}`);

            // SIGTERM only. The daemon drains in-flight tool calls on SIGTERM;
            // SIGKILL strands them, and a daemon slow to drain is a daemon
            // doing exactly the work we would be destroying. An orphan that
            // outlives this call is harmless — it holds no data-dir lock we
            // need, and the next launch reclassifies it.
            for (const pid of pidsToKill) {
              try {
                log.info(`[BackendManager] Terminating orphaned process ${pid}...`);
                execSync(`kill -TERM ${pid}`, { timeout: 1000 });
              } catch (e) {
                // Process might already be gone
              }
            }

            log.info('[BackendManager] Orphaned process cleanup complete');
          }
        } catch (e) {
          // grep returns exit code 1 if no matches - this is fine
          if (e.status !== 1) {
            log.debug('[BackendManager] Process search failed:', e.message);
          }
        }
        
        // Also clean up orphaned crashpad handlers (only in production - these are per-app-bundle)
        if (!this.isDevelopment) {
          try {
            const crashpadResult = execSync('pgrep -f "chrome_crashpad_handler.*reliant"', { encoding: 'utf8', timeout: 5000 }).trim();
            const crashpadPids = crashpadResult.split('\n').filter(pid => pid);
            
            if (crashpadPids.length > 0) {
              log.warn(`[BackendManager] Found ${crashpadPids.length} orphaned crashpad process(es)`);
              for (const pid of crashpadPids) {
                try {
                  execSync(`kill -9 ${pid}`, { timeout: 1000 });
                } catch (e) {
                  // Ignore
                }
              }
            }
          } catch (e) {
            // No crashpad processes found
          }
        }
        
      } else if (process.platform === 'win32') {
        // Windows: Look for our unique binary name or reliant-backend.exe in prod.
        // When RELIANT_BACKEND_BIN is set, search by the binary's basename so
        // tasklist's IMAGENAME filter finds it.
        const searchName = this.isDevelopment && this.devProcessSearchPattern
          ? (process.env.RELIANT_BACKEND_BIN
              ? path.basename(process.env.RELIANT_BACKEND_BIN)
              : `reliant-${this.instanceId}.exe`)
          : 'reliant-backend.exe';
        
        try {
          // Get process info
          const tasklistResult = execSync(
            `tasklist /FI "IMAGENAME eq ${searchName}" /NH`,
            { encoding: 'utf8', timeout: 5000 }
          );
          
          // Parse PIDs from tasklist output (format: "name.exe    PID ...")
          const lines = tasklistResult.split('\n').filter(line => line.includes(searchName));
          const pidsToKill = [];
          
          for (const line of lines) {
            const match = line.match(new RegExp(`${searchName.replace('.', '\\.')}\\s+(\\d+)`));
            if (match) {
              const pid = match[1];

              // Same ownership test as the Unix path — tasklist's IMAGENAME
              // filter is as blind to who owns a process as ps is, so two
              // same-worktree stacks look identical here too.
              const { orphaned, reason } = this.classifyDaemonProcess(pid);
              if (!orphaned) {
                log.debug(`[BackendManager] Leaving daemon PID ${pid} alone (${reason})`);
                continue;
              }

              log.info(`[BackendManager] Found orphaned process: PID ${pid} (${reason})`);
              pidsToKill.push(pid);
            }
          }
          
          if (pidsToKill.length > 0) {
            log.warn(`[BackendManager] Found ${pidsToKill.length} orphaned backend process(es) on Windows`);
            
            for (const pid of pidsToKill) {
              try {
                log.info(`[BackendManager] Killing orphaned process ${pid}...`);
                execSync(`taskkill /PID ${pid} /F`, { timeout: 5000 });
              } catch (e) {
                log.debug(`[BackendManager] Failed to kill process ${pid}:`, e.message);
              }
            }
            
            log.info('[BackendManager] Orphaned process cleanup complete');
          }
        } catch (e) {
          log.debug('[BackendManager] No orphaned processes found on Windows:', e.message);
        }
      }
      
      // Also check the PID lock file for any orphaned process
      await this.cleanupFromLockFile();
      
    } catch (error) {
      // Don't let cleanup failures prevent startup
      log.error('[BackendManager] Error during orphan cleanup (continuing anyway):', error.message);
    }
  }
  
  /**
   * Check the PID lock file and kill any orphaned process recorded in it.
   * This is a fallback for cases where the process name-based cleanup misses zombies.
   */
  async cleanupFromLockFile() {
    const { execSync } = require('child_process');
    
    // Determine data directory (same logic as when starting backend)
    const dataDir = this.daemonDataDir();

    // Two records can name an orphaned process: the legacy monolith's bare-PID
    // lock, and the daemon's JSON runtime record. Each states its PID in its own
    // shape, so each carries its own reader — parsing JSON with parseInt yields
    // NaN, which this loop treats as a corrupt file and DELETES.
    //
    // `daemon.lock` is deliberately absent. It is an OS advisory lock whose PID
    // line exists for humans reading the file (daemonstate/lock.go); the claim
    // lives on the open file description, so unlinking it out from under a live
    // daemon hands the next one a fresh inode to lock and quietly defeats the
    // one-daemon-per-data-dir guarantee.
    const lockFiles = [
      { path: path.join(dataDir, '.reliant-backend.lock'), readPid: (raw) => parseInt(raw, 10) },
      { path: path.join(dataDir, 'daemon-state.json'), readPid: (raw) => JSON.parse(raw).pid },
    ];

    for (const { path: lockFilePath, readPid } of lockFiles) {
      try {
        if (!fs.existsSync(lockFilePath)) {
          continue;
        }

        // Read PID from lock file
        const content = fs.readFileSync(lockFilePath, 'utf8').trim();
        let pid;
        try {
          pid = readPid(content);
        } catch (e) {
          pid = NaN;
        }

        if (isNaN(pid) || pid <= 0) {
          log.debug('[BackendManager] Invalid PID in lock file:', content);
          // Remove invalid lock file
          try { fs.unlinkSync(lockFilePath); } catch (e) { /* ignore */ }
          continue;
        }
        
        // Naming a pid is not owning it. In dev, `--data-dir ./data` resolves
        // against Electron's cwd, so two stacks launched from the same
        // worktree write this record to the SAME file — the pid in it is
        // simply whichever daemon started last, very possibly a healthy one
        // belonging to a stack that is running right now. Put it to the same
        // ownership test as a ps hit, and act only on a proven orphan.
        const { orphaned, reason } = this.classifyDaemonProcess(pid);

        if (!orphaned) {
          log.debug(`[BackendManager] Lock file PID ${pid} left alone (${reason})`);
          // Deliberately NOT unlinking. A record that belongs to a live daemon
          // is that daemon's readiness contract with its own Electron
          // (checkHealth reads it); deleting it makes a healthy stack look
          // unhealthy and hang for the full startup timeout.
          if (reason !== 'already exited') {
            continue;
          }
        } else {
          log.warn(`[BackendManager] Found orphaned process from lock file: PID ${pid} (${reason})`);
          try {
            if (process.platform === 'win32') {
              execSync(`taskkill /PID ${pid}`, { timeout: 5000 });
            } else {
              // SIGTERM only — see the Unix scan above for why we do not
              // escalate to SIGKILL against a tools-daemon.
              execSync(`kill -TERM ${pid}`, { timeout: 1000 });
            }
            log.info(`[BackendManager] Signalled orphaned process ${pid} from lock file`);
          } catch (e) {
            // Already gone, or not ours to signal.
          }
        }

        // Only reached for a record whose process is gone or was just told to
        // exit: remove it so the next daemon starts against a clean slate.
        try {
          fs.unlinkSync(lockFilePath);
          log.info('[BackendManager] Removed stale lock file');
        } catch (e) {
          log.debug('[BackendManager] Could not remove lock file:', e.message);
        }
        
      } catch (error) {
        log.debug('[BackendManager] Error checking lock file:', error.message);
      }
    }
  }

  getBinaryPath() {

    if (this.isDevelopment) {
      this.resolveDevBinary();
      const devBinaryPath = this.devBinaryPath;
      log.info('[BackendManager] Development mode - using binary:', devBinaryPath);

      if (!fs.existsSync(devBinaryPath)) {
        log.error('[BackendManager] ERROR: Development binary not found at:', devBinaryPath);
        const hint = process.env.RELIANT_BACKEND_BIN
          ? `RELIANT_BACKEND_BIN=${devBinaryPath} does not exist. Ensure cloud-dev / dev-up has built it.`
          : `Run 'npm run build:backend' first, or export RELIANT_BACKEND_BIN to point at a prebuilt binary.`;
        throw new Error(`Development backend binary not found: ${devBinaryPath}. ${hint}`);
      }

      // When the caller supplied an explicit binary path (cloud-dev / dev-up),
      // use it directly. The full path is unique per worktree, so orphan
      // cleanup can grep ps output without needing a per-instance symlink.
      if (process.env.RELIANT_BACKEND_BIN) {
        log.info('[BackendManager] Using RELIANT_BACKEND_BIN; skipping symlink. Instance ID:', this.instanceId);
        return {
          command: devBinaryPath,
          args: this.buildDaemonArgs()
        };
      }

      // Legacy fallback: create a symlink with a unique name (reliant-{instanceId})
      // so multiple worktrees sharing the same dist/ stay distinguishable in ps.
      const binaryName = process.platform === 'win32' ? 'reliant.exe' : 'reliant';
      const uniqueBinaryName = process.platform === 'win32'
        ? `reliant-${this.instanceId}.exe`
        : `reliant-${this.instanceId}`;
      const symlinkPath = path.join(__dirname, '../../dist', uniqueBinaryName);

      try {
        if (fs.existsSync(symlinkPath)) {
          const existingTarget = fs.readlinkSync(symlinkPath);
          if (existingTarget !== devBinaryPath && existingTarget !== binaryName) {
            fs.unlinkSync(symlinkPath);
            fs.symlinkSync(binaryName, symlinkPath);
          }
        } else {
          fs.symlinkSync(binaryName, symlinkPath);
        }
        log.info(`[BackendManager] Using unique binary name: ${uniqueBinaryName}`);
      } catch (error) {
        // If symlink fails (e.g., Windows without admin), fall back to original binary
        log.warn(`[BackendManager] Could not create symlink, using original binary: ${error.message}`);
        return {
          command: devBinaryPath,
          args: this.buildDaemonArgs()
        };
      }

      log.info('[BackendManager] Starting daemon with command:', symlinkPath);
      log.info('[BackendManager] Instance ID:', this.instanceId);

      return {
        command: symlinkPath,
        args: this.buildDaemonArgs()
      };
    } else {
      // In production, use embedded binary
      const platform = process.platform;
      const arch = process.arch;

      // Map Node.js arch names to Go arch names
      const archMap = {
        'x64': 'amd64',
        'arm64': 'arm64',
        'ia32': '386'
      };

      const goArch = archMap[arch] || arch;
      const binaryName = platform === 'win32' ? 'reliant-backend.exe' : 'reliant-backend';

      // Use platform-specific binary paths
      if (platform === 'darwin') {
        // macOS: use architecture-specific binary (x64 or arm64)
        const macArch = arch === 'x64' ? 'x64' : 'arm64';
        const binaryPath = path.join(process.resourcesPath, 'server', `mac-${macArch}`, binaryName);

        log.info(`[BackendManager] Looking for macOS ${macArch} backend at: ${binaryPath}`);

        if (!fs.existsSync(binaryPath)) {
          throw new Error(`macOS ${macArch} backend binary not found: ${binaryPath}. Ensure the binary was built for ${macArch}.`);
        }

        // Ensure binary is executable on Unix systems
        try {
          fs.chmodSync(binaryPath, '755');
        } catch (error) {
          log.warn('Could not set binary permissions:', error.message);
        }

        return {
          command: binaryPath,
          args: this.buildDaemonArgs()
        };
      } else if (platform === 'win32') {
        // Windows: use win32-amd64 or win32-arm64 based on architecture
        const platformDir = arch === 'arm64' ? 'win32-arm64' : 'win32-amd64';
        const binaryPath = path.join(process.resourcesPath, 'server', platformDir, binaryName);

        log.info(`[BackendManager] Looking for Windows backend at: ${binaryPath}`);

        if (!fs.existsSync(binaryPath)) {
          throw new Error(`Windows backend binary not found: ${binaryPath}. Ensure the binary was built for platform ${platform}-${goArch}`);
        }

        return {
          command: binaryPath,
          args: this.buildDaemonArgs()
        };
      } else if (platform === 'linux') {
        // Linux: use linux-amd64 or linux-arm64 based on architecture
        const platformDir = arch === 'arm64' ? 'linux-arm64' : 'linux-amd64';
        const binaryPath = path.join(process.resourcesPath, 'server', platformDir, binaryName);

        log.info(`[BackendManager] Looking for Linux backend at: ${binaryPath}`);

        if (!fs.existsSync(binaryPath)) {
          throw new Error(`Linux backend binary not found: ${binaryPath}. Ensure the binary was built for platform ${platform}-${goArch}`);
        }

        // Ensure binary is executable on Unix systems
        try {
          fs.chmodSync(binaryPath, '755');
        } catch (error) {
          log.warn('Could not set binary permissions:', error.message);
        }

        return {
          command: binaryPath,
          args: this.buildDaemonArgs()
        };
      } else {
        throw new Error(`Unsupported platform: ${platform}`);
      }
    }
  }

  /**
   * Wait for the daemon to become ready. Resolves `true` once the gateway
   * stream is connected. Resolves `false` (does NOT reject) as soon as the
   * daemon reports it is idling under --non-interactive with no
   * credentials (isAwaitingCredentials()) — that is the expected steady
   * state until the user signs in, not a failure, and the Go daemon sits in
   * it indefinitely rather than exiting. Rejecting on a timeout here would
   * misclassify "waiting for sign-in" as "crashed": every caller of this
   * method either shows an error dialog or (in the child-process exit
   * handler's case) feeds handleCrash's 5-attempt restart loop, respawning
   * — and, pre-fix, browser-tab-opening — every attempt. Resolving `false`
   * promptly instead lets callers hand the port to the renderer (which
   * already shows the login page independent of daemon readiness) and stay
   * quiet, with no dialog and no restart loop, until sign-in triggers
   * restartBackendForAuthPrincipalChange.
   *
   * A genuine startup failure (never wrote a record, disconnected, wrong
   * pid — anything NOT "awaiting credentials") still rejects on timeout,
   * preserving the existing crash-detection contract callers rely on.
   */
  async waitForReady(timeout = null) {
    const actualTimeout = timeout || this.startupTimeout;

    return new Promise((resolve, reject) => {
      const startTime = Date.now();

      const checkReady = async () => {
        // The daemon doesn't expose a local HTTP health endpoint; readiness is
        // read out of the runtime record it publishes. See checkHealth().
        try {
          const isReady = await this.checkHealth();
          if (isReady) {
            log.info(`[BackendManager] Daemon ready after ${Date.now() - startTime}ms`);
            resolve(true);
            return;
          }
        } catch (error) {
          // Daemon not ready yet, continue polling
        }

        // Waiting for the user to sign in is not a timeout condition —
        // settle immediately rather than either rejecting or blocking the
        // caller for the full startup timeout.
        if (this.isAwaitingCredentials()) {
          log.info('[BackendManager] Daemon awaiting credentials — not ready, but not a crash');
          resolve(false);
          return;
        }

        // Check if we've exceeded timeout
        if (Date.now() - startTime > actualTimeout) {
          reject(new Error(
            `Daemon failed to become ready within ${actualTimeout}ms — ${this.describeDaemonState()}`
          ));
          return;
        }

        // Schedule next check - poll faster for quicker startup
        setTimeout(checkReady, 50);
      };

      // Start checking
      checkReady();
    });
  }

  /**
   * Check whether the spawned daemon is ready to serve tool calls.
   *
   * In dial-out mode the daemon binds no local port, so there is nothing to
   * probe over HTTP. It publishes its liveness in <data-dir>/daemon-state.json
   * instead (internal/toolexec/daemonstate), and two things must hold there:
   *
   *   1. the record's `pid` is OUR child — a record left behind by an earlier
   *      run must never read as this process being up; and
   *   2. the gateway stream is established (`connected`), the only state in
   *      which the daemon can actually serve a tool call.
   *
   * (2) is what a bare process-and-PID-file check could never give. The record
   * exists from startup onward carrying `stream: "connecting"`, so treating its
   * mere presence as readiness reports a daemon still stuck in registration as
   * ready — the false positive that used to hide a credential-less daemon
   * dropping into its interactive OAuth flow.
   *
   * Server mode is the exception: there the daemon accepts gateway dial-ins
   * rather than making one, so `listening` is its healthy steady state and
   * waiting for `connected` would never return.
   */
  checkHealth() {
    return new Promise((resolve) => {
      // If no process, not healthy
      if (!this.process || this.process.killed) {
        this.lastDaemonState = null;
        resolve(false);
        return;
      }

      const state = this.readDaemonState();
      this.lastDaemonState = state;

      // No record yet, or one belonging to some other (earlier) daemon.
      if (!state || state.pid !== this.process.pid) {
        resolve(false);
        return;
      }

      resolve(
        state.server_mode
          ? state.stream === 'listening' || state.stream === 'connected'
          : state.stream === 'connected'
      );
    });
  }

  /**
   * Is the running daemon idling under --non-interactive with no
   * credentials, per its own runtime record?
   *
   * This is the distinction the whole "make Electron patient" fix hinges
   * on: a daemon in this state is NOT crashed and NOT stuck — it is doing
   * exactly what --non-interactive tells it to do, and will sit here
   * indefinitely until Electron mints a PAT and respawns it after sign-in.
   * Callers (waitForReady, the whenReady startup path, handleCrash) must
   * treat it as distinct from every other non-ready state, which is exactly
   * "still starting up" or "actually broken".
   *
   * Reads the record fresh (like checkHealth) rather than reusing
   * lastDaemonState, since callers poll this independently of checkHealth.
   */
  isAwaitingCredentials() {
    if (!this.process || this.process.killed) {
      return false;
    }
    const state = this.readDaemonState();
    if (!state || state.pid !== this.process.pid) {
      return false;
    }
    return state[DAEMON_STATE_STREAM_FIELD] === DAEMON_STREAM_AWAITING_CREDENTIALS;
  }

  /**
   * One-line account of the last runtime record checkHealth() saw, for the
   * startup-timeout error. "Never wrote a record", "still connecting", and
   * "connected then dropped" are three different failures; the message should
   * name which one happened instead of making the reader go find the file.
   */
  describeDaemonState() {
    const state = this.lastDaemonState;
    if (!state) {
      return 'no runtime record written — the daemon exited or never started';
    }
    if (this.process && state.pid !== this.process.pid) {
      return `runtime record belongs to pid ${state.pid}, not our daemon (pid ${this.process.pid})`;
    }
    const detail = state.stream_detail ? `: ${state.stream_detail}` : '';
    return `gateway stream is "${state.stream || 'unknown'}"${detail}`;
  }

  async start() {
    const startTime = Date.now();
    log.info('[BackendManager] start() called');

    // Clean up any orphaned processes from previous runs before starting.
    // This prevents "app can't be opened" errors after failed updates.
    //
    // Cold start only. The sweep's whole purpose is daemons abandoned by an
    // EARLIER app run, so it answers a cold-start question; on a restart we
    // just watched our own daemon exit, in this process, and no previous run
    // can have appeared in the meantime. Re-sweeping there spends ~93ms of
    // blocking `ps aux` (measured, and it blocks the event loop) on the
    // sign-in path to rediscover nothing.
    //
    // Skipping it also REMOVES risk rather than adding it: every sweep is
    // another chance to misclassify a sibling dev stack's healthy daemon,
    // which is a regression that has happened before (see the comment on
    // cleanupOrphanedProcesses).
    if (!this.hasSweptOrphans) {
      await this.cleanupOrphanedProcesses();
      this.hasSweptOrphans = true;
    } else {
      log.info('[BackendManager] Skipping orphan sweep (already swept this session)');
    }

    // Check if external backend is already running (dev mode with Air)
    if (process.env.RELIANT_EXTERNAL_BACKEND) {
      const externalDaemonPort = parseInt(process.env.TOOLS_DAEMON_PORT, 10);
      
      log.info('[BackendManager] Using external backend (Air/dev mode)');
      log.info('[BackendManager] External daemon port:', externalDaemonPort);
      
      this.daemonPort = externalDaemonPort;
      this.isRunning = true;
      
      log.info('[BackendManager] ✓ External backend is ready');
      return this.daemonPort;
    }

    log.info('[BackendManager] API URL:', this.apiUrl);
    log.info('[BackendManager] Gateway URL:', this.gatewayUrl || '(auto-derived from API URL)');

    if (this.isRunning) {
      log.info('[BackendManager] Daemon already running on port:', this.daemonPort);
      return this.daemonPort;
    }

    if (this.isShuttingDown) {
      log.info('[BackendManager] Daemon is shutting down, waiting...');
      // Wait for shutdown to complete using event-driven approach
      await new Promise((resolve) => {
        const checkShutdown = () => {
          if (!this.isShuttingDown) {
            resolve();
          } else {
            // Check again in next tick
            setImmediate(checkShutdown);
          }
        };
        checkShutdown();
      });
      return this.start();
    }

    try {
      // Find available port for daemon (or use env var if set by dev script)
      if (process.env.TOOLS_DAEMON_PORT) {
        this.daemonPort = parseInt(process.env.TOOLS_DAEMON_PORT, 10);
        log.info(`[BackendManager] Using TOOLS_DAEMON_PORT from environment: ${this.daemonPort}`);
      } else {
        const findFreePort = require('find-free-port');
        [this.daemonPort] = await findFreePort(9190, 9290);
        log.info(`[BackendManager] ✓ Daemon port found: ${this.daemonPort}`);
      }

      // Get binary path
      const binaryPath = this.getBinaryPath();
      // Build environment variables
      const homeDir = require('os').homedir();
      let backendEnv = {
        ...process.env,
        TOOLS_DAEMON_PORT: this.daemonPort.toString(),
        // User config: ~/.reliant/ (user-editable, git-friendly)
        RELIANT_USER_CONFIG_DIR: path.join(homeDir, '.reliant'),
        // Internal app data: platform-specific (databases, analytics, auth, cache)
        RELIANT_APP_DATA_DIR: app.getPath('userData'),
        // Logs: platform-specific logs directory
        RELIANT_LOGS_PATH: app.getPath('logs'),
        // Temp: system temp directory
        RELIANT_TEMP_PATH: app.getPath('temp'),
        // CRITICAL: Set environment mode for the Go backend
        // In packaged apps, NODE_ENV is not set automatically, causing the backend to default to dev mode
        RELIANT_ENV: this.isDevelopment ? 'dev' : 'prod',
        // Pass hosted API URLs to the daemon process as env vars
        RELIANT_SERVER_URL: this.apiUrl,
      };

      // Pass gateway URL if explicitly configured
      if (this.gatewayUrl) {
        backendEnv.RELIANT_GATEWAY_URL = this.gatewayUrl;
      }

      // CRITICAL: In production, ensure PATH includes common locations for MCP tools
      // MCP servers often use npx, uvx, python, etc. which aren't in Electron's default PATH
      if (!this.isDevelopment) {
        const isWindows = process.platform === 'win32';
        const pathSeparator = isWindows ? ';' : ':';

        let additionalPaths = [];

        if (isWindows) {
          // Windows-specific paths
          additionalPaths = [
            path.join(process.env.PROGRAMFILES || 'C:\\Program Files', 'nodejs'),
            path.join(process.env.APPDATA || path.join(homeDir, 'AppData', 'Roaming'), 'npm'),
            path.join(homeDir, 'AppData', 'Local', 'Programs', 'Python'),
            path.join(homeDir, '.local', 'bin'),
            path.join(homeDir, 'go', 'bin'),  // Go binaries on Windows
          ];
        } else {
          // Unix-specific paths (macOS/Linux)
          additionalPaths = [
            '/usr/local/bin',
            '/opt/homebrew/bin',  // macOS Apple Silicon Homebrew
            '/usr/bin',
            '/bin',
            path.join(homeDir, 'go', 'bin'),  // Go binaries (air, etc.)
            path.join(homeDir, '.local', 'bin'),  // Python user binaries (uvx, etc.)
            path.join(homeDir, '.npm-global', 'bin'),  // Global npm binaries
            '/opt/homebrew/opt/python/libexec/bin',  // Homebrew python
            '/usr/local/go/bin',  // System Go installation
          ];

          // Try to find nvm-managed node installations (Unix only)
          const nvmDir = path.join(homeDir, '.nvm', 'versions', 'node');
          if (fs.existsSync(nvmDir)) {
            try {
              const versions = fs.readdirSync(nvmDir);
              if (versions.length > 0) {
                // Use the latest version (or default)
                const latestVersion = versions.sort().reverse()[0];
                const nvmNodeBin = path.join(nvmDir, latestVersion, 'bin');
                if (fs.existsSync(nvmNodeBin)) {
                  additionalPaths.push(nvmNodeBin);
                  log.info('[BackendManager] Found nvm node installation:', nvmNodeBin);
                }
              }
            } catch (err) {
              log.warn('[BackendManager] Error scanning nvm directory:', err.message);
            }
          }
        }

        // Get existing PATH and deduplicate
        const existingPath = backendEnv.PATH || backendEnv.Path || '';
        const pathParts = existingPath.split(pathSeparator).filter(p => p);

        // Add additional paths if they exist and aren't already in PATH
        for (const dir of additionalPaths) {
          if (fs.existsSync(dir) && !pathParts.includes(dir)) {
            pathParts.push(dir);
          }
        }

        backendEnv.PATH = pathParts.join(pathSeparator);
        if (isWindows) {
          backendEnv.Path = backendEnv.PATH; // Windows uses both PATH and Path
        }
        log.info('[BackendManager] Production PATH set to:', backendEnv.PATH);

        // Ensure critical environment variables are set for MCP servers
        if (!isWindows) {
          if (!backendEnv.SHELL) {
            backendEnv.SHELL = '/bin/zsh';  // macOS default
          }
        }
        if (!backendEnv.HOME && !isWindows) {
          backendEnv.HOME = homeDir;
        }
        if (!backendEnv.USERPROFILE && isWindows) {
          backendEnv.USERPROFILE = homeDir;
        }
        // Some MCP servers need USER/USERNAME
        if (!backendEnv.USER && !isWindows) {
          backendEnv.USER = require('os').userInfo().username;
        }
        if (!backendEnv.USERNAME && isWindows) {
          backendEnv.USERNAME = require('os').userInfo().username;
        }
        // TERM is needed for scripts that use tput or terminal capabilities
        if (!backendEnv.TERM && !isWindows) {
          backendEnv.TERM = 'xterm-256color';
        }

        log.info('[BackendManager] Environment variables set:', {
          SHELL: backendEnv.SHELL,
          HOME: backendEnv.HOME,
          USERPROFILE: backendEnv.USERPROFILE,
          USER: backendEnv.USER,
          USERNAME: backendEnv.USERNAME,
          PATH_length: backendEnv.PATH.length
        });
      }

      // Set data directory based on mode
      // In development: use ./data (relative to cwd) for worktree isolation
      // In production: use userData/data for shared app data
      if (!backendEnv.RELIANT_DATA_DIR) {
        backendEnv.RELIANT_DATA_DIR = this.isDevelopment
          ? './data'  // Dev: local to each worktree
          : path.join(app.getPath('userData'), 'data');  // Prod: shared app data
      }

      // Spawn daemon process.
      //
      // Pipe ALL THREE in every mode. stdout is read unconditionally below —
      // it carries the stream notice, which is the push path that reports
      // readiness without waiting on the 250ms stat poll. `inherit` makes Node
      // set child.stdout/child.stderr to null, so inheriting in dev threw
      // "Cannot read properties of null (reading 'on')" immediately after
      // spawn: the daemon was already running, but the error aborted start()
      // before the readiness wiring, orphaning it and surfacing 30s later as
      // "Daemon failed to become ready — no runtime record written".
      //
      // Dev used to rely on the inherited fd to show daemon output. It no
      // longer can, so consumeDaemonStdout and the stderr handler log in every
      // mode instead — same output, one path, and it lands in the forge log.
      //
      // CRITICAL: stdin (index 0) must stay piped so the daemon can detect when
      // the parent process dies (Suicide Pact pattern).
      const stdio = ['pipe', 'pipe', 'pipe'];

      log.info('[BackendManager] Spawning daemon:', binaryPath.command, binaryPath.args.join(' '));

      // Pre-flight: make sure ~/.reliant/daemon.json has a valid PAT for the
      // current --server origin. The daemon's own interactive registration
      // flow is broken under Electron (no TTY); we mint the PAT here so the
      // daemon finds existing creds and skips registration. Never throws —
      // every failure path inside ensureDaemonCreds is logged and swallowed.
      await this.ensureDaemonCreds();

      this.process = spawn(binaryPath.command, binaryPath.args, {
        cwd: process.cwd(),
        stdio: stdio,
        env: backendEnv,
        windowsHide: true  // Prevent console window from appearing on Windows
      });

      log.info('[BackendManager] Daemon process started with PID:', this.process.pid);

      // CRITICAL: Keep stdin stream alive for the "Suicide Pact" pattern to work correctly.
      // The Go backend monitors stdin for EOF to detect when the parent process dies.
      // If we don't keep this stream alive, Node.js may auto-close it, causing premature shutdown.
      if (this.process.stdin) {
        // Prevent errors from crashing the process
        this.process.stdin.on('error', (err) => {
          log.debug('[BackendManager] stdin stream error (expected on shutdown):', err.message);
        });
        // Keep the stream referenced to prevent garbage collection
        this.stdinStream = this.process.stdin;
      }

      // Read the daemon's stdout in EVERY mode.
      //
      // This used to be production-only, purely to avoid duplicating log lines
      // in dev. It now also carries the stream notice (see
      // consumeDaemonStdout), which is the push path that tells us the daemon
      // is up without waiting on the 250ms stat poll — and dev is where the
      // sign-in restart is exercised most, so skipping it there would leave the
      // fast path untested by everyday use.
      this.process.stdout.on('data', (data) => {
        this.consumeDaemonStdout(data.toString());
      });

      this.process.stderr.on('data', (data) => {
        const output = data.toString().trim();
        // Every mode, same reason as stdout: dev no longer inherits this fd.
        if (output) {
          log.error(`[Daemon Error]: ${output}`);
        }
      });

      this.process.on('error', (error) => {
        log.error('Failed to start daemon process:', error);
        this.isRunning = false;
      });

      this.process.on('exit', (code, signal) => {
        log.info(`[BackendManager] Daemon process exited (code: ${code}, signal: ${signal})`);
        this.isRunning = false;
        this.process = null;

        // Auto-restart on crash if not intentionally shutting down
        if (!this.isShuttingDown && !this.intentionalShutdown && code !== 0) {
          log.error('Daemon crashed unexpectedly, attempting restart...');
          // A daemon that died badly may have left children behind — the exact
          // condition the orphan sweep exists for. Re-arm it so the restart
          // that follows sweeps, even though this process already has once.
          this.hasSweptOrphans = false;
          this.handleCrash();
        } else if (code === 0) {
          // Reset restart attempts on clean exit
          this.restartAttempts = 0;
        }
      });

      // Wait for daemon to be ready. Resolves `false` (does not throw) when
      // the daemon is legitimately idling under --non-interactive with no
      // credentials — the process is alive and spawned successfully, it is
      // just not yet able to serve tool calls. isRunning tracks "the daemon
      // process exists and did not crash"; isReady()/checkHealth() is the
      // separate, correct place to ask "can it serve a tool call right now".
      const readyStart = Date.now();
      const ready = await this.waitForReady();
      if (ready) {
        log.info(`[BackendManager] ✓ Daemon ready in ${Date.now() - readyStart}ms`);
      } else {
        log.info(`[BackendManager] Daemon spawned but awaiting credentials (not yet ready) after ${Date.now() - readyStart}ms`);
      }

      this.isRunning = true;

      log.info(`[BackendManager] ✓✓✓ Total daemon startup: ${Date.now() - startTime}ms`);
      return this.daemonPort;

    } catch (error) {
      log.error('[BackendManager] Failed to start daemon:', error);
      log.error('[BackendManager] Error stack:', error.stack);
      await this.cleanup();
      throw error;
    }
  }

  /**
   * Pre-spawn check: ensure ~/.reliant/daemon.json has a PAT entry for the
   * current `--server` origin. Delegates to daemon-creds.js — the seam stays
   * here so callers can mock at the BackendManager boundary and the
   * orchestration body stays unit-testable in isolation.
   *
   * Failure-mode contract (owned by daemon-creds): NEVER throws.
   */
  async ensureDaemonCreds() {
    await daemonCreds.ensureDaemonPATForOrigin({
      authStorage: this.authStorage,
      apiUrl: this.apiUrl,
      gatewayUrl: this.gatewayUrl || process.env.RELIANT_GATEWAY_URL || '',
      // GoTrue provider for the pre-mint session refresh. Same precedence
      // family as the rest of the config: the daemon-side names
      // (RELIANT_AUTH_*, what `reliant daemon start` itself reads for its
      // OAuth flow) win over the renderer-side VITE_SUPABASE_* pair; both
      // come from the closed deploy config (dev-electron Taskfile env /
      // packaged build-config.js — loadEnvironment() has already projected
      // those into process.env by the time start() runs). When neither is
      // set (OSS build, no hosted config) the refresh is disabled and the
      // mint uses the stored token as before.
      authUrl: process.env.RELIANT_AUTH_URL || process.env.VITE_SUPABASE_URL || '',
      authAnonKey: process.env.RELIANT_AUTH_KEY || process.env.VITE_SUPABASE_ANON_KEY || '',
      logger: log,
    });
  }

  async cleanup() {
    // Clean up stdin stream reference
    if (this.stdinStream) {
      this.stdinStream = null;
    }
    if (this.process && !this.process.killed) {
      try {
        this.process.kill('SIGKILL');
      } catch (e) {
        log.error('Error killing process:', e);
      }
    }
    this.process = null;
    this.isRunning = false;
    this.daemonPort = null;
  }

  async handleCrash() {
    if (this.restartAttempts >= this.maxRestartAttempts) {
      log.error(`[BackendManager] Max restart attempts (${this.maxRestartAttempts}) reached, giving up`);
      // Emit an event or notify the main window about the failure
      return;
    }

    this.restartAttempts++;
    const delay = Math.min(this.restartDelay * this.restartAttempts, 10000); // Max 10 seconds delay

    log.info(`[BackendManager] Restart attempt ${this.restartAttempts}/${this.maxRestartAttempts} in ${delay}ms...`);

    setTimeout(async () => {
      try {
        await this.start();
        log.info('[BackendManager] Daemon restarted successfully');
        // Reset attempts after successful restart
        this.restartAttempts = 0;
      } catch (error) {
        log.error('[BackendManager] Failed to restart daemon:', error);
        // Will retry if attempts remaining
        this.handleCrash();
      }
    }, delay);
  }

  async stop() {
    // Skip stopping if using external backend
    if (process.env.RELIANT_EXTERNAL_BACKEND) {
      log.info('[BackendManager] External backend - skipping stop (managed externally)');
      this.isRunning = false;
      return;
    }

    if (!this.process || this.isShuttingDown) {
      return;
    }

    this.intentionalShutdown = true; // Mark as intentional shutdown
    this.isShuttingDown = true;
    log.info('[BackendManager] Stopping daemon gracefully...');

    return new Promise((resolve) => {
      let cleanupDone = false;

      const cleanup = () => {
        if (cleanupDone) return;
        cleanupDone = true;

        this.process = null;
        this.isRunning = false;
        this.daemonPort = null;
        this.isShuttingDown = false;
        this.intentionalShutdown = false; // Reset for next start
        resolve();
      };

      // Set a timeout for forced kill
      const forceKillTimeout = setTimeout(() => {
        if (this.process && !this.process.killed) {
          log.warn('[BackendManager] Force killing daemon after timeout');
          try {
            this.process.kill('SIGKILL');
          } catch (e) {
            log.error('Error force killing:', e);
          }
        }
        cleanup();
      }, this.shutdownTimeout);

      // Listen for exit
      this.process.once('exit', (code, signal) => {
        log.info(`[BackendManager] Daemon exited gracefully (code: ${code}, signal: ${signal})`);
        clearTimeout(forceKillTimeout);
        cleanup();
      });

      // Try graceful shutdown with SIGTERM
      try {
        this.process.kill('SIGTERM');
      } catch (error) {
        log.error('Error sending SIGTERM:', error);
        clearTimeout(forceKillTimeout);
        cleanup();
      }
    });
  }

  /**
   * Logout cleanup: drop the daemon.json entry for the current --server
   * origin. Clears the PAT, owner sub, and stable daemon_id together so the
   * next login mints fresh credentials and the server assigns a fresh daemon
   * id (logout may precede a user switch). Best-effort — swallows errors so
   * it never blocks the logout path.
   *
   * @returns {boolean} true if an entry was removed.
   */
  clearDaemonCredsForOrigin() {
    try {
      const removed = daemonCreds.deleteEntry({
        apiUrl: this.apiUrl,
        logger: log,
      });
      if (removed) {
        log.info('[BackendManager] Cleared daemon.json entry for origin on logout:', this.apiUrl);
      }
      return removed;
    } catch (e) {
      log.warn('[BackendManager] Failed to clear daemon.json entry on logout:', e?.message || e);
      return false;
    }
  }

  getPort() {
    return this.daemonPort;
  }

  getStatus() {
    return {
      isRunning: this.isRunning,
      daemonPort: this.daemonPort,
      apiUrl: this.apiUrl,
      rendererApiUrl: this.rendererApiUrl,
      gatewayUrl: this.gatewayUrl,
      controlPlaneUrl: this.controlPlaneUrl,
      isShuttingDown: this.isShuttingDown,
    };
  }

  async isReady() {
    if (!this.isRunning) {
      return false;
    }

    try {
      return await this.checkHealth();
    } catch {
      return false;
    }
  }

  // Emergency cleanup - called on app crash
  emergencyStop() {
    if (this.process && !this.process.killed) {
      try {
        this.process.kill('SIGKILL');
      } catch (e) {
        // Ignore errors during emergency stop
      }
    }
  }
}

module.exports = BackendManager;