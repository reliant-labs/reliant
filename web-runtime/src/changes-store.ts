// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Forge Changes for an app: load the environment's snapshot once per page,
// evaluate changes from it, and record each useful evaluation as a product
// event. The twin of forge/pkg/changes's Store.
//
//   const changes = await loadChanges();                 // never rejects
//   const r = changes.evaluate("new-checkout", { targetingKey: userId });
//   if (changeBool(r, false)) { … }                      // false: your default
//
// ── Where the snapshot comes from ────────────────────────────────────────
//
// The platform serves the environment's snapshot at `/_forge/changes.json`
// on the site's own origin (no-cache, with an ETag), rewriting it whenever a
// change's delivery moves. loadChanges fetches it once; a page applies a new
// revision on its next load, so a treatment never switches mid-interaction.
// A missing, unreachable or malformed snapshot is an empty store: every
// change evaluates as `unknown` and every call site gets its own default.
//
// An app not on Reliant hosting serves that file itself, from a server that
// fetches it from the control plane (`./changes-server`); server rendering
// can hand loadChanges the same fetch as its `source`.
//
// ── What gets recorded ───────────────────────────────────────────────────
//
// evaluate() records `flag_evaluated` through exposure.ts with
// provider=forge plus the change's `run`, `unit`, `reason` and the snapshot
// `revision` (and, for an account unit, the `account` it assigned), once per
// person, change, variant and run per day. Only
// results a readout or dashboard can use are recorded: enrolled ones
// (`rollout`, `experiment`), which are a unit's assignment and are recorded
// before the app renders the treatment, plus `preview` and `everyone`.
// Defaults, units outside the audience or the percentage, and changes that
// did not evaluate are not.
//
// Dependency-free apart from changes.ts and exposure.ts: a fetch only when
// loadChanges is called, nothing on import.
import {
  changeUnit,
  changeUnitId,
  evaluate,
  parseSnapshot,
  type EvalContext,
  type Reason,
  type Result,
  type Snapshot,
} from "./changes.js";
import { FORGE_CHANGES_PROVIDER, trackExposure } from "./exposure.js";

/** Where the platform serves the environment's snapshot. */
export const CHANGES_SNAPSHOT_URL = "/_forge/changes.json";

/** loadChanges's options. */
export interface LoadChangesOptions {
  /** The snapshot's URL. Default `/_forge/changes.json`. */
  url?: string;
  /** The fetch to use (tests, server rendering). Default the global one. */
  fetch?: typeof fetch;
  /**
   * Where the snapshot document comes from, instead of fetching `url`: a
   * function resolving to its JSON text, or to null or "" for none. For
   * server rendering an app not on Reliant hosting, pass one that calls
   * `fetchChangesSnapshot` from `@reliantlabs/forge-web-runtime/changes-server`
   * — on the server only, since it holds the control plane token. A
   * rejection is an empty store, like an unreachable url.
   */
  source?: () => Promise<string | null | undefined>;
}

/** A loaded snapshot, ready to evaluate. */
export interface ChangesStore {
  /**
   * Decide the change `key` for `ctx` and record the evaluation (see the
   * module header). Never throws.
   */
  evaluate(key: string, ctx?: EvalContext): Result;
  /** The snapshot's revision; 0 for an empty store. */
  readonly revision: number;
}

/** The reasons recorded as flag_evaluated. */
const RECORDED: ReadonlySet<Reason> = new Set<Reason>([
  "rollout",
  "experiment",
  "preview",
  "everyone",
]);

function store(snapshot: Snapshot | null): ChangesStore {
  const revision = snapshot?.revision ?? 0;
  return {
    revision,
    evaluate(key: string, ctx: EvalContext = {}): Result {
      const r = evaluate(snapshot, key, ctx);
      if (RECORDED.has(r.reason) && r.variant !== "") {
        const unit = changeUnit(snapshot, key);
        trackExposure(key, r.variant, {
          provider: FORGE_CHANGES_PROVIDER,
          run: r.run,
          unit,
          reason: r.reason,
          revision,
          // A user or visitor id is a column of every event; an account id
          // is not, so the assignment names the one the change hashed.
          account:
            unit === "account" ? changeUnitId(snapshot, key, ctx) : undefined,
        });
      }
      return r;
    },
  };
}

/**
 * Fetch the environment's snapshot and return a store over it. Never
 * rejects: a missing (404), unreachable or malformed snapshot is an empty
 * store, where every call site gets its own default; the last two warn on
 * the console.
 */
export async function loadChanges(
  options?: LoadChangesOptions,
): Promise<ChangesStore> {
  const source = options?.source;
  const url = source
    ? "the changes source"
    : (options?.url ?? CHANGES_SNAPSHOT_URL);
  try {
    if (source) {
      const text = await source();
      return store(text ? parseSnapshot(text) : null);
    }
    const fetchImpl = options?.fetch ?? globalThis.fetch;
    if (typeof fetchImpl !== "function") {
      return store(null);
    }
    const res = await fetchImpl(url, {
      cache: "no-cache",
      credentials: "same-origin",
    });
    if (res.status === 404) {
      return store(null);
    }
    if (!res.ok) {
      throw new Error(`HTTP ${res.status}`);
    }
    return store(parseSnapshot(await res.text()));
  } catch (err) {
    console.warn(
      `[forge] changes: cannot load ${url}; every change uses its default:`,
      err instanceof Error ? err.message : err,
    );
    return store(null);
  }
}

/** r's value when it is a boolean, else `def`. */
export function changeBool(r: Result, def: boolean): boolean {
  return typeof r.value === "boolean" ? r.value : def;
}

/** r's value when it is a string, else `def`. */
export function changeString(r: Result, def: string): string {
  return typeof r.value === "string" ? r.value : def;
}

/** r's value when it is a finite number, else `def`. */
export function changeNumber(r: Result, def: number): number {
  return typeof r.value === "number" && Number.isFinite(r.value)
    ? r.value
    : def;
}

/**
 * r's value (frozen) for a JSON-valued change, else `def` when the caller's
 * default applies. Its shape is the change's declaration: this checks only
 * that a value is present.
 */
export function changeJson<T>(r: Result, def: T): T {
  return r.value === undefined ? def : (r.value as T);
}
