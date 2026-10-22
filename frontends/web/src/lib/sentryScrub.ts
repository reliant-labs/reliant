/**
 * What the browser may send to Sentry.
 *
 * Sentry is told what broke and where: the error type, the stack trace, the
 * release, and the identifiers needed to find the run (chat, workflow, thread,
 * tool call, user id). It is never told what the user was doing: prompts, chat
 * messages, tool inputs and outputs, file contents, diffs, command output or
 * credentials.
 *
 * A scrubber cannot recognise content, so this one does not try. Free-form
 * fields are dropped unless their key is known to hold an identifier or a small
 * enum AND the value looks like one. Error messages are too useful to drop, so
 * they are cut to their first line and a short prefix with credential shapes
 * redacted — a ConnectError's message routinely wraps a server error that
 * itself wraps tool output.
 *
 * The same policy runs on the backend (internal/telemetry/scrub.go) and in
 * Electron's main process (electron/src/sentry-scrub.js). Keep them in step.
 */
import type { Breadcrumb, Event } from "@sentry/react";

export const MAX_MESSAGE_CHARS = 160;
export const TRUNCATED_MARKER = " [truncated]";
export const REDACTED = "[redacted]";

const SECRET_PATTERNS: ReadonlyArray<readonly [RegExp, string]> = [
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

/** Keys holding error text: kept, bounded like an exception message. */
const MESSAGE_KEYS = new Set(["error", "err", "message", "logmessage"]);

/** Keys holding small enums worth keeping (compared via normalizeKey). */
const METADATA_KEYS = new Set([
  "type", "kind", "component", "operation", "procedure", "method", "code", "status", "statuscode",
  "errortype", "errorcategory", "errorcode", "errorname", "activitytype", "provider", "model", "driver",
  "service", "phase", "step", "laststep", "source", "level", "category", "environment", "release", "version",
  "url", "baseurl", "route", "from", "to", "grpcservice", "grpcmethod", "grpccode", "durationms",
  "attempt", "attemptnumber", "logsource", "funnelevent", "funnelstep",
  // Span attributes the performance UI relies on.
  "sentryop", "sentryorigin", "sentrysource", "httpmethod", "httprequestmethod", "httpurl", "urlfull",
  "serveraddress", "httpresponsestatuscode",
]);

/** Keys that may carry a query string or fragment (OAuth codes, signed URLs). */
const URL_KEYS = new Set(["url", "baseurl", "from", "to", "httpurl", "urlfull"]);

/** One token, no whitespace: the shape of an id or an enum, not of prose or code. */
const IDENTIFIER_VALUE = /^[A-Za-z0-9_.:/@+%-]{1,128}$/;
const IDENTIFIER_KEY_SUFFIXES = ["_id", "_ids", "Id", "Ids", "ID", "IDs"];

/** SDK-populated contexts that carry environment facts, not user data. */
const STANDARD_CONTEXTS = new Set([
  "app", "browser", "os", "device", "runtime", "trace", "culture", "react", "replay", "cloud_resource",
]);

/** "GET /path" — what an HTTP span describes itself as. Anything else is dropped. */
const SPAN_DESCRIPTION = /^(?:[A-Z]+ )?[A-Za-z0-9_.:/@+%-]{1,200}$/;

function normalizeKey(key: string): string {
  return key.replace(/[_\-. ]/g, "").toLowerCase();
}

function isIdentifierKey(key: string): boolean {
  return normalizeKey(key) === "id" || IDENTIFIER_KEY_SUFFIXES.some((suffix) => key.endsWith(suffix));
}

export function stripQuery(url: string): string {
  const cut = url.search(/[?#]/);
  return cut >= 0 ? url.slice(0, cut) : url;
}

function redactSecrets(s: string): string {
  return SECRET_PATTERNS.reduce((acc, [re, repl]) => acc.replace(re, repl), s);
}

/**
 * Keeps the first line of s, redacts credential shapes, and cuts it to
 * MAX_MESSAGE_CHARS. Multi-line strings are where tool output, file contents
 * and stderr end up, so everything after the first line goes.
 */
export function boundMessage(s: string): string {
  if (!s) return s;
  const newline = s.indexOf("\n");
  let first = (newline >= 0 ? s.slice(0, newline) : s).replace(/[\r\t ]+$/, "");
  first = redactSecrets(first);
  let cut = newline >= 0;
  const chars = Array.from(first);
  if (chars.length > MAX_MESSAGE_CHARS) {
    first = chars.slice(0, MAX_MESSAGE_CHARS).join("");
    cut = true;
  }
  return cut ? first + TRUNCATED_MARKER : first;
}

function scrubValue(key: string, value: unknown): { keep: boolean; value?: unknown } {
  if (typeof value === "number" || typeof value === "boolean") {
    // A number or a flag cannot carry a prompt.
    return { keep: true, value };
  }
  if (typeof value !== "string") {
    // Objects and arrays are exactly where request bodies and tool payloads
    // hide; nothing here inspects them.
    return { keep: false };
  }
  const norm = normalizeKey(key);
  if (MESSAGE_KEYS.has(norm)) return { keep: true, value: boundMessage(value) };
  if (!isIdentifierKey(key) && !METADATA_KEYS.has(norm)) return { keep: false };
  const candidate = URL_KEYS.has(norm) ? stripQuery(value) : value;
  return IDENTIFIER_VALUE.test(candidate) ? { keep: true, value: candidate } : { keep: false };
}

/** Keeps scalar values whose key and shape pass the allowlist. */
export function scrubFields<T>(input: Record<string, T> | undefined): Record<string, T> | undefined {
  if (!input) return undefined;
  const out: Record<string, T> = {};
  for (const [key, value] of Object.entries(input)) {
    const result = scrubValue(key, value);
    if (result.keep) out[key] = result.value as T;
  }
  return out;
}

/** ui.click messages are DOM selector paths; attribute values can be labels or titles. */
function scrubSelector(selector: string): string {
  return boundMessage(selector.replace(/\[([\w-]+)=("[^"]*"|'[^']*'|[^\]]*)\]/g, "[$1]"));
}

export function scrubBreadcrumb(breadcrumb: Breadcrumb): Breadcrumb | null {
  if (breadcrumb.category === "console") {
    // console.* arguments are chat text, tool output, whatever was logged.
    // Keep that a line was logged, and at what level — nothing it said.
    const kept: Breadcrumb = {};
    if (breadcrumb.category !== undefined) kept.category = breadcrumb.category;
    if (breadcrumb.level !== undefined) kept.level = breadcrumb.level;
    if (breadcrumb.type !== undefined) kept.type = breadcrumb.type;
    if (breadcrumb.timestamp !== undefined) kept.timestamp = breadcrumb.timestamp;
    return kept;
  }
  const scrubbed: Breadcrumb = { ...breadcrumb };
  if (typeof scrubbed.message === "string") {
    scrubbed.message = breadcrumb.category?.startsWith("ui.")
      ? scrubSelector(scrubbed.message)
      : boundMessage(scrubbed.message);
  }
  if (breadcrumb.data) scrubbed.data = scrubFields(breadcrumb.data);
  return scrubbed;
}

/**
 * Replay records navigation and resource timings outside the breadcrumb path,
 * each described by its full URL — an OAuth callback's included. Keep the
 * path, drop the query and fragment.
 */
export function scrubRecordingEvent<T>(event: T): T {
  const data = (event as { data?: { tag?: unknown; payload?: { description?: unknown } } }).data;
  if (data?.tag === "performanceSpan" && typeof data.payload?.description === "string") {
    data.payload.description = stripQuery(data.payload.description);
  }
  return event;
}

function scrubSpanDescription(description: string | undefined): string | undefined {
  if (description === undefined) return undefined;
  const stripped = stripQuery(description);
  return SPAN_DESCRIPTION.test(stripped) ? stripped : undefined;
}

/**
 * Applies the policy to an error or transaction event and returns it. It never
 * drops the event: that something failed, and where, is what Sentry is for.
 */
export function scrubEvent<T extends Event>(event: T): T {
  if (event.message) event.message = boundMessage(event.message);
  if (event.logentry) {
    event.logentry = { message: event.logentry.message ? boundMessage(event.logentry.message) : undefined };
  }

  for (const ex of event.exception?.values ?? []) {
    if (ex.value) ex.value = boundMessage(ex.value);
    if (ex.mechanism) {
      // Type and handled say how it was caught; data can hold the rejection reason.
      delete ex.mechanism.data;
    }
    for (const frame of ex.stacktrace?.frames ?? []) {
      delete frame.vars;
    }
  }

  if (event.extra) event.extra = scrubFields(event.extra);
  if (event.tags) event.tags = scrubFields(event.tags);

  if (event.contexts) {
    for (const [name, context] of Object.entries(event.contexts)) {
      if (STANDARD_CONTEXTS.has(name) || !context) continue;
      const kept = scrubFields(context as Record<string, unknown>);
      if (kept && Object.keys(kept).length > 0) event.contexts[name] = kept;
      else delete event.contexts[name];
    }
  }

  if (event.request) {
    // URL without query or fragment, and the User-Agent Sentry derives the
    // browser context from. Never cookies, body, or credentials.
    const userAgent = event.request.headers?.["User-Agent"];
    event.request = {
      ...(event.request.url ? { url: stripQuery(event.request.url) } : {}),
      ...(userAgent ? { headers: { "User-Agent": userAgent } } : {}),
    };
  }

  if (event.breadcrumbs) {
    event.breadcrumbs = event.breadcrumbs
      .map(scrubBreadcrumb)
      .filter((crumb): crumb is Breadcrumb => crumb !== null);
  }

  if (event.user) {
    event.user = event.user.id !== undefined ? { id: event.user.id } : {};
  }

  for (const span of event.spans ?? []) {
    span.description = scrubSpanDescription(span.description);
    if (span.data) span.data = scrubFields(span.data) ?? {};
  }

  return event;
}
