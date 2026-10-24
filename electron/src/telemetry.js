// Main-process telemetry: forge's Node runtime (@reliantlabs/forge-web-runtime/node)
// shipping to ClickStack over OTLP, in place of @sentry/electron.
//
// What leaves the app is what forge's runtime sends: uncaught exceptions and
// unhandled rejections as ERROR records (exception type, the first line of the
// message with credential shapes redacted, a stack whose paths carry no home
// directory or install location), the crash signals of crash-signals.js, the
// release, the platform, and the signed-in user's opaque id. No minidump is
// ever produced or uploaded: crashReporter is never started.
//
// WHERE it goes is the renderer's own OTEL_ENDPOINT — the keyed ingest URL
// (https://<ingest host>/k/<key>) in the release document control-plane's KCL
// projects into electron/release.config.json (reliant_desktop_release_config).
// One key serves both processes, and its origin allowlist already holds the
// renderer's origin, app://bundle, which is the Origin the main process sends.
// RELIANT_OTEL_ENDPOINT overrides it, for pointing a packaged build at a test
// ingest. Neither set, or `off`: telemetry is off and nothing is installed.
//
// Records are batched for at most BATCH_DELAY_MS. gracefulShutdown and the
// update install await flush() before they exit; a quit that bypasses both
// loses at most that window. forge's flushOnQuit is NOT used: it defers
// before-quit and quits again itself, and main.js's before-quit already defers
// for gracefulShutdown, so the two would cut each other short.

'use strict';

const { APP_ORIGIN } = require('./app-protocol');

/** `<project>-<frontend>`, as forge derives a frontend's service.name. */
const TELEMETRY_PROJECT = 'reliant';
const TELEMETRY_FRONTEND = 'desktop';
const FLUSH_TIMEOUT_MS = 2000;
const BATCH_DELAY_MS = 1000;
const MAX_USER_ID_CHARS = 128;

function loadRuntime() {
  return require('@reliantlabs/forge-web-runtime/node');
}

/**
 * The ingest base the main process ships to, or "" for none.
 *
 * @param {{ env?: Record<string, string | undefined>, release?: Record<string, string> }} input
 */
function telemetryEndpoint({ env = {}, release = {} }) {
  const override = (env.RELIANT_OTEL_ENDPOINT || '').trim();
  if (override) return override;
  return String(release.OTEL_ENDPOINT || '').trim();
}

/**
 * The runtime's config for this app.
 *
 * @param {{ endpoint: string, version: string, isPrerelease: boolean, appRoot: string,
 *   serviceName: string, onExportError?: (e: Error) => void }} input
 */
function telemetryConfig({ endpoint, version, isPrerelease, appRoot, serviceName, onExportError }) {
  return {
    serviceName,
    serviceVersion: version,
    serviceNamespace: TELEMETRY_PROJECT,
    appClient: 'electron',
    endpoint,
    origin: APP_ORIGIN,
    appRoot,
    batchDelayMs: BATCH_DELAY_MS,
    resourceAttributes: { 'app.channel': isPrerelease ? 'prerelease' : 'stable' },
    onExportError,
  };
}

/**
 * The main process's telemetry handle. Every method is safe to call whether
 * or not telemetry is running, and none of them throws.
 *
 * @param {{ log: { info: Function, warn: Function }, load?: () => any }} deps
 */
function createMainTelemetry({ log, load = loadRuntime }) {
  let runtime = null;
  let instance = null;
  let warnedExport = false;

  function ensureRuntime() {
    if (!runtime) runtime = load();
    return runtime;
  }

  return {
    /** Starts telemetry; returns whether it is running. */
    start({ endpoint, version, isPrerelease, appRoot }) {
      if (instance) return true;
      try {
        const rt = ensureRuntime();
        instance = rt.initNodeTelemetry(
          telemetryConfig({
            endpoint,
            version,
            isPrerelease,
            appRoot,
            serviceName: rt.frontendServiceName(TELEMETRY_PROJECT, TELEMETRY_FRONTEND),
            onExportError: (error) => {
              // Once: an unreachable ingest must not fill the log.
              if (warnedExport) return;
              warnedExport = true;
              log.warn('[Telemetry] export failed:', error && error.message);
            },
          }),
        );
      } catch (error) {
        log.warn('[Telemetry] could not start:', error && error.message);
        instance = null;
      }
      return instance !== null;
    },

    /** Stops telemetry (crash reporting turned off): later calls are no-ops. */
    async stop() {
      const running = instance;
      instance = null;
      if (running) await running.shutdown(FLUSH_TIMEOUT_MS);
    },

    /** Exports everything queued; resolves within FLUSH_TIMEOUT_MS. */
    async flush() {
      if (instance) await instance.flush(FLUSH_TIMEOUT_MS);
    },

    /** The signed-in user's opaque id, or null. Anything else is ignored. */
    setUser(userId) {
      if (!instance) return;
      if (userId === null) {
        instance.setUser(null);
      } else if (typeof userId === 'string' && userId !== '' && userId.length <= MAX_USER_ID_CHARS) {
        instance.setUser(userId);
      }
    },

    /** One crash-signal record (crash-signals.js toTelemetryRecord). */
    record({ severity, body, attributes }) {
      if (!instance) return;
      const write = severity === 'ERROR' ? instance.logger.error : instance.logger.warn;
      write(body, attributes);
    },

    get running() {
      return instance !== null;
    },
  };
}

module.exports = {
  BATCH_DELAY_MS,
  FLUSH_TIMEOUT_MS,
  TELEMETRY_FRONTEND,
  TELEMETRY_PROJECT,
  createMainTelemetry,
  telemetryConfig,
  telemetryEndpoint,
};
