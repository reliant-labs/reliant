// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// What a Node / Electron-main ERROR record carries so the issues pipeline
// groups it: the Node twin of forge/pkg/observe's error_inputs.go.
//
// A record from `/node` has no `rum.sessionId`, so the control plane's issue
// view treats it as a SERVER error and fingerprints it with sfp1
// (forge/pkg/obsquery/fingerprint.go). sfp1 hashes the service, then
//
//	code.function.name   where the failure began (never a line number)
//	norm(Body)           the message template ("uncaught exception", ...)
//	exception.type       the kind of failure
//
// so this module derives exactly those keys, under the same names observe
// writes, plus exception.message, exception.stacktrace, code.file.path and
// code.line.number for whoever reads the issue.
//
// ── Two installs must group as one ───────────────────────────────────────
//
// A stack frame names a file on the user's disk: /Users/<name>/..., or
// C:\Users\<name>\AppData\... on Windows. That is a username, and it would
// also split one bug into an issue per machine, because code.function.name
// carries the file. So every path is rewritten before it leaves: below the
// app root it becomes relative (`src/main.js`), inside an Electron archive it
// starts after `app.asar/`, and anything else under the home directory starts
// with `~`. The same rewrite runs over the whole stack text.
//
// ── What leaves, and what does not ───────────────────────────────────────
//
// The message is cut to its first line, has credential shapes redacted (the
// same patterns `track()` uses, redactSecrets in track.ts) and is bounded.
// The stack is REBUILT as "<type>: <that message>" plus the frame lines, so a
// multi-line message (stderr, tool output, a file's contents) cannot ride in
// through the stack's first line. A thrown non-Error object contributes its
// type and a string `message` property only: an object literal is where a
// payload hides, and it is never serialized.
import { redactSecrets } from "./track.js";

/** Log attribute keys an ERROR record carries; the same keys observe writes. */
export const EXCEPTION_TYPE = "exception.type";
export const EXCEPTION_MESSAGE = "exception.message";
export const EXCEPTION_STACKTRACE = "exception.stacktrace";
export const CODE_FUNCTION = "code.function.name";
export const CODE_FILE = "code.file.path";
export const CODE_LINE = "code.line.number";
/** Pins a record to a named issue whatever its call site (observe.ErrorGroup). */
export const ERROR_GROUP = "error.group";

/** Longest exception.message, in characters: observe's MaxExceptionMessage. */
export const MAX_EXCEPTION_MESSAGE_CHARS = 1024;
/** Longest exception.stacktrace, in characters: observe's maxStacktrace. */
export const MAX_STACKTRACE_CHARS = 16 * 1024;
/** Most frames kept from a stack. V8 records 10 by default. */
export const MAX_FRAMES = 64;

const ELLIPSIS = "…";

/** How paths in a stack are rewritten. */
export interface PathScrub {
  /** Files below it are reported relative to it (Electron: app.getAppPath()). */
  appRoot?: string;
  /** Files below it that are not below appRoot start with `~`. */
  homeDir?: string;
  /** Compare paths case-insensitively (win32). */
  caseInsensitive?: boolean;
}

/** One parsed V8 stack frame. */
export interface StackFrame {
  /** The function as V8 names it, without `async `, `new ` or `[as alias]`; "" when anonymous. */
  fn: string;
  /** The file, already rewritten by the PathScrub. */
  file: string;
  line: number;
  column: number;
}

// `    at fn (file:line:col)`, `    at file:line:col`, `    at async fn (...)`.
// The file part is greedy up to the LAST :line:col, so a Windows drive letter
// (C:\...) or a URL scheme (file:///...) stays in it.
const FRAME_RE = /^\s*at (?:(.*?) \()?(.+):(\d+):(\d+)\)?\s*$/;

/** Normalize separators so one comparison serves POSIX and Windows paths. */
function slashes(p: string): string {
  return p.replace(/\\/g, "/");
}

function stripFileUrl(p: string): string {
  if (!p.startsWith("file://")) {
    return p;
  }
  let rest = p.slice("file://".length);
  // file:///C:/x -> C:/x ; file:///home/x -> /home/x
  if (/^\/[A-Za-z]:\//.test(rest)) {
    rest = rest.slice(1);
  }
  try {
    return decodeURIComponent(rest);
  } catch {
    return rest;
  }
}

function under(
  path: string,
  root: string | undefined,
  ci: boolean,
): string | null {
  if (!root) {
    return null;
  }
  let r = slashes(root).replace(/\/+$/, "");
  if (r === "") {
    return null;
  }
  let p = path;
  if (ci) {
    r = r.toLowerCase();
    p = path.toLowerCase();
  }
  if (p === r) {
    return "";
  }
  if (p.startsWith(r + "/")) {
    return path.slice(r.length + 1);
  }
  return null;
}

/**
 * The path a record may carry for `file`: relative below the app root, from
 * inside an Electron archive, `~`-rooted below the home directory, otherwise
 * unchanged. Pure; exported for tests.
 */
export function scrubPath(file: string, scrub: PathScrub): string {
  const ci = scrub.caseInsensitive ?? false;
  const p = slashes(stripFileUrl(file));
  const rel = under(p, scrub.appRoot, ci);
  if (rel !== null) {
    return rel;
  }
  const asar = p.search(/\/app\.asar(?:\.unpacked)?\//);
  if (asar !== -1) {
    return p.slice(p.indexOf("/", asar + 1) + 1);
  }
  const home = under(p, scrub.homeDir, ci);
  if (home !== null) {
    return home === "" ? "~" : `~/${home}`;
  }
  return p;
}

/** Parse a V8 stack's frame lines. Lines that are not frames are skipped. */
export function parseStack(stack: string, scrub: PathScrub): StackFrame[] {
  const frames: StackFrame[] = [];
  for (const line of stack.split("\n")) {
    if (frames.length >= MAX_FRAMES) {
      break;
    }
    const m = FRAME_RE.exec(line);
    if (!m) {
      continue;
    }
    // `Timeout.explode [as _onTimeout]`: the alias is the property the
    // function was reached through, which changes with the caller.
    const fn = (m[1] ?? "")
      .replace(/^(?:async |new )/, "")
      .replace(/ \[as [^\]]*\]$/, "")
      .trim();
    frames.push({
      fn,
      file: scrubPath(m[2] ?? "", scrub),
      line: Number(m[3]),
      column: Number(m[4]),
    });
  }
  return frames;
}

/**
 * Whether a frame is the application's own code: not Node or Electron
 * internals, not a dependency, not eval. The innermost such frame is where a
 * failure "began" for grouping, as observe takes the innermost application
 * frame of a Go stack.
 */
export function isApplicationFrame(frame: StackFrame): boolean {
  const f = frame.file;
  if (f === "" || f === "<anonymous>" || f === "native") {
    return false;
  }
  if (
    f.startsWith("node:") ||
    f.startsWith("internal/") ||
    f.startsWith("electron/")
  ) {
    return false;
  }
  if (f.includes("node_modules/") || f.includes("node_modules.asar")) {
    return false;
  }
  if (f.includes("electron.asar") || f.includes("electron/js2c")) {
    return false;
  }
  return true;
}

/**
 * code.function.name for a frame: V8's own frame shape without line and
 * column, `createWindow (src/main.js)`. The file is part of it because sfp1
 * hashes this attribute and not code.file.path, and two `handle` functions in
 * two files are two places.
 */
export function frameFunction(frame: StackFrame): string {
  return `${frame.fn || "<anonymous>"} (${frame.file})`;
}

/** Cut to `max` characters, ellipsis included. */
export function bound(value: string, max: number): string {
  if (value.length <= max) {
    return value;
  }
  return value.slice(0, Math.max(0, max - ELLIPSIS.length)) + ELLIPSIS;
}

/** First line, credential shapes redacted, bounded: what a message may say. */
export function boundMessage(message: string): string {
  const nl = message.indexOf("\n");
  const first = (nl === -1 ? message : message.slice(0, nl)).replace(
    /\s+$/,
    "",
  );
  // Cut before redacting so the scan is bounded, with headroom so a secret
  // straddling the final cut is still seen whole.
  return bound(
    redactSecrets(first.slice(0, MAX_EXCEPTION_MESSAGE_CHARS * 2)),
    MAX_EXCEPTION_MESSAGE_CHARS,
  );
}

/**
 * exception.type for a thrown value: the class name for an Error (a subclass
 * that never set `name` still reports its class), `typeof` for anything else.
 */
export function exceptionType(value: unknown): string {
  if (value instanceof Error) {
    const ctor = (value as { constructor?: { name?: unknown } }).constructor
      ?.name;
    if (
      typeof ctor === "string" &&
      ctor !== "" &&
      ctor !== "Error" &&
      ctor !== "Object"
    ) {
      return ctor;
    }
    return typeof value.name === "string" && value.name !== ""
      ? value.name
      : "Error";
  }
  if (value === null) {
    return "null";
  }
  return typeof value;
}

function rawMessage(value: unknown): string {
  if (value instanceof Error) {
    return typeof value.message === "string" ? value.message : "";
  }
  switch (typeof value) {
    case "string":
      return value;
    case "number":
    case "boolean":
    case "bigint":
    case "symbol":
      return String(value);
    case "object": {
      // Never serialized: an object literal is where a payload hides.
      const m = (value as { message?: unknown } | null)?.message;
      return typeof m === "string" ? m : "";
    }
    default:
      return "";
  }
}

/** Attribute values an ERROR record carries. */
export type ExceptionRecordAttributes = Record<string, string | number>;

/**
 * The attributes an ERROR record about `value` carries: exception.type,
 * exception.message, and — when the value has a stack — exception.stacktrace
 * and the code.* location of its innermost application frame. Pure given
 * `scrub`; never throws.
 */
export function exceptionAttributes(
  value: unknown,
  scrub: PathScrub,
): ExceptionRecordAttributes {
  const type = exceptionType(value);
  const message = boundMessage(rawMessage(value));
  const out: ExceptionRecordAttributes = {
    [EXCEPTION_TYPE]: type,
    [EXCEPTION_MESSAGE]: message,
  };
  let stack: unknown;
  try {
    stack = (value as { stack?: unknown } | null)?.stack;
  } catch {
    stack = undefined;
  }
  if (typeof stack !== "string" || stack === "") {
    return out;
  }
  const frames = parseStack(stack, scrub);
  if (frames.length === 0) {
    return out;
  }
  const header = message === "" ? type : `${type}: ${message}`;
  const lines = frames.map(
    (f) =>
      `    at ${f.fn ? `${f.fn} (${f.file}:${f.line}:${f.column})` : `${f.file}:${f.line}:${f.column}`}`,
  );
  // The top of the stack — where it failed — is the part kept.
  out[EXCEPTION_STACKTRACE] = bound(
    redactSecrets([header, ...lines].join("\n")),
    MAX_STACKTRACE_CHARS,
  );
  const origin = frames.find(isApplicationFrame) ?? frames[0];
  if (origin) {
    out[CODE_FUNCTION] = frameFunction(origin);
    out[CODE_FILE] = origin.file;
    out[CODE_LINE] = origin.line;
  }
  return out;
}
