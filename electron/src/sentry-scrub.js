// What Electron's main process may send to Sentry.
//
// Sentry is told what broke and where: the error type, the stack trace, the
// release, the crashed process, and identifiers (user id, chat id, ...). It is
// never told what the user was doing: prompts, chat messages, tool output, file
// contents, command output, environment values or credentials.
//
// A scrubber cannot recognise content, so this one does not try. Free-form
// fields are dropped unless their key is known to hold an identifier or a small
// enum AND the value looks like one. Error messages are too useful to drop, so
// they are cut to their first line and a short prefix with credential shapes
// redacted.
//
// This is the main-process copy of a policy that also runs in the renderer
// (web/src/lib/sentryScrub.ts) and on the backend
// (internal/telemetry/scrub.go). Keep the three in step.

'use strict';

const MAX_MESSAGE_CHARS = 160;
const TRUNCATED_MARKER = ' [truncated]';
const REDACTED = '[redacted]';

const SECRET_PATTERNS = [
  // URL userinfo: postgres://user:password@host
  [/([A-Za-z][A-Za-z0-9+.-]*:\/\/)[^\s/@:]+:[^\s/@]+@/g, `$1${REDACTED}@`],
  // key=value / "key": "value" for credential-named keys, including an
  // Authorization header's scheme so the whole credential goes at once.
  [
    /\b(authorization|x-api-key|api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|client[_-]?secret|secret|password|passwd|token)(["']?\s*[:=]\s*["']?)(?:(?:bearer|basic|token)\s+)?[^\s"'&,;]+/gi,
    `$1$2${REDACTED}`,
  ],
  // A bare authorization scheme outside a key=value pair.
  [/\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+/gi, `$1 ${REDACTED}`],
  // JWTs.
  [/\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*/g, REDACTED],
  // Provider and platform key prefixes.
  [
    /\b(?:sk-ant-[A-Za-z0-9_-]{8,}|sk-[A-Za-z0-9_-]{16,}|sk_(?:live|test)_[A-Za-z0-9]{8,}|(?:rlat|dpat|gho|ghp|ghu|ghs|ghr|github_pat|glpat|xox[abprs])[_-][A-Za-z0-9_-]{10,}|AIza[0-9A-Za-z_-]{20,}|ya29\.[0-9A-Za-z_-]+|AKIA[0-9A-Z]{16})/g,
    REDACTED,
  ],
  // Long opaque runs (digests, base64 blobs). '-', '/' and '.' are excluded so
  // UUIDs, paths and dotted names are not swallowed.
  [/[A-Za-z0-9+=_]{40,}/g, REDACTED],
];

// Keys holding error text: kept, bounded like an exception message.
const MESSAGE_KEYS = new Set(['error', 'err', 'message', 'logmessage']);

// Keys holding small enums worth keeping (compared via normalizeKey).
const METADATA_KEYS = new Set([
  'type', 'kind', 'component', 'operation', 'procedure', 'method', 'code', 'status', 'statuscode',
  'errortype', 'errorcategory', 'errorcode', 'errorname', 'provider', 'model', 'service', 'phase',
  'step', 'source', 'level', 'category', 'environment', 'release', 'version',
  'url', 'baseurl', 'route', 'from', 'to', 'durationms', 'attempt',
  // Electron's own crash and process vocabulary.
  'reason', 'exitcode', 'processtype', 'crashedprocess', 'crashedurl', 'eventprocess',
  'eventenvironment', 'exitreason', 'trigger', 'event',
]);

// Keys that may carry a query string or fragment (OAuth codes, signed URLs).
const URL_KEYS = new Set(['url', 'baseurl', 'from', 'to', 'crashedurl']);

// One token, no whitespace: the shape of an id or an enum, not of prose.
const IDENTIFIER_VALUE = /^[A-Za-z0-9_.:/@+%-]{1,128}$/;
const IDENTIFIER_KEY_SUFFIXES = ['_id', '_ids', 'Id', 'Ids', 'ID', 'IDs'];

// SDK-populated contexts that carry environment facts, not user data.
const STANDARD_CONTEXTS = new Set([
  'app', 'os', 'device', 'runtime', 'trace', 'culture', 'chrome', 'node',
]);

function normalizeKey(key) {
  return String(key).replace(/[_\-. ]/g, '').toLowerCase();
}

function isIdentifierKey(key) {
  return normalizeKey(key) === 'id' || IDENTIFIER_KEY_SUFFIXES.some((suffix) => key.endsWith(suffix));
}

function stripQuery(url) {
  const cut = url.search(/[?#]/);
  return cut >= 0 ? url.slice(0, cut) : url;
}

function redactSecrets(s) {
  return SECRET_PATTERNS.reduce((acc, [re, repl]) => acc.replace(re, repl), s);
}

// Keeps the first line of s, redacts credential shapes, and cuts it to
// MAX_MESSAGE_CHARS. Multi-line strings are where tool output, file contents
// and stderr end up, so everything after the first line goes.
function boundMessage(s) {
  if (!s) return s;
  const newline = s.indexOf('\n');
  let first = (newline >= 0 ? s.slice(0, newline) : s).replace(/[\r\t ]+$/, '');
  first = redactSecrets(first);
  let cut = newline >= 0;
  const chars = Array.from(first);
  if (chars.length > MAX_MESSAGE_CHARS) {
    first = chars.slice(0, MAX_MESSAGE_CHARS).join('');
    cut = true;
  }
  return cut ? first + TRUNCATED_MARKER : first;
}

function scrubValue(key, value) {
  if (typeof value === 'number' || typeof value === 'boolean') {
    // A number or a flag cannot carry a prompt.
    return { keep: true, value };
  }
  if (typeof value !== 'string') {
    // Objects and arrays are where payloads hide; nothing here inspects them.
    return { keep: false };
  }
  const norm = normalizeKey(key);
  if (MESSAGE_KEYS.has(norm)) return { keep: true, value: boundMessage(value) };
  if (!isIdentifierKey(key) && !METADATA_KEYS.has(norm)) return { keep: false };
  const candidate = URL_KEYS.has(norm) ? stripQuery(value) : value;
  return IDENTIFIER_VALUE.test(candidate) ? { keep: true, value: candidate } : { keep: false };
}

// Keeps scalar values whose key and shape pass the allowlist.
function scrubFields(input) {
  if (!input || typeof input !== 'object') return undefined;
  const out = {};
  for (const [key, value] of Object.entries(input)) {
    const result = scrubValue(key, value);
    if (result.keep) out[key] = result.value;
  }
  return out;
}

function scrubBreadcrumb(breadcrumb) {
  if (!breadcrumb) return breadcrumb;
  if (breadcrumb.category === 'console') {
    // Logged arguments are whatever the process logged. Keep that a line was
    // logged, and at what level — nothing it said.
    const kept = {};
    for (const field of ['category', 'level', 'type', 'timestamp']) {
      if (breadcrumb[field] !== undefined) kept[field] = breadcrumb[field];
    }
    return kept;
  }
  const scrubbed = { ...breadcrumb };
  if (typeof scrubbed.message === 'string') scrubbed.message = boundMessage(scrubbed.message);
  if (breadcrumb.data) scrubbed.data = scrubFields(breadcrumb.data);
  return scrubbed;
}

// Applies the policy to an event in place and returns it. It never drops the
// event: that something failed, and where, is what Sentry is for.
function scrubEvent(event) {
  if (!event) return event;
  if (event.message) event.message = boundMessage(event.message);
  if (event.logentry) {
    event.logentry = { message: event.logentry.message ? boundMessage(event.logentry.message) : undefined };
  }

  for (const ex of (event.exception && event.exception.values) || []) {
    if (ex.value) ex.value = boundMessage(ex.value);
    if (ex.mechanism) delete ex.mechanism.data;
    for (const frame of (ex.stacktrace && ex.stacktrace.frames) || []) {
      delete frame.vars;
    }
  }

  if (event.extra) event.extra = scrubFields(event.extra);
  if (event.tags) event.tags = scrubFields(event.tags);

  if (event.contexts) {
    for (const [name, context] of Object.entries(event.contexts)) {
      if (STANDARD_CONTEXTS.has(name) || !context) continue;
      // The electron context (crashed_url, crash details, crashpad
      // annotations) goes through the same allowlist as everything else.
      const kept = scrubFields(context);
      if (kept && Object.keys(kept).length > 0) event.contexts[name] = kept;
      else delete event.contexts[name];
    }
  }

  if (event.request) {
    event.request = event.request.url ? { url: stripQuery(event.request.url) } : {};
  }

  if (Array.isArray(event.breadcrumbs)) {
    event.breadcrumbs = event.breadcrumbs.map(scrubBreadcrumb).filter(Boolean);
  }

  if (event.user) {
    // The id is enough to find a user's events. No email, name or IP.
    event.user = event.user.id !== undefined ? { id: event.user.id } : {};
  }

  return event;
}

// The main process's Sentry.init options. isEnabled is re-read on every event
// so turning crash reporting off in Settings takes effect without a restart.
function sentryMainOptions({ dsn, environment, release, isEnabled }) {
  return {
    dsn,
    environment,
    release,
    beforeBreadcrumb(breadcrumb) {
      return scrubBreadcrumb(breadcrumb);
    },
    beforeSend(event) {
      if (!isEnabled()) return null;
      return scrubEvent(event);
    },
    beforeSendTransaction(event) {
      if (!isEnabled()) return null;
      return scrubEvent(event);
    },
  };
}

module.exports = {
  MAX_MESSAGE_CHARS,
  TRUNCATED_MARKER,
  boundMessage,
  scrubBreadcrumb,
  scrubEvent,
  scrubFields,
  sentryMainOptions,
  stripQuery,
};
