/**
 * Presentation formatting for generated pages — YOURS to edit.
 * Used by list, detail, and edit page templates.
 *
 * What is NOT here, deliberately: error copy. `userMessage` and
 * `stripServerFraming` mirror the framing forge's own backend writes
 * (`forge/pkg/svcerr`), so they are a wire contract rather than a design
 * choice, and they live in `@reliantlabs/forge-web-runtime` next to
 * `normalizeError` — one implementation instead of two that a comment
 * promised to keep in sync. Import them from the package:
 *
 *   import { userMessage } from "@reliantlabs/forge-web-runtime";
 */

export function formatValue(value: unknown): string {
  if (value === null || value === undefined) return "—";
  if (typeof value === "boolean") return value ? "Yes" : "No";
  // A `bytes` column is the one protobuf-es kind whose runtime value has no
  // readable string form: String(new Uint8Array([1,2,3])) is "1,2,3", which
  // reads as a list of small numbers rather than as a blob. Size is the only
  // thing a BYTEA column DECLARES — the schema never says whether it holds a
  // thumbnail, a signature or a protobuf — so size is what gets rendered.
  if (value instanceof Uint8Array) return formatByteSize(value);
  // Handle protobuf-es Timestamp objects (have seconds/nanos properties)
  if (typeof value === "object" && value !== null && "seconds" in value) {
    try {
      const ts = value as { seconds: bigint; nanos?: number };
      return new Date(Number(ts.seconds) * 1000).toLocaleDateString(undefined, {
        year: "numeric",
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      });
    } catch {
      /* fall through */
    }
  }
  const s = String(value);
  if (/^\d{4}-\d{2}-\d{2}T/.test(s)) {
    try {
      return new Date(s).toLocaleDateString(undefined, {
        year: "numeric",
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      });
    } catch {
      return s;
    }
  }
  return s;
}

/**
 * formatByteSize renders a binary column's SIZE — "0 bytes", "812 bytes",
 * "1.2 KB", "3.4 MB" — which is what {@link formatValue} shows for a
 * protobuf-es `bytes` field (a Uint8Array at runtime).
 *
 * Size rather than content, deliberately. A BYTEA/`bytes` column is opaque by
 * declaration: nothing in the schema says whether it holds a PNG, a signature,
 * a gzip blob or a nested protobuf, so any attempt to render the CONTENT is a
 * guess about meaning that forge has no basis for — and the two obvious
 * guesses are both worse than useless in a table cell. A hex/base64 preview
 * shows a truncated prefix that is not the value and cannot be copied as one;
 * raw content risks pasting control characters and megabytes of binary into
 * the DOM. Size is derivable from the value alone, is true for every blob
 * whatever it holds, and answers the question a table can actually answer:
 * is this row's blob present, empty, or unexpectedly large?
 *
 * An empty blob renders "0 bytes", NOT the em dash: an unset column and a
 * present-but-empty one are different facts, and only `null`/`undefined`
 * are unset.
 *
 * A download / preview affordance is deliberately NOT built in here.
 * `formatValue` returns a string for a table cell, and what a blob should
 * DO when clicked (its filename, its MIME type, whether it is fetched by
 * URL or already in memory) is domain knowledge the schema does not carry.
 * Projects that want one override the column's `cell` in the generated page.
 *
 * Units are decimal (1 KB = 1000 bytes), matching how storage sizes are
 * quoted in dashboards and object stores.
 */
export function formatByteSize(bytes: Uint8Array): string {
  const n = bytes.byteLength;
  if (n < 1000) return `${n} ${n === 1 ? "byte" : "bytes"}`;
  const units = ["KB", "MB", "GB", "TB"];
  let size = n / 1000;
  let unit = 0;
  while (size >= 1000 && unit < units.length - 1) {
    size /= 1000;
    unit++;
  }
  // One decimal below 10 ("1.2 KB"), none above ("812 KB") — the second
  // digit of a three-digit size is noise in a table cell.
  return `${size < 10 ? size.toFixed(1) : Math.round(size)} ${units[unit]}`;
}

/**
 * toDatetimeLocal — convert a proto Timestamp / ISO string / Date into the
 * `YYYY-MM-DDTHH:mm` shape an <input type="datetime-local"> expects.
 * Returns "" for unset values so controlled inputs stay controlled.
 */
export function toDatetimeLocal(value: unknown): string {
  let d: Date | null = null;
  if (value instanceof Date) {
    d = value;
  } else if (
    typeof value === "object" &&
    value !== null &&
    "seconds" in value
  ) {
    const ts = value as { seconds: bigint };
    d = new Date(Number(ts.seconds) * 1000);
  } else if (typeof value === "string" && value !== "") {
    const parsed = new Date(value);
    if (!Number.isNaN(parsed.getTime())) d = parsed;
  }
  if (!d) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(
    d.getHours(),
  )}:${pad(d.getMinutes())}`;
}

export function isEnumLike(key: string, value: unknown): boolean {
  if (typeof value !== "string") return false;
  const enumKeys = [
    "status",
    "type",
    "kind",
    "role",
    "state",
    "category",
    "priority",
    "level",
  ];
  return enumKeys.some((k) => key.toLowerCase().includes(k));
}

export type BadgeVariant = "info" | "success" | "warning" | "error" | "neutral";

/**
 * BadgeVariantInput — what {@link registerStatusVariants} accepts. Mirrors the
 * <Badge> component's alias set: "danger" is a synonym for "error" and
 * "default" for "neutral", so a project can register whichever spelling its
 * design vocabulary uses without a mismatch.
 */
export type BadgeVariantInput = BadgeVariant | "danger" | "default";

function normalizeVariant(v: BadgeVariantInput): BadgeVariant {
  if (v === "danger") return "error";
  if (v === "default") return "neutral";
  return v;
}

/** Lower-case + trim a status token so map lookups are case/whitespace-stable. */
function normalizeStatusKey(value: string): string {
  return value.trim().toLowerCase();
}

/**
 * statusVariants — the built-in status-word → badge-variant map. Generic
 * lifecycle vocabulary only; domain statuses come from
 * {@link registerStatusVariants}. Lookup is EXACT on the resolved token —
 * never a substring — so a status can only ever get the colour someone
 * declared for it. (This replaced a hash-of-charcodes scheme that assigned
 * semantic colors at random — "failed" could render green and "active" red,
 * differently per value.)
 *
 * Do NOT edit this object to add product vocabulary — call
 * registerStatusVariants() from your app so the seam stays owned/extensible.
 */
const statusVariants: Record<string, BadgeVariant> = {
  active: "success",
  approved: "success",
  captured: "success",
  complete: "success",
  completed: "success",
  connected: "success",
  delivered: "success",
  done: "success",
  enabled: "success",
  fulfilled: "success",
  granted: "success",
  healthy: "success",
  online: "success",
  paid: "success",
  ready: "success",
  resolved: "success",
  sent: "success",
  shipped: "success",
  succeeded: "success",
  success: "success",
  verified: "success",

  created: "info",
  draft: "info",
  in_progress: "info",
  new: "info",
  open: "info",
  processing: "info",
  queued: "info",
  running: "info",
  scheduled: "info",
  trial: "info",

  awaiting: "warning",
  degraded: "warning",
  expiring: "warning",
  incomplete: "warning",
  on_hold: "warning",
  partial: "warning",
  paused: "warning",
  pending: "warning",
  retrying: "warning",
  suspended: "warning",
  unverified: "warning",
  warning: "warning",

  abandoned: "error",
  blocked: "error",
  canceled: "error",
  cancelled: "error",
  declined: "error",
  deleted: "error",
  delinquent: "error",
  disabled: "error",
  disconnected: "error",
  error: "error",
  exhausted: "error",
  expired: "error",
  failed: "error",
  invalid: "error",
  lost: "error",
  offline: "error",
  overdue: "error",
  past_due: "error",
  rejected: "error",
  revoked: "error",
  unhealthy: "error",
  unpaid: "error",

  // Dormant, not failed. Declared rather than left to the neutral fallback:
  // their affirmative twins above are success, and a reader looking for
  // "why is `inactive` grey?" must find a decision here, not an absence.
  archived: "neutral",
  closed: "neutral",
  inactive: "neutral",
};

/**
 * registerStatusVariants — the extension seam for domain statuses.
 *
 * The built-in map only knows GENERIC lifecycle words; a domain status like
 * `payment_captured` or `sent_to_pharmacy` isn't in it. Register your
 * vocabulary once (e.g. in a top-level module or app bootstrap) so every
 * generated Badge colors it consistently:
 *
 *   registerStatusVariants({
 *     payment_captured: "success",
 *     capture_failed:   "danger",   // "danger" is accepted → "error"
 *     needs_info:       "warning",
 *     sent_to_pharmacy: "info",
 *     renewal_required: "warning",
 *     awaiting_labs:    "warning",
 *   });
 *
 * Keys are matched case-insensitively and EXACTLY against the
 * (type-prefix-stripped) status token. Registered entries override built-ins;
 * anything still unknown renders neutral. There is no inference step: a color
 * is either declared — here or in the built-in map — or it is grey. Grey reads
 * "nobody has assigned this a meaning yet", which is recoverable; a wrong
 * color reads "this is fine", which is not.
 */
export function registerStatusVariants(
  entries: Record<string, BadgeVariantInput>,
): void {
  for (const [key, variant] of Object.entries(entries)) {
    statusVariants[normalizeStatusKey(key)] = normalizeVariant(variant);
  }
}

/**
 * screamingSnake — "OrderStatus" → "ORDER_STATUS". Used to derive an enum's
 * value-name prefix from its TS type identifier so it can be stripped.
 */
function screamingSnake(typeName: string): string {
  return typeName
    .replace(/([a-z0-9])([A-Z])/g, "$1_$2")
    .replace(/([A-Z]+)([A-Z][a-z])/g, "$1_$2")
    .toUpperCase();
}

/**
 * stripEnumTypePrefix — drop the SCREAMING_SNAKE enum-type prefix from a value
 * so both the DB/full-name form and the protobuf-es-stripped form collapse to
 * the same token:
 *
 *   stripEnumTypePrefix("ORDER_STATUS_DRAFT", "OrderStatus") // "DRAFT"
 *   stripEnumTypePrefix("DRAFT",              "OrderStatus") // "DRAFT"
 *
 * Without `enumTypeName` (a plain string column, no proto enum behind it) the
 * value is returned unchanged.
 */
function stripEnumTypePrefix(value: string, enumTypeName?: string): string {
  if (!enumTypeName) return value;
  const prefix = `${screamingSnake(enumTypeName)}_`;
  return value.toUpperCase().startsWith(prefix)
    ? value.slice(prefix.length)
    : value;
}

/**
 * ProtoEnum — the runtime shape of a protobuf-es v2 enum object.
 *
 * protoc-gen-es (`target=ts`) emits proto enums as NUMERIC TypeScript enums,
 * so the runtime object carries BOTH directions: the forward entries
 * (name → number) AND the reverse entries (number → name). Its value type is
 * therefore `string | number`, not `Record<string, number>` — typing it the
 * latter way is the TS2322 a hand-rolled enum helper trips on.
 */
export type ProtoEnum = Record<string, string | number>;

/**
 * EnumRef — the enum context every enum-aware formatter accepts.
 *
 * Prefer the ENUM OBJECT (`OrderStatus`). At runtime a proto enum field is a
 * NUMBER (`order.status === 1`), and the object is the only thing carrying the
 * number → name reverse map, so it is the only form that can turn `1` into
 * "Pending". The TYPE-NAME string ("OrderStatus") stays supported for plain
 * string columns that store a fully-qualified `ORDER_STATUS_…` token with no
 * proto enum behind them — it can strip the prefix but cannot reverse a number.
 */
export type EnumRef = ProtoEnum | string;

/** Forward-mapping member names of a protobuf-es enum (value is a number). */
function enumMemberNames(enumObject: ProtoEnum): string[] {
  return Object.keys(enumObject).filter(
    (k) => typeof enumObject[k] === "number",
  );
}

/**
 * commonEnumPrefix — the `_`-terminated prefix EVERY member name shares, or ""
 * when they share none.
 *
 * protobuf-es only strips the prefix derived from the ENUM's OWN name, so a
 * nested `Order.Status` whose values are spelled `ORDER_STATUS_DRAFT` keeps its
 * full member names at runtime (the derived prefix is `STATUS_`, which nothing
 * starts with). Deriving the shared prefix from the object catches that without
 * the caller naming the type, so the label reads "Draft", not
 * "Order Status Draft".
 */
function commonEnumPrefix(enumObject: ProtoEnum): string {
  const names = enumMemberNames(enumObject);
  if (names.length < 2) return "";
  let prefix = names[0] ?? "";
  for (const name of names.slice(1)) {
    while (prefix !== "" && !name.startsWith(prefix))
      prefix = prefix.slice(0, -1);
    if (prefix === "") return "";
  }
  // Only ever cut on a `_` boundary — a shared "PA" across PAID/PAUSED is a
  // coincidence, not a prefix.
  const cut = prefix.lastIndexOf("_");
  return cut <= 0 ? "" : prefix.slice(0, cut + 1);
}

/**
 * enumTokenFromObject — resolve any runtime spelling of an enum value to its
 * bare member name using the enum object itself.
 *
 * Handles, in order: the runtime NUMBER (`1` → "PENDING" via the reverse map),
 * a name that is already a member, a numeric string (protojson / form values),
 * and a fully-qualified DB string whose member-name suffix matches
 * ("ORDER_STATUS_PAYMENT_CAPTURED" → "PAYMENT_CAPTURED"). An unmatched value is
 * returned unchanged so an unknown token still renders instead of vanishing.
 */
function enumTokenFromObject(
  value: string | number,
  enumObject: ProtoEnum,
): string {
  if (typeof value === "number") {
    const name = enumObject[value];
    return typeof name === "string" ? name : String(value);
  }
  const raw = String(value);
  if (typeof enumObject[raw] === "number") return raw;
  if (/^\d+$/.test(raw)) {
    const name = enumObject[Number(raw)];
    if (typeof name === "string") return name;
  }
  const upper = raw.toUpperCase();
  let best = "";
  for (const name of enumMemberNames(enumObject)) {
    if (upper.endsWith(`_${name}`) && name.length > best.length) best = name;
  }
  return best === "" ? raw : best;
}

/**
 * enumToken — the single value → bare-token step both {@link enumLabel} and
 * {@link enumBadgeVariant} run, so a value can never label one way and color
 * another.
 */
function enumToken(value: string | number, enumType?: EnumRef): string {
  if (typeof enumType === "object") {
    const name = enumTokenFromObject(value, enumType);
    const prefix = commonEnumPrefix(enumType);
    return prefix !== "" && name.startsWith(prefix)
      ? name.slice(prefix.length)
      : name;
  }
  return stripEnumTypePrefix(String(value), enumType);
}

/**
 * enumBadgeVariant — map a status value to a semantic badge variant.
 *
 * Resolution: EXACT lookup of the resolved bare token in the explicit map
 * (built-ins + registerStatusVariants), else neutral. Pass the enum OBJECT
 * (`OrderStatus`) so the runtime NUMBER resolves to the same key a project
 * registered (`payment_captured`) instead of hashing the ordinal "1" to grey;
 * a type-name string still works for plain string columns.
 *
 * There is deliberately NO substring guess behind the map. One lived here and
 * returned the INVERSE of the truth for every common English negation, because
 * "inactive" contains "active" — a retired patient rendered success-green.
 * A color nobody declared is grey; grey is recoverable, green is a lie.
 * Declare domain vocabulary with {@link registerStatusVariants}.
 */
export function enumBadgeVariant(
  value: string | number | null | undefined,
  enumType?: EnumRef,
): BadgeVariant {
  if (value === null || value === undefined || value === "") return "neutral";
  return (
    statusVariants[normalizeStatusKey(enumToken(value, enumType))] ?? "neutral"
  );
}

/**
 * humanizeEnum turns a status token into a display label. Splits on
 * `_`/whitespace and title-cases: "IN_REVIEW" -> "In Review". Prefer
 * {@link enumLabel} for enum columns — it strips the redundant type prefix
 * first. Kept exported for plain (non-enum) string tokens and callers that
 * have already stripped the prefix.
 */
export function humanizeEnum(name: string | number | null | undefined): string {
  if (name === null || name === undefined || name === "") return "—";
  return String(name)
    .toLowerCase()
    .split(/[_\s]+/)
    .filter(Boolean)
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(" ");
}

/**
 * enumLabel — display label for an enum value, WITHOUT the redundant type
 * prefix.
 *
 * Pass the enum OBJECT and the raw field goes straight in — no reverse lookup
 * at the call site. A proto enum field is a NUMBER at runtime (`order.status`
 * is `1`, not `"PENDING"`), and only the object carries the number → name map:
 *
 *   enumLabel(order.status, OrderStatus)                       // "Pending"
 *   enumLabel("ORDER_STATUS_PAYMENT_CAPTURED", OrderStatus)    // "Payment Captured"
 *   enumLabel("ORDER_STATUS_PAYMENT_CAPTURED", "OrderStatus")  // "Payment Captured"
 *
 * The type-NAME string form still works for a plain string column with no proto
 * enum behind it, but it cannot reverse a number — `enumLabel(1, "OrderStatus")`
 * has nothing to look the ordinal up in and returns "1". Prefer the object.
 *
 * Renders an em dash for the unset case (an out-of-range enum number
 * reverse-maps to undefined).
 */
export function enumLabel(
  value: string | number | null | undefined,
  enumType?: EnumRef,
): string {
  if (value === null || value === undefined || value === "") return "—";
  return humanizeEnum(enumToken(value, enumType));
}

/** One option for a proto-enum-backed select / picker control. */
export interface EnumOption {
  /** Numeric enum value — the shape the wire message / zod nativeEnum expects. */
  value: number;
  /** Humanized member name, e.g. "Pending Review". */
  label: string;
  /** Raw member name, e.g. "PENDING_REVIEW". */
  name: string;
}

/**
 * enumOptions — turn a protobuf-es v2 enum into select / picker options.
 *
 * A protobuf-es enum (protoc-gen-es `target=ts`) is a NUMERIC TypeScript enum.
 * Its runtime object carries BOTH the forward (name → number) AND the reverse
 * (number → name) mappings, so its value type is `string | number`, NOT
 * `Record<string, number>` — typing a hand-rolled helper the latter way is the
 * TS2322 an enum-backed option list hits the moment it is handed `OrderStatus`.
 * This takes the enum object at that honest type, keeps only the forward
 * entries (value is a number), drops the `UNSPECIFIED` / 0 sentinel by default,
 * and humanizes each member name. Feed the result straight to whatever
 * select / picker control the frontend uses:
 *
 *   const options = enumOptions(OrderStatus);
 */
export function enumOptions(
  enumObject: ProtoEnum,
  opts?: { includeUnspecified?: boolean },
): EnumOption[] {
  const includeUnspecified = opts?.includeUnspecified ?? false;
  const out: EnumOption[] = [];
  for (const [name, value] of Object.entries(enumObject)) {
    // Skip the reverse mapping (number → name), whose value is a string.
    if (typeof value !== "number") continue;
    if (
      !includeUnspecified &&
      (value === 0 || /(^|_)UNSPECIFIED$/.test(name))
    ) {
      continue;
    }
    out.push({ value, label: humanizeEnum(name), name });
  }
  return out.sort((a, b) => a.value - b.value);
}

/**
 * formatMinorUnits is where every money helper meets Intl, and the one place
 * that may not throw.
 *
 * The currency usually comes from the ROW (a sibling `currency` column), so it
 * is data, not a constant — and Intl.NumberFormat throws a RangeError for a
 * code that is not a well-formed ISO 4217 code: an empty string, a typo, a
 * seeded `sample_currency_3`. A throw inside a table cell takes the whole page
 * down to its error boundary, so one bad row used to blank the entire list.
 * An unusable code renders the plain amount beside the raw value instead
 * ("0.18 sample_currency_3"), which keeps the page up and shows exactly what
 * the row holds.
 */
function formatMinorUnits(
  cents: number,
  currency: string | null | undefined,
  wholeUnits: boolean,
): string {
  const amount = cents / 100;
  const digits = wholeUnits ? { maximumFractionDigits: 0 } : {};
  const code = (currency ?? "").trim();
  if (code !== "") {
    try {
      return new Intl.NumberFormat(undefined, {
        style: "currency",
        currency: code,
        ...digits,
      }).format(amount);
    } catch {
      // Not a well-formed currency code — fall through to the plain amount.
    }
  }
  const plain = new Intl.NumberFormat(undefined, {
    minimumFractionDigits: wholeUnits ? 0 : 2,
    maximumFractionDigits: wholeUnits ? 0 : 2,
  }).format(amount);
  return code === "" ? plain : `${plain} ${code}`;
}

/**
 * formatMoneyCents renders an integer minor-unit amount (cents) as
 * currency. Money is stored as integer cents (proto int64 → bigint at
 * runtime) to avoid binary-float rounding; the generator emits this for
 * columns whose proto field name ends in `_cents`/`Cents`. Defaults to USD
 * — pass a currency code (or edit the call site) for other currencies. A
 * code Intl cannot format renders as the plain amount and the raw code,
 * never a thrown error (see formatMinorUnits).
 */
export function formatMoneyCents(
  value: unknown,
  currency: string | null | undefined = "USD",
): string {
  if (value === null || value === undefined || value === "") return "—";
  const cents = typeof value === "bigint" ? Number(value) : Number(value);
  if (Number.isNaN(cents)) return String(value);
  return formatMinorUnits(cents, currency, false);
}

/**
 * intervalSuffix — the compact per-period suffix for a recurring price
 * ("/mo", "/yr"). Returns "" for a one-time / unset interval so a call site
 * can append it unconditionally.
 *
 * Accepts the shapes a billing field realistically arrives in: the proto enum
 * spelling (`BILLING_INTERVAL_MONTH`), the bare token (`MONTH` / `month`), and
 * the common adjective forms (`monthly`, `annual`). Case- and prefix-
 * insensitive. Unknown or one-time cadences ("once", "one_time", "lifetime")
 * render no suffix.
 */
export function intervalSuffix(interval: string | null | undefined): string {
  if (interval === null || interval === undefined || interval === "") return "";
  const key = normalizeStatusKey(String(interval))
    .replace(/^billing_interval_/, "")
    .replace(/^interval_/, "");
  switch (key) {
    case "day":
    case "daily":
      return "/day";
    case "week":
    case "weekly":
      return "/wk";
    case "month":
    case "monthly":
      return "/mo";
    case "quarter":
    case "quarterly":
      return "/qtr";
    case "year":
    case "annual":
    case "annually":
    case "yearly":
      return "/yr";
    default:
      // one-time charges (once / one_time / lifetime) and anything unknown
      // carry no recurrence suffix.
      return "";
  }
}

/**
 * formatMoneyInterval renders a recurring price: the currency-formatted
 * amount plus a compact interval suffix — "$29.00/mo", "$290.00/yr". A
 * one-time or unset interval renders just the amount ("$29.00"), so this is
 * safe for both recurring and one-off prices. Amount rules follow
 * {@link formatMoneyCents}; cadence rules follow {@link intervalSuffix}.
 */
export function formatMoneyInterval(
  value: unknown,
  interval: string | null | undefined,
  currency: string | null | undefined = "USD",
): string {
  const amount = formatMoneyCents(value, currency);
  // formatMoneyCents renders "—" for an unset amount; never tack a recurrence
  // suffix onto the em dash.
  if (amount === "—") return amount;
  return `${amount}${intervalSuffix(interval)}`;
}

/**
 * formatMoneyWhole renders cents as whole currency units — "$272,000", never
 * "$272,000.00". For a headline figure (a dashboard total, a pipeline value)
 * where the cents are two characters of noise on a number nobody reconciles to
 * the penny.
 *
 * Use {@link formatMoneyCents} anywhere the exact amount is the point — an
 * invoice line, a payment, a balance.
 */
export function formatMoneyWhole(
  value: unknown,
  currency: string | null | undefined = "USD",
): string {
  if (value === null || value === undefined || value === "") return "—";
  const cents = typeof value === "bigint" ? Number(value) : Number(value);
  if (Number.isNaN(cents)) return String(value);
  return formatMinorUnits(cents, currency, true);
}

/**
 * WireTimestamp is the runtime shape of a protobuf-es `google.protobuf.
 * Timestamp`: `seconds` is a bigint, so no JS date API accepts it directly.
 */
export interface WireTimestamp {
  seconds: bigint;
  nanos?: number;
}

/**
 * timestampToDate converts a proto Timestamp to a Date, or null when the
 * value is unset or out of range.
 *
 * The range guard matters: `new Date(NaN)` and `new Date(8.64e15 + 1)` are
 * both "Invalid Date", which renders as the literal string "Invalid Date" in
 * a table cell. Returning null routes those through the same "—" path as an
 * unset value.
 */
export function timestampToDate(
  ts: WireTimestamp | undefined | null,
): Date | null {
  if (!ts) return null;
  const ms = Number(ts.seconds) * 1000;
  if (!Number.isFinite(ms)) return null;
  const date = new Date(ms);
  return Number.isNaN(date.getTime()) ? null : date;
}

/** formatDate renders a Timestamp as "Jan 14, 2024". */
export function formatDate(ts: WireTimestamp | undefined | null): string {
  const date = timestampToDate(ts);
  if (!date) return "—";
  return date.toLocaleDateString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
}

/** formatDateTime adds the time of day — for appointments and audit trails. */
export function formatDateTime(ts: WireTimestamp | undefined | null): string {
  const date = timestampToDate(ts);
  if (!date) return "—";
  return date.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

/**
 * formatAge renders elapsed time the way a person says it — "Today",
 * "3 days ago", "5 weeks ago". For a record's age, where the magnitude is the
 * signal and the exact date is not. A future timestamp renders "—".
 */
export function formatAge(
  ts: WireTimestamp | undefined | null,
  now: Date = new Date(),
): string {
  const date = timestampToDate(ts);
  if (!date) return "—";
  const days = Math.floor((now.getTime() - date.getTime()) / 86_400_000);
  if (days < 0) return "—";
  if (days === 0) return "Today";
  if (days === 1) return "Yesterday";
  if (days < 21) return `${days} days ago`;
  const weeks = Math.floor(days / 7);
  if (weeks < 9) return `${weeks} weeks ago`;
  const months = Math.floor(days / 30);
  if (months < 24) return `${months} months ago`;
  return `${Math.floor(days / 365)} years ago`;
}
