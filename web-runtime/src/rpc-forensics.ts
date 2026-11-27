// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The redacted shape of a failed RPC's request: what `forge.forensics` carries
// on the browser's RPC span (rpc-span.ts), so "why did this call fail for that
// user" has the arguments' shape beside the error, and never their content.
//
// ── Deny by default ──────────────────────────────────────────────────────
//
// A field is recorded only in a form that cannot carry what a person typed.
// The rules are forge's server-side forensics rules (docs/proposals/
// runtime-context.md, B.3), applied to the request message by reflection:
//
//   number, bool                      the value (a 64-bit integer beyond 2^53
//                                     as its decimal string)
//   number with a secret name         "[len=N]": its digit count, never its
//     (isSecretNumberNamed: `pin`,    value, whether or not the name is
//     `cvv`, `security_code`,         id-shaped; a quantity (`max_tokens`,
//     `session_id`)                   `session_count`) keeps its value
//   enum                              the value's name, else its number
//   string named like an id           the value, cut to MAX_VALUE_CHARS
//     (`id`, `ids`, `uuid`, `*_id`, `*_ids`, `*_uuid`, `*Id`, `*ID`,
//     `*Ids`, `*IDs`, `*Uuid`, `*UUID`)
//   ...whose name also names a secret "[len=N]": `session_id`, `apiKeyId`,
//                                     `otp_id` are credentials, not handles
//   any other string                  "[len=N]": its length only
//   bytes                             "[len=N]"
//   repeated, map                     "[count=N]": its length only
//   a temporal type: Timestamp,       "[type=<Name>]" (e.g. "[type=Timestamp]"):
//     Duration, google.type Date,     its type only, never its value and never
//     DateTime, TimeOfDay, Interval,  its subfields. A time is request content
//     Month, DayOfWeek                (a birthdate, an appointment), not shape.
//   other message                     its set fields, recursively, down to
//                                     MAX_DEPTH levels ("[message]" below that)
//   field option `debug_redact`       never: the field is left out entirely
//
// A name is secret when, lowercased with `_` and `-` removed, it contains
// token, secret, passw, credential, session, cookie, apikey, privatekey,
// signingkey or otp; or when one of its words (split on `_`, `-`, camelCase
// and letter/digit boundaries), or two adjacent words joined, is pin, otp,
// cvv, cvc, ssn, tan, passcode, securitycode, mfa or totp, plural or not —
// so `spinner`, `pinned`, `shipping` and `tank` are not. A NUMBER whose
// first or last word is max, min, limit, count, total, size, len or length,
// or whose FIRST word is num, is a quantity and keeps its value
// (`max_tokens`, `num_sessions`), unless its name is id-shaped
// (`max_session_id` records its digit count); a trailing num reads as "number", so
// `pin_num` still records its digit count, and a string named like a
// quantity still records its length. A bool or an enum
// keeps its value under a secret name (`mfa_enabled`, `session_state`):
// either is one of a few values the schema itself lists, so it cannot carry
// anything a person typed. These are forge's server-side rules
// (pkg/observe/forensic_names.go), tested against the same table.
//
// Unset fields are left out, so a record names what the caller sent. Keys are
// `forensics.arg.<proto field name>`, nested fields joined with `.`
// (`forensics.arg.filter.owner_id`). At most MAX_ARGS fields are recorded;
// a record cut short says so with `forensics.truncated = true`.
//
// The `debug_redact` option survives into generated code: protoc-gen-es
// embeds each file's descriptor with its field options (only json names and
// source info are stripped), so `field.proto.options.debugRedact` is the
// option as the .proto declared it.
//
// DOM-free: the interceptors entry point (React Native) reaches it.
import {
  ScalarType,
  type DescField,
  type DescMessage,
} from "@bufbuild/protobuf";
import {
  reflect,
  type ReflectList,
  type ReflectMap,
  type ReflectMessage,
} from "@bufbuild/protobuf/reflect";

/** Attribute values a span event accepts. */
export type ForensicsValue = string | number | boolean;

/** How many levels of nested messages are walked below the request. */
export const MAX_DEPTH = 3;
/** The most fields one record carries. */
export const MAX_ARGS = 32;
/** The longest id value recorded; longer ones are cut. */
export const MAX_VALUE_CHARS = 128;

/** The event name forge's forensics records share, browser and server. */
export const FORENSICS_EVENT = "forge.forensics";
/** The attribute prefix of a recorded argument. */
export const FORENSICS_ARG_PREFIX = "forensics.arg.";

// `id`, `ids`, `uuid`, `uuids`, `owner_id`, `owner_uuids` (proto's
// snake_case, any case), and `ownerId`, `ownerID`, `ownerIds`, `ownerIDs`,
// `ownerUuid`, `ownerUUID` (plural too) for a .proto that spells fields in
// camel. Spelled exactly as forge's server-side rule (idShapedName in
// pkg/observe/forensic_names.go), with no `i` flag so both engines read
// them alike.
const SNAKE_ID_RE = /(^|_)([uU][uU])?[iI][dD][sS]?$/;
const CAMEL_ID_RE = /[a-z0-9](Id|ID|Ids|IDs|Uuid|UUID|Uuids|UUIDs)$/;

/** Whether a field's name says it holds an identifier. */
export function isIdShaped(name: string): boolean {
  return SNAKE_ID_RE.test(name) || CAMEL_ID_RE.test(name);
}

// The id shape forge's server matched before isIdShaped narrowed it
// (legacyIDShapedName in pkg/observe/forensic_names.go): `id`, `ids`, `ID`,
// `IDs`, `Id`, `Ids`, or any name ending `_id`, `_ids`, `ID`, `IDs`, `Id` or
// `Ids` (`max_SESSIONID`, `maxTOKENId`). A secret name of this shape is
// never a quantity, so no credential the server hid before starts recording
// its value.
const LEGACY_ID_RE = /^(id|ids|ID|IDs|Id|Ids)$|.(_ids?|IDs?|Ids?)$/;

// Fragments that make a name a secret wherever they appear: a session id or
// an API key id authenticates whoever holds it. Matched against the name
// lowercased with `_` and `-` removed, so `api_key_id`, `apiKeyId` and
// `API-KEY-ID` all hit.
const SECRET_FRAGMENTS = [
  "token",
  "secret",
  "passw",
  "credential",
  "session",
  "cookie",
  "apikey",
  "privatekey",
  "signingkey",
  "otp",
];

// Secrets too short to match as fragments (`pin` is in `spinner` and
// `shipping`, `tan` in `tank`): they match only a whole word of the name
// (nameWords), or two adjacent words joined (`security_code`, `passCode`),
// optionally plural (`pins`).
const SECRET_WORDS = new Set([
  "pin",
  "otp",
  "cvv",
  "cvc",
  "ssn",
  "tan",
  "passcode",
  "securitycode",
  "mfa",
  "totp",
]);

/**
 * Whether a field's name says its value is a secret, whatever its shape or
 * type: it contains a secret fragment, or one of its words (or two adjacent
 * words joined) is a secret word, plural or not. `pin`, `userPIN`, `cvv2`,
 * `security_code` and `SSNs` are secret; `spinner`, `pinned`, `shipping`
 * and `tank` are not. forge's server-side rule (ForensicSecretName in
 * pkg/observe) is the same, tested against the same table.
 */
export function isSecretNamed(name: string): boolean {
  const folded = name.toLowerCase().replace(/[_-]/g, "");
  if (SECRET_FRAGMENTS.some((w) => folded.includes(w))) {
    return true;
  }
  const words = nameWords(name);
  return words.some(
    (w, i) =>
      secretWord(w) || (i + 1 < words.length && secretWord(w + words[i + 1])),
  );
}

// Words that make a number a quantity, not a secret, as its first or last
// word: `max_tokens`, `token_limit`, `session_count`.
const QUANTITY_WORDS = new Set([
  "max",
  "min",
  "limit",
  "count",
  "total",
  "size",
  "len",
  "length",
]);

// A quantity only as the FIRST word (`num_sessions`): last, it reads as
// "number", and `pin_num`, `ssn_num` and `otpNum` are the secret itself.
const LEADING_QUANTITY_WORD = "num";

/**
 * Whether a NUMBER field's name hides its value: a secret name that is
 * id-shaped, or whose first and last words are not quantity words (max, min,
 * limit, count, total, size, len, length, and num as the first word only).
 * An id-shaped secret name is never a quantity: a session or OTP id is the
 * credential itself. So `pin`, `pin_num`, `otp_counter_id` and
 * `max_session_id` record their digit count, and `max_tokens`,
 * `session_count` and `num_sessions` their value. The exemption is
 * for numbers only: a string under any secret name records its length.
 * forge's server-side rule (ForensicSecretNumberName) is the same.
 */
export function isSecretNumberNamed(name: string): boolean {
  if (!isSecretNamed(name)) {
    return false;
  }
  if (isIdShaped(name) || LEGACY_ID_RE.test(name)) {
    return true;
  }
  const words = nameWords(name);
  if (words.length === 0) {
    return true;
  }
  const first = words[0]!;
  const last = words[words.length - 1]!;
  return (
    !QUANTITY_WORDS.has(first) &&
    first !== LEADING_QUANTITY_WORD &&
    !QUANTITY_WORDS.has(last)
  );
}

function secretWord(w: string): boolean {
  return (
    SECRET_WORDS.has(w) || (w.endsWith("s") && SECRET_WORDS.has(w.slice(0, -1)))
  );
}

const isLower = (c: string): boolean => c >= "a" && c <= "z";
const isUpper = (c: string): boolean => c >= "A" && c <= "Z";
const isDigit = (c: string): boolean => c >= "0" && c <= "9";

/**
 * nameWords splits a name into lowercase words at `_` and `-`, at a
 * lower-to-upper change (`userPin` → user, pin), at the end of an upper-case
 * run followed by a word (`PINCode` → pin, code, but `SSNs` stays one word),
 * and between letters and digits (`cvv2` → cvv, 2). Only ASCII decides a
 * boundary; any other character belongs to the word around it.
 */
function nameWords(name: string): string[] {
  const words: string[] = [];
  let start = -1;
  const flush = (end: number): void => {
    if (start >= 0 && end > start) {
      words.push(
        name.slice(start, end).replace(/[A-Z]/g, (c) => c.toLowerCase()),
      );
    }
    start = -1;
  };
  const at = (i: number): string => name[i] ?? "";
  for (let i = 0; i < name.length; i++) {
    const c = at(i);
    if (c === "_" || c === "-") {
      flush(i);
      continue;
    }
    if (start < 0) {
      start = i;
      continue;
    }
    const p = at(i - 1);
    const next = at(i + 1);
    if (
      (isLower(p) && isUpper(c)) ||
      isDigit(p) !== isDigit(c) ||
      (isUpper(p) &&
        isUpper(c) &&
        isLower(next) &&
        (next !== "s" || isLower(at(i + 2))))
    ) {
      flush(i);
      start = i;
    }
  }
  flush(name.length);
  return words;
}

/** Whether a field's value is recorded as it is: an id that is no secret. */
function recordsIdValue(name: string): boolean {
  return isIdShaped(name) && !isSecretNamed(name);
}

/**
 * classifyRequest returns the forensics attributes of a request message under
 * the rules in the header, keyed `forensics.arg.<path>`. Never throws: a
 * message it cannot walk yields what was classified before the failure.
 */
export function classifyRequest(
  desc: DescMessage | undefined,
  message: unknown,
): Record<string, ForensicsValue> {
  const out: Record<string, ForensicsValue> = {};
  if (!desc || typeof message !== "object" || message === null) {
    return out;
  }
  const state = { count: 0, truncated: false };
  try {
    walk(reflect(desc, message as never, false), "", 0, out, state);
  } catch {
    // Forensics must never break the call it describes.
  }
  if (state.truncated) {
    out["forensics.truncated"] = true;
  }
  return out;
}

interface WalkState {
  count: number;
  truncated: boolean;
}

function walk(
  msg: ReflectMessage,
  prefix: string,
  depth: number,
  out: Record<string, ForensicsValue>,
  state: WalkState,
): void {
  for (const field of msg.sortedFields) {
    if (state.truncated) {
      return;
    }
    if (field.proto.options?.debugRedact === true || !msg.isSet(field)) {
      continue;
    }
    const path = prefix + field.name;
    if (field.fieldKind === "message") {
      const value = msg.get(field) as ReflectMessage;
      const temporal = temporalName(field.message.typeName);
      if (temporal !== undefined) {
        put(out, state, path, `[type=${temporal}]`);
      } else if (depth >= MAX_DEPTH) {
        put(out, state, path, "[message]");
      } else {
        walk(value, `${path}.`, depth + 1, out, state);
      }
      continue;
    }
    if (field.fieldKind === "list") {
      put(out, state, path, `[count=${(msg.get(field) as ReflectList).size}]`);
      continue;
    }
    if (field.fieldKind === "map") {
      put(out, state, path, `[count=${(msg.get(field) as ReflectMap).size}]`);
      continue;
    }
    const value = classifyLeaf(field, msg.get(field));
    if (value !== undefined) {
      put(out, state, path, value);
    }
  }
}

function put(
  out: Record<string, ForensicsValue>,
  state: WalkState,
  path: string,
  value: ForensicsValue,
): void {
  if (state.count >= MAX_ARGS) {
    state.truncated = true;
    return;
  }
  out[FORENSICS_ARG_PREFIX + path] = value;
  state.count++;
}

/** A scalar or enum field's recorded value, or undefined to leave it out. */
function classifyLeaf(
  field: DescField,
  value: unknown,
): ForensicsValue | undefined {
  if (field.fieldKind === "enum") {
    const temporal = temporalName(field.enum.typeName);
    if (temporal !== undefined) {
      return `[type=${temporal}]`;
    }
    const n = Number(value);
    return field.enum.value[n]?.name ?? n;
  }
  if (field.fieldKind !== "scalar") {
    return undefined;
  }
  switch (field.scalar) {
    case ScalarType.STRING: {
      const s = String(value);
      return recordsIdValue(field.name)
        ? s.slice(0, MAX_VALUE_CHARS)
        : `[len=${s.length}]`;
    }
    case ScalarType.BYTES:
      return `[len=${(value as Uint8Array).length}]`;
    case ScalarType.BOOL:
      return Boolean(value);
    default:
      // A numeric PIN, CVV or session id is as much a secret as a string
      // one: its digit count only. A quantity (`max_tokens`) is not.
      return isSecretNumberNamed(field.name)
        ? `[len=${String(value).length}]`
        : number(value);
  }
}

function number(value: unknown): ForensicsValue {
  if (typeof value === "bigint") {
    const n = Number(value);
    return Number.isSafeInteger(n) ? n : value.toString();
  }
  if (typeof value === "string") {
    // A 64-bit field generated with jstype = JS_STRING.
    return value.slice(0, MAX_VALUE_CHARS);
  }
  const n = Number(value);
  return Number.isFinite(n) ? n : String(n);
}

// Types whose value is a point or span of time. Their value is the request's
// content (a birthdate, an appointment), so only their type is recorded.
const TEMPORAL_TYPES = new Set([
  "google.protobuf.Timestamp",
  "google.protobuf.Duration",
  "google.type.Date",
  "google.type.DateTime",
  "google.type.TimeOfDay",
  "google.type.Interval",
  "google.type.Month",
  "google.type.DayOfWeek",
]);

/** The short name of a temporal type ("Timestamp"), or undefined. */
function temporalName(typeName: string): string | undefined {
  return TEMPORAL_TYPES.has(typeName)
    ? typeName.slice(typeName.lastIndexOf(".") + 1)
    : undefined;
}

// ── Once per failure signature per page session ─────────────────────────

/** The most distinct failure signatures one page session records. */
export const MAX_SIGNATURES = 256;

const seen = new Set<string>();

/**
 * claimForensics reports whether the failure `procedure` + `code` should get
 * a forensics record now: true the first time this page session sees that
 * pair, false after, and false for every pair once MAX_SIGNATURES have been
 * recorded, so a page that fails in a loop costs a bounded amount.
 */
export function claimForensics(procedure: string, code: string): boolean {
  const key = `${procedure}\u0000${code}`;
  if (seen.has(key) || seen.size >= MAX_SIGNATURES) {
    return false;
  }
  seen.add(key);
  return true;
}

/** Test seam. */
export function resetForensics(): void {
  seen.clear();
}
