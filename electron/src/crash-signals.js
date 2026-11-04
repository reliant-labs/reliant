// Native crash SIGNALS for the main process: that a process died, why, and with
// what exit code. Never a minidump.
//
// A minidump is a copy of process memory and can hold prompts, file contents and
// credentials, so dumps stay off: crashReporter is never started. Electron still tells the
// main process the reason and exit code of every renderer and child-process
// death. That is enough to see a GPU crash loop or a renderer OOM, and it
// carries nothing the user typed.
//
// Electron's `details` objects are never forwarded as they are: each field of
// the event is picked by name, and enums are mapped through an allowlist.
// JS exceptions in the main process (uncaughtException, unhandledRejection) are
// already recorded by forge's Node telemetry runtime (telemetry.js), so they are
// not reported a second time here.

'use strict';

const RATE_LIMIT_MAX_EVENTS = 5;
const RATE_LIMIT_WINDOW_MS = 10 * 60 * 1000;

// Electron's `reason` values, minus 'clean-exit', which is a normal exit.
const KNOWN_REASONS = new Set([
  'crashed',
  'oom',
  'killed',
  'launch-failed',
  'integrity-failure',
  'abnormal-exit',
  'memory-eviction',
]);
const ERROR_REASONS = new Set(['crashed', 'oom', 'launch-failed', 'integrity-failure']);

// Electron's child-process-gone `type` values.
const CHILD_PROCESS_TYPES = {
  gpu: 'gpu',
  utility: 'utility',
  zygote: 'zygote',
  'sandbox helper': 'sandbox-helper',
  'pepper plugin': 'pepper-plugin',
  'pepper plugin broker': 'pepper-plugin-broker',
};

const ROUTE_SEGMENT = /^[a-z][a-z0-9-]{0,31}$/;
const SERVICE_NAME = /^[A-Za-z][A-Za-z0-9_.]{0,63}$/;

// 'clean-exit' maps to null: not a crash, nothing to report.
function mapReason(reason) {
  if (reason === 'clean-exit') return null;
  return KNOWN_REASONS.has(reason) ? reason : 'unknown';
}

function mapChildProcessType(type) {
  if (typeof type !== 'string') return 'unknown';
  return CHILD_PROCESS_TYPES[type.toLowerCase()] || 'unknown';
}

// The first path segment of the page ("chats" for /chats/<id>), when it looks
// like a route name rather than an identifier. Never a query, a fragment, an id
// or any later segment, since those can carry user content.
function safeRouteName(url) {
  if (typeof url !== 'string' || url === '') return '';
  let pathname;
  try {
    pathname = new URL(url).pathname;
  } catch {
    return '';
  }
  const first = pathname.split('/').filter(Boolean)[0] || '';
  return ROUTE_SEGMENT.test(first) ? first : '';
}

// Builds the event for one process death, or returns null if it is not a crash.
// `crash` is { processType, reason, exitCode, serviceName?, route? }.
function buildCrashEvent(crash, env) {
  const reason = mapReason(crash.reason);
  if (reason === null) return null;
  const event = {
    processType: crash.processType,
    reason,
    exitCode: Number.isInteger(crash.exitCode) ? crash.exitCode : -1,
    appVersion: env.appVersion,
    platform: env.platform,
    arch: env.arch,
    osVersion: env.osVersion,
    uptimeSeconds: Math.round(env.uptimeSeconds),
  };
  if (typeof crash.serviceName === 'string' && SERVICE_NAME.test(crash.serviceName)) {
    event.serviceName = crash.serviceName;
  }
  if (crash.route) event.route = crash.route;
  return event;
}

// The telemetry log record for a crash signal. The body and error.group are
// fixed strings built from enums, so every crash of one kind is one issue
// (error.group pins forge's sfp1 grouping; observe.ErrorGroup on the server).
// Attribute values are the event's enums, numbers and versions, nothing else.
function toTelemetryRecord(event) {
  const attributes = {
    'error.group': `native-crash/${event.processType}/${event.reason}`,
    'crash.signal': true,
    'crash.process_type': event.processType,
    'crash.reason': event.reason,
    'crash.exit_code': event.exitCode,
    'app.version': event.appVersion,
    'os.platform': event.platform,
    'os.arch': event.arch,
    'os.version': event.osVersion,
    'process.uptime_s': event.uptimeSeconds,
  };
  if (event.serviceName) attributes['crash.service_name'] = event.serviceName;
  if (event.route) attributes['crash.route'] = event.route;
  if (event.suppressedSinceLastReport) {
    attributes['crash.suppressed_since_last_report'] = event.suppressedSinceLastReport;
  }
  return {
    severity: ERROR_REASONS.has(event.reason) ? 'ERROR' : 'WARN',
    body: `native-crash ${event.processType} ${event.reason}`,
    attributes,
  };
}

// Returns report(crash): sends at most `maxEvents` per `windowMs` per process
// type, so a renderer crash loop cannot flood telemetry. The first event sent after
// a suppressed stretch says how many were dropped.
function createCrashSignalReporter({
  capture,
  log,
  env,
  now = Date.now,
  maxEvents = RATE_LIMIT_MAX_EVENTS,
  windowMs = RATE_LIMIT_WINDOW_MS,
}) {
  const sentAt = new Map();
  const suppressed = new Map();

  return function report(crash) {
    const event = buildCrashEvent(crash, env());
    if (event === null) return null;

    const t = now();
    const recent = (sentAt.get(event.processType) || []).filter((at) => t - at < windowMs);
    if (recent.length >= maxEvents) {
      sentAt.set(event.processType, recent);
      suppressed.set(event.processType, (suppressed.get(event.processType) || 0) + 1);
      log.warn(`[CrashSignals] ${event.processType} crash rate-limited (${event.reason})`);
      return null;
    }

    const dropped = suppressed.get(event.processType) || 0;
    if (dropped > 0) {
      event.suppressedSinceLastReport = dropped;
      suppressed.delete(event.processType);
    }
    recent.push(t);
    sentAt.set(event.processType, recent);

    try {
      capture(toTelemetryRecord(event));
    } catch (error) {
      log.error('[CrashSignals] failed to report crash signal:', error);
    }
    return event;
  };
}

// Registers the Electron listeners. `getActiveRoute()` names the focused
// window's route for crashes that have no web contents of their own.
function installCrashSignals({ app, report, getActiveRoute = () => '' }) {
  app.on('render-process-gone', (_event, webContents, details) => {
    let route = '';
    try {
      route = safeRouteName(webContents.getURL());
    } catch {
      // The contents may already be destroyed; the crash is reported without a route.
    }
    report({
      processType: 'renderer',
      reason: details && details.reason,
      exitCode: details && details.exitCode,
      route,
    });
  });
  app.on('child-process-gone', (_event, details) => {
    report({
      processType: mapChildProcessType(details && details.type),
      reason: details && details.reason,
      exitCode: details && details.exitCode,
      serviceName: details && details.serviceName,
      route: getActiveRoute(),
    });
  });
}

module.exports = {
  RATE_LIMIT_MAX_EVENTS,
  RATE_LIMIT_WINDOW_MS,
  mapReason,
  mapChildProcessType,
  safeRouteName,
  buildCrashEvent,
  toTelemetryRecord,
  createCrashSignalReporter,
  installCrashSignals,
};
