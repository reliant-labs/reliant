// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Forge Changes, evaluated in the browser: remote config, gradual rollouts
// and A/B tests as one model. This module is the twin of forge/pkg/changes and
// gives the same unit the same answer for the same snapshot; both read
// pkg/changes/testdata/vectors.json, so they cannot drift apart.
//
//   const snapshot = parseSnapshot(await res.text());     // throws on a bad one
//   const r = evaluate(snapshot, "new-checkout", { targetingKey: userId });
//   const on = r.value === undefined ? false : r.value;    // undefined: your default
//
// ── One object: a change ─────────────────────────────────────────────────
//
// A change has named variants, an optional declared default, and a delivery
// mode: `off` (the default for everyone, previews included: the kill switch),
// `preview` (the default for all but the units on its preview list),
// `everyone` (one variant for all: remote config), `rollout` (one variant for
// a stable percentage of an audience) or `experiment` (a stable random split
// of that percentage). A preview list applies under every mode but `off`.
//
// ── Sticky cohorts from a hash ───────────────────────────────────────────
//
// The unit that stays together — the user (`targetingKey`), an `account` or
// an anonymous `visitor` attribute — is hashed with MurmurHash3 x86_32 over
// its UTF-8 bytes into a bucket 0..9999 (percentages are basis points), under
// separate domain labels for enrollment and variant. So raising a rollout
// only adds units, lowering and raising it returns the same ones, and a
// unit's variant does not depend on the percentage. No network call, nothing
// stored on the device. A context with no id for the unit gets the default
// and is NOT enrolled (`no_unit`), never a control: only `rollout` and
// `experiment` results are enrolled and belong in an experiment's numbers.
//
// ── What this is not ─────────────────────────────────────────────────────
//
// The provider-agnostic core only: it fetches nothing and records no event
// (recording an enrolled evaluation is exposure.ts's job). Pure and
// dependency-free, with no imports at all, so it sits in the barrel like
// money.ts.

/** The only snapshot schema this module reads. */
const SCHEMA_V1 = "forge.changes/v1";

/** 100% in basis points. */
const FULL_BP = 10000;

/**
 * Joins the fields of a hash input. A change key or salt containing it makes
 * the change invalid; the unit id comes last and may contain it.
 */
const SEPARATOR = "|";

const PREVIEW_DOMAIN = `${SCHEMA_V1}|preview|`;
const ENROLL_DOMAIN = `${SCHEMA_V1}|enroll|`;
const VARIANT_DOMAIN = `${SCHEMA_V1}|variant|`;

/** The attribute name an audience uses for the context's targetingKey. */
const TARGETING_KEY_ATTRIBUTE = "targetingKey";

/** Why evaluate returned what it did. Only rollout and experiment enroll. */
export type Reason =
  | "unknown"
  | "default"
  | "invalid"
  | "preview"
  | "everyone"
  | "no_unit"
  | "ineligible"
  | "not_enrolled"
  | "rollout"
  | "experiment";

/**
 * Who a change is evaluated for: `targetingKey` is the user id; attributes
 * carry `account`, `visitor` and the attribute an audience names.
 */
export interface EvalContext {
  targetingKey?: string;
  attributes?: Readonly<Record<string, string>>;
}

/** One evaluation. */
export interface Result {
  /** The variant chosen, or "" when the caller's default applies. */
  variant: string;
  /** The variant's value (frozen), or undefined when the caller's default applies. */
  value: unknown;
  reason: Reason;
  /**
   * The measured run whose delivery decided this: set for rollout and
   * experiment once preview is passed (including ineligible, not enrolled
   * and no unit); "" otherwise.
   */
  run: string;
  /** True only for the `rollout` and `experiment` reasons. */
  enrolled: boolean;
}

/** A parsed, validated, immutable policy snapshot. */
export interface Snapshot {
  /** Grows with every published edit. */
  readonly revision: number;
}

type Mode = "off" | "preview" | "everyone" | "rollout" | "experiment";

const MODES: ReadonlySet<string> = new Set([
  "off",
  "preview",
  "everyone",
  "rollout",
  "experiment",
]);

/** Where a unit's id is read: "" is targetingKey, else an attribute. */
const UNITS: ReadonlyMap<string, string> = new Map([
  ["user", ""],
  ["account", "account"],
  ["visitor", "visitor"],
]);

const UNIT_HASH = /^[0-9a-f]{8}$/;

interface Change {
  variants: Map<string, unknown>;
  defaultVariant: string;
  mode: Mode;
  hasUnit: boolean;
  unitAttr: string;
  run: string;
  salt: string;
  audience: { attribute: string; in: Set<string> } | null;
  percentBP: number;
  split: Array<{ variant: string; weightBP: number }>;
  rolloutVariant: string;
  variant: string;
  /** unit hash → variant; the first listed wins. */
  preview: Map<string, string>;
}

/** A change that failed validation: evaluates as `invalid`. */
const INVALID = null;

class ParsedSnapshot implements Snapshot {
  constructor(
    readonly revision: number,
    readonly changes: ReadonlyMap<string, Change | typeof INVALID>,
  ) {
    Object.freeze(this);
  }
}

// ── MurmurHash3 x86_32 ───────────────────────────────────────────────────

let encoder: TextEncoder | undefined;

/**
 * MurmurHash3 x86_32, seed 0, over the UTF-8 bytes of s; unsigned.
 *
 * @internal Exported for the golden-vector test only: the barrel does not
 * re-export it and the package's exports map does not reach this module.
 */
export function murmur3(s: string): number {
  encoder ??= new TextEncoder();
  const data = encoder.encode(s);
  const c1 = 0xcc9e2d51;
  const c2 = 0x1b873593;
  const n = data.length;
  let h = 0;
  let i = 0;
  for (; i + 4 <= n; i += 4) {
    let k =
      data[i]! |
      (data[i + 1]! << 8) |
      (data[i + 2]! << 16) |
      (data[i + 3]! << 24);
    k = Math.imul(k, c1);
    k = (k << 15) | (k >>> 17);
    k = Math.imul(k, c2);
    h ^= k;
    h = (h << 13) | (h >>> 19);
    h = (Math.imul(h, 5) + 0xe6546b64) | 0;
  }
  let k = 0;
  switch (n & 3) {
    case 3:
      k ^= data[i + 2]! << 16;
    // falls through
    case 2:
      k ^= data[i + 1]! << 8;
    // falls through
    case 1:
      k ^= data[i]!;
      k = Math.imul(k, c1);
      k = (k << 15) | (k >>> 17);
      k = Math.imul(k, c2);
      h ^= k;
  }
  h ^= n;
  h ^= h >>> 16;
  h = Math.imul(h, 0x85ebca6b);
  h ^= h >>> 13;
  h = Math.imul(h, 0xc2b2ae35);
  h ^= h >>> 16;
  return h >>> 0;
}

function bucket(s: string): number {
  return murmur3(s) % FULL_BP;
}

/**
 * How a preview list names a unit of the change key: 8 lowercase hex digits
 * of its hash, so a snapshot served to a browser carries no raw user ids.
 */
export function previewHash(key: string, unitId: string): string {
  return murmur3(`${PREVIEW_DOMAIN}${key}|${unitId}`)
    .toString(16)
    .padStart(8, "0");
}

// ── Parsing ──────────────────────────────────────────────────────────────

type Obj = Record<string, unknown>;

function isObject(v: unknown): v is Obj {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** Present and not null: an explicit null reads as absent, as in Go. */
function has(o: Obj, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(o, key) && o[key] !== null;
}

class Bad extends Error {}

/** An optional string field: undefined when absent. */
function str(o: Obj, key: string): string | undefined {
  if (!has(o, key)) {
    return undefined;
  }
  const v = o[key];
  if (typeof v !== "string") {
    throw new Bad(`${key} is not a string`);
  }
  return v;
}

/** An optional integer field in lo..hi: undefined when absent. */
function integer(
  o: Obj,
  key: string,
  lo: number,
  hi: number,
): number | undefined {
  if (!has(o, key)) {
    return undefined;
  }
  const v = o[key];
  if (typeof v !== "number" || !Number.isInteger(v) || v < lo || v > hi) {
    throw new Bad(`${key} is not an integer in ${lo}..${hi}`);
  }
  return v === 0 ? 0 : v; // -0 is 0
}

function object(v: unknown, what: string): Obj {
  if (!isObject(v)) {
    throw new Bad(`${what} is not an object`);
  }
  return v;
}

function array(v: unknown, what: string): unknown[] {
  if (!Array.isArray(v)) {
    throw new Bad(`${what} is not an array`);
  }
  return v;
}

function deepFreeze<T>(v: T): T {
  if (typeof v === "object" && v !== null) {
    for (const child of Object.values(v)) {
      deepFreeze(child);
    }
    Object.freeze(v);
  }
  return v;
}

function parseChange(raw: unknown): Change {
  const obj = object(raw, "change");
  if (!has(obj, "variants")) {
    throw new Bad("variants is missing");
  }
  const variants = new Map<string, unknown>();
  for (const [name, value] of Object.entries(
    object(obj["variants"], "variants"),
  )) {
    if (name === "") {
      throw new Bad("a variant has an empty name");
    }
    if (value === null) {
      throw new Bad(`variant ${name} has a null value`);
    }
    variants.set(name, deepFreeze(value));
  }
  const declared = (o: Obj, key: string): string => {
    const name = str(o, key);
    if (name !== undefined && !variants.has(name)) {
      throw new Bad(`${key} names undeclared variant ${name}`);
    }
    return name ?? "";
  };
  const defaultVariant = declared(obj, "default_variant");

  if (!has(obj, "delivery")) {
    throw new Bad("delivery is missing");
  }
  const d = object(obj["delivery"], "delivery");
  const mode = str(d, "mode") ?? "";
  if (!MODES.has(mode)) {
    throw new Bad(`unknown mode ${mode}`);
  }
  const unitName = str(d, "unit");
  let unitAttr = "";
  if (unitName !== undefined) {
    const attr = UNITS.get(unitName);
    if (attr === undefined) {
      throw new Bad(`unknown unit ${unitName}`);
    }
    unitAttr = attr;
  }
  const run = str(d, "run") ?? "";
  const salt = str(d, "salt") ?? "";
  if (salt.includes(SEPARATOR)) {
    throw new Bad("salt contains the hash separator");
  }
  const percentBP = integer(d, "percent_bp", 0, FULL_BP);
  const variant = declared(d, "variant");
  const rolloutVariant = declared(d, "rollout_variant");

  let audience: Change["audience"] = null;
  if (has(d, "audience")) {
    const a = object(d["audience"], "audience");
    const attribute = str(a, "attribute");
    if (!attribute) {
      throw new Bad("audience needs an attribute");
    }
    if (!has(a, "in")) {
      throw new Bad('audience needs "in"');
    }
    const values = new Set<string>();
    for (const v of array(a["in"], "audience in")) {
      if (typeof v !== "string") {
        throw new Bad('audience "in" holds a value that is not a string');
      }
      values.add(v);
    }
    audience = { attribute, in: values };
  }

  const split: Change["split"] = [];
  if (has(d, "split")) {
    let sum = 0;
    for (const raw of array(d["split"], "split")) {
      const e = object(raw, "split entry");
      const name = str(e, "variant");
      if (name === undefined || !variants.has(name)) {
        throw new Bad(`split names undeclared variant ${name}`);
      }
      const weightBP = integer(e, "weight_bp", 0, FULL_BP);
      if (weightBP === undefined) {
        throw new Bad("split weight_bp is missing");
      }
      sum += weightBP;
      split.push({ variant: name, weightBP });
    }
    if (split.length > 0 && sum !== FULL_BP) {
      throw new Bad(`split weights sum to ${sum}, not ${FULL_BP}`);
    }
  }

  const preview = new Map<string, string>();
  if (has(d, "preview")) {
    for (const raw of array(d["preview"], "preview")) {
      const e = object(raw, "preview entry");
      const h = str(e, "unit_hash");
      if (h === undefined || !UNIT_HASH.test(h)) {
        throw new Bad(`preview unit_hash ${h} is not 8 lowercase hex digits`);
      }
      const name = str(e, "variant");
      if (name === undefined || !variants.has(name)) {
        throw new Bad(`preview names undeclared variant ${name}`);
      }
      if (!preview.has(h)) {
        preview.set(h, name);
      }
    }
  }

  const hasUnit = unitName !== undefined;
  if (preview.size > 0 && !hasUnit) {
    throw new Bad("preview needs a unit");
  }
  switch (mode) {
    case "everyone":
      if (variant === "") {
        throw new Bad("everyone needs a variant");
      }
      break;
    case "rollout":
    case "experiment":
      if (!hasUnit || salt === "" || percentBP === undefined) {
        throw new Bad(
          "rollout and experiment need a unit, a salt and percent_bp",
        );
      }
      if (mode === "rollout" && rolloutVariant === "") {
        throw new Bad("rollout needs a rollout_variant");
      }
      if (mode === "experiment" && (run === "" || split.length === 0)) {
        throw new Bad("experiment needs a run and a split");
      }
  }
  return {
    variants,
    defaultVariant,
    mode: mode as Mode,
    hasUnit,
    unitAttr,
    run,
    salt,
    audience,
    percentBP: percentBP ?? 0,
    split,
    rolloutVariant,
    variant,
    preview,
  };
}

/**
 * Parse and validate a `forge.changes/v1` snapshot from its JSON text. Throws
 * only when the snapshot as a whole cannot be read — malformed JSON, an
 * unknown schema, a missing or non-integer revision, or `changes` that is not
 * an object — and the caller then keeps its last good snapshot. A malformed
 * change does not throw: it evaluates as `invalid` and the others are
 * unaffected. Unknown fields are ignored.
 */
export function parseSnapshot(json: string): Snapshot {
  let root: unknown;
  try {
    root = JSON.parse(json);
  } catch (err) {
    throw new Error(`changes: snapshot: ${(err as Error).message}`);
  }
  if (!isObject(root)) {
    throw new Error("changes: snapshot: not a JSON object");
  }
  const schema = root["schema"];
  if (!has(root, "schema") || typeof schema !== "string") {
    throw new Error("changes: snapshot: schema is missing or not a string");
  }
  if (schema !== SCHEMA_V1) {
    throw new Error(
      `changes: snapshot: unknown schema "${schema}" (this reader knows "${SCHEMA_V1}")`,
    );
  }
  let revision: number | undefined;
  try {
    revision = integer(root, "revision", 0, Number.MAX_SAFE_INTEGER);
  } catch {
    revision = undefined;
  }
  if (revision === undefined) {
    throw new Error(
      "changes: snapshot: revision is missing or not an integer in 0..2^53-1",
    );
  }
  const changes = new Map<string, Change | typeof INVALID>();
  if (has(root, "changes")) {
    const list = root["changes"];
    if (!isObject(list)) {
      throw new Error("changes: snapshot: changes: not a JSON object");
    }
    for (const [key, raw] of Object.entries(list)) {
      let c: Change | typeof INVALID = INVALID;
      if (!key.includes(SEPARATOR)) {
        try {
          c = parseChange(raw);
        } catch (err) {
          if (!(err instanceof Bad)) {
            throw err;
          }
        }
      }
      changes.set(key, c);
    }
  }
  return new ParsedSnapshot(revision, changes);
}

// ── Evaluation ───────────────────────────────────────────────────────────

function attribute(ctx: EvalContext, name: string): string | undefined {
  const attrs = ctx.attributes;
  if (
    attrs === undefined ||
    !Object.prototype.hasOwnProperty.call(attrs, name)
  ) {
    return undefined;
  }
  const v = attrs[name];
  return typeof v === "string" ? v : undefined;
}

function unitId(c: Change, ctx: EvalContext): string {
  if (!c.hasUnit) {
    return "";
  }
  if (c.unitAttr === "") {
    return typeof ctx.targetingKey === "string" ? ctx.targetingKey : "";
  }
  return attribute(ctx, c.unitAttr) ?? "";
}

function audienceValue(ctx: EvalContext, name: string): string | undefined {
  if (name === TARGETING_KEY_ATTRIBUTE) {
    const k = ctx.targetingKey;
    return typeof k === "string" && k !== "" ? k : undefined;
  }
  return attribute(ctx, name);
}

function chose(
  c: Change,
  variant: string,
  reason: Reason,
  run: string,
  enrolled: boolean,
): Result {
  return { variant, value: c.variants.get(variant), reason, run, enrolled };
}

function fallback(c: Change, reason: Reason, run: string): Result {
  if (c.defaultVariant === "") {
    return { variant: "", value: undefined, reason, run, enrolled: false };
  }
  return chose(c, c.defaultVariant, reason, run, false);
}

function callerDefault(reason: Reason): Result {
  return { variant: "", value: undefined, reason, run: "", enrolled: false };
}

/**
 * Decide the change `key` for `ctx`. Never throws: a missing snapshot or key
 * is `unknown` and a malformed change `invalid`, both with the caller's
 * default (value undefined). The same snapshot, key and context always give
 * the same result.
 */
export function evaluate(
  snapshot: Snapshot | null | undefined,
  key: string,
  ctx: EvalContext = {},
): Result {
  if (!(snapshot instanceof ParsedSnapshot)) {
    return callerDefault("unknown");
  }
  const c = snapshot.changes.get(key);
  if (c === undefined) {
    return callerDefault("unknown");
  }
  if (c === INVALID) {
    return callerDefault("invalid");
  }
  // Off is the kill switch: it silences previews too.
  if (c.mode === "off") {
    return fallback(c, "default", "");
  }
  const unit = unitId(c, ctx);
  if (unit !== "" && c.preview.size > 0) {
    const v = c.preview.get(previewHash(key, unit));
    if (v !== undefined) {
      return chose(c, v, "preview", "", false);
    }
  }
  if (c.mode === "preview") {
    return fallback(c, "default", "");
  }
  if (c.mode === "everyone") {
    return chose(c, c.variant, "everyone", "", false);
  }
  if (unit === "") {
    return fallback(c, "no_unit", c.run);
  }
  if (c.audience !== null) {
    const v = audienceValue(ctx, c.audience.attribute);
    if (v === undefined || !c.audience.in.has(v)) {
      return fallback(c, "ineligible", c.run);
    }
  }
  if (bucket(`${ENROLL_DOMAIN}${key}|${c.salt}|${unit}`) >= c.percentBP) {
    return fallback(c, "not_enrolled", c.run);
  }
  if (c.mode === "rollout") {
    return chose(c, c.rolloutVariant, "rollout", c.run, true);
  }
  const v = bucket(`${VARIANT_DOMAIN}${key}|${c.salt}|${unit}`);
  let cumulative = 0;
  for (const e of c.split) {
    cumulative += e.weightBP;
    if (v < cumulative) {
      return chose(c, e.variant, "experiment", c.run, true);
    }
  }
  // Unreachable: parseSnapshot holds a split's weights to 10000.
  return callerDefault("invalid");
}

/**
 * The unit the change `key` keeps together — "user", "account" or "visitor" —
 * or "" when the snapshot has no valid change of that key or it names none.
 *
 * @internal For changes-store.ts, which names the unit on the event it
 * records; the barrel does not re-export it.
 */
export function changeUnit(
  snapshot: Snapshot | null | undefined,
  key: string,
): string {
  if (!(snapshot instanceof ParsedSnapshot)) {
    return "";
  }
  const c = snapshot.changes.get(key);
  if (c === undefined || c === INVALID || !c.hasUnit) {
    return "";
  }
  for (const [name, attr] of UNITS) {
    if (attr === c.unitAttr) {
      return name;
    }
  }
  return "";
}
