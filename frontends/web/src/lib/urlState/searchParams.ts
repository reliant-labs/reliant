// Copyright (c) 2026 Reliant Labs

/**
 * TYPED SEARCH PARAMS FOR VIEW STATE (owner, 2026-11-24: the URL is the only
 * source of view state).
 *
 * A `SearchParam<T>` is one query parameter's grammar: how a raw value (what
 * the router parsed out of `?key=…`) reads as a T, and how a T is written. A
 * value that does not read is absent, never an error: the URL is
 * user-editable and shared, and a bad param must not break the page.
 *
 *   readParam(search, param)        the value, or the param's default
 *   writeParam(search, param, v)    a new search object with v (or without the
 *                                   param: undefined, or a value equal to the
 *                                   default, which never needs to be written)
 *   retainSearchParams(params)      a search middleware: a navigation that does
 *                                   not name a retained param keeps its current
 *                                   value; one that names it (even as
 *                                   undefined) decides
 *
 * Replace vs push is the caller's: a person's choice is a push (Back undoes
 * it), filling an empty URL from a default or mirroring how the page already
 * looks is a replace (it is not a step Back should retrace).
 *
 * Router-neutral: it works on plain search objects and imports nothing from
 * the app, so it can move to @reliantlabs/forge-web-runtime unchanged. The
 * middleware's shape is TanStack Router's (`{ search, next }`), which is also
 * the plainest one.
 */

export type SearchRecord = Record<string, unknown>;

export interface SearchParam<T> {
  readonly key: string;
  /** The raw value (a string, or a number/boolean the router's JSON parse produced) as a T; undefined when it does not read. */
  parse: (raw: unknown) => T | undefined;
  /** T as the raw value written; undefined writes nothing. */
  serialize: (value: T) => string | undefined;
  /** What an absent param means; written values equal to it are dropped. */
  readonly defaultValue?: T;
  /** Value equality, for dropping the default (=== by default). */
  equals?: (a: T, b: T) => boolean;
}

export function defineSearchParam<T>(param: SearchParam<T>): SearchParam<T> {
  return param;
}

/** The param's value in `search`, else its default. */
export function readParam<T>(search: SearchRecord | null | undefined, param: SearchParam<T>): T | undefined {
  const raw = search?.[param.key];
  if (raw === undefined || raw === null || raw === "") return param.defaultValue;
  let value: T | undefined;
  try {
    value = param.parse(raw);
  } catch {
    value = undefined;
  }
  return value === undefined ? param.defaultValue : value;
}

/** Whether `search` names the param at all (with a readable value). */
export function hasParam<T>(search: SearchRecord | null | undefined, param: SearchParam<T>): boolean {
  const raw = search?.[param.key];
  if (raw === undefined || raw === null || raw === "") return false;
  try {
    return param.parse(raw) !== undefined;
  } catch {
    return false;
  }
}

function isDefault<T>(param: SearchParam<T>, value: T): boolean {
  if (param.defaultValue === undefined) return false;
  return param.equals ? param.equals(value, param.defaultValue) : value === param.defaultValue;
}

/**
 * `search` with the param set to `value`. Undefined, or the default, leaves
 * the key PRESENT with an undefined value: a navigation's search object that
 * names a retained param as undefined clears it (retainSearchParams), and the
 * router's stringifier drops undefined values from the URL.
 */
export function writeParam<T>(search: SearchRecord, param: SearchParam<T>, value: T | undefined): SearchRecord {
  const raw = value === undefined || isDefault(param, value) ? undefined : param.serialize(value);
  return { ...search, [param.key]: raw === "" ? undefined : raw };
}

/** A search updater (`(prev) => next`) that writes several params at once. */
export function patchParams(patch: ReadonlyArray<readonly [SearchParam<never>, unknown]>): (prev: SearchRecord) => SearchRecord {
  return (prev) => {
    let next: SearchRecord = { ...prev };
    for (const [param, value] of patch) next = writeParam(next, param as unknown as SearchParam<unknown>, value);
    return next;
  };
}

export interface SearchMiddlewareArgs {
  search: SearchRecord;
  next: (search: SearchRecord) => SearchRecord;
}

/**
 * Keep the params across navigations that do not name them. `when` narrows it
 * to the values worth carrying (a choice of several projects is; a single
 * project the next page names itself is not).
 */
export function retainSearchParams(
  params: ReadonlyArray<SearchParam<never> | { param: SearchParam<never>; when: (value: unknown, search: SearchRecord) => boolean }>,
): (args: SearchMiddlewareArgs) => SearchRecord {
  const rules = params.map((entry) => ("param" in entry ? entry : { param: entry, when: () => true }));
  return ({ search, next }) => {
    const result = next(search);
    let out: SearchRecord | null = null;
    for (const { param, when } of rules) {
      if (param.key in result) continue;
      const raw = search[param.key];
      if (raw === undefined || raw === null || raw === "") continue;
      if (!when(readParam(search, param as unknown as SearchParam<unknown>), search)) continue;
      out ??= { ...result };
      out[param.key] = raw;
    }
    return out ?? result;
  };
}

// ── Common grammars ─────────────────────────────────────────────────────────

/** A string param; `allowed` narrows it to an enum. */
export function stringParam<T extends string = string>(key: string, allowed?: readonly T[], defaultValue?: T): SearchParam<T> {
  return defineSearchParam<T>({
    key,
    defaultValue,
    parse: (raw) => {
      const value = typeof raw === "number" || typeof raw === "boolean" ? String(raw) : raw;
      if (typeof value !== "string" || value === "") return undefined;
      if (allowed && !(allowed as readonly string[]).includes(value)) return undefined;
      return value as T;
    },
    serialize: (value) => value,
  });
}

/** A comma-separated list of ids: deduplicated, in order, empty entries dropped. */
export function idListParam(key: string, { max = 200 }: { max?: number } = {}): SearchParam<string[]> {
  return defineSearchParam<string[]>({
    key,
    parse: (raw) => {
      const value = typeof raw === "number" ? String(raw) : raw;
      const parts = Array.isArray(value) ? value.map(String) : typeof value === "string" ? value.split(",") : null;
      if (!parts) return undefined;
      const ids = [...new Set(parts.map((part) => part.trim()).filter(Boolean))].slice(0, max);
      return ids.length > 0 ? ids : undefined;
    },
    serialize: (ids) => (ids.length > 0 ? ids.join(",") : undefined),
    equals: (a, b) => a.length === b.length && a.every((id, i) => id === b[i]),
  });
}
