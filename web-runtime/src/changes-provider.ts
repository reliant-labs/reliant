// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The OpenFeature web provider for Forge Changes: app code evaluates through
// the standard OpenFeature web SDK, with the default at the call site, and a
// team can later swap providers without touching a call site. The twin of
// forge/pkg/changes/openfeature.
//
//   OpenFeature.setProvider(forgeChangesProvider(await loadChanges()));
//   await OpenFeature.setContext({ targetingKey: userId, plan: "paid" });
//   const on = OpenFeature.getClient().getBooleanValue("new-checkout", false);
//
// Typed structurally: this package does not depend on OpenFeature. The SDK
// types an error code as its own string enum, which no structural type can
// name, so the provider takes it as a type parameter that a direct call to
// setProvider infers; a provider held in a variable first names it:
// `forgeChangesProvider<ErrorCode>(store)`. The store
// records each evaluation itself (changes-store.ts), and forgeExposureHook
// skips this provider, so registering both records it once.
//
// The context's targetingKey is the user id; its string attributes carry
// `account`, `visitor` and the attribute an audience names (others are
// dropped). How a forge reason reads in OpenFeature:
//
//   experiment, rollout                         SPLIT
//   preview                                     TARGETING_MATCH
//   everyone                                    STATIC
//   default, not_enrolled, ineligible, no_unit  DEFAULT
//   unknown                                     ERROR, FLAG_NOT_FOUND, the call site's default
//   invalid                                     ERROR, PARSE_ERROR, the call site's default
//
// A value of another type than the call asked for is ERROR, TYPE_MISMATCH,
// with the call site's default. flagMetadata carries the forge `reason`, the
// snapshot `revision` and, for a rollout or experiment, the `run`.
import type { EvalContext, Reason, Result } from "./changes.js";
import type { ChangesStore } from "./changes-store.js";
import { FORGE_CHANGES_PROVIDER } from "./exposure.js";

/** The slice of an OpenFeature evaluation context the provider reads. */
export type ChangesProviderContext = Readonly<Record<string, unknown>>;

/** An OpenFeature resolution, as the web SDK's provider interface returns it. */
export interface ChangesResolution<T, E extends string = string> {
  value: T;
  variant?: string;
  reason: string;
  errorCode?: E;
  errorMessage?: string;
  flagMetadata: Record<string, string | number>;
}

/** The OpenFeature web provider forgeChangesProvider returns. */
export interface ChangesProvider<E extends string = string> {
  readonly metadata: { readonly name: string };
  readonly runsOn: "client";
  resolveBooleanEvaluation(
    flagKey: string,
    defaultValue: boolean,
    context?: ChangesProviderContext,
  ): ChangesResolution<boolean, E>;
  resolveStringEvaluation(
    flagKey: string,
    defaultValue: string,
    context?: ChangesProviderContext,
  ): ChangesResolution<string, E>;
  resolveNumberEvaluation(
    flagKey: string,
    defaultValue: number,
    context?: ChangesProviderContext,
  ): ChangesResolution<number, E>;
  resolveObjectEvaluation<T>(
    flagKey: string,
    defaultValue: T,
    context?: ChangesProviderContext,
  ): ChangesResolution<T, E>;
}

const OPENFEATURE_REASONS: Partial<Record<Reason, string>> = {
  experiment: "SPLIT",
  rollout: "SPLIT",
  preview: "TARGETING_MATCH",
  everyone: "STATIC",
};

function evalContext(context?: ChangesProviderContext): EvalContext {
  const attributes: Record<string, string> = {};
  let targetingKey: string | undefined;
  for (const [k, v] of Object.entries(context ?? {})) {
    if (typeof v !== "string") {
      continue;
    }
    if (k === "targetingKey") {
      targetingKey = v;
    } else {
      attributes[k] = v;
    }
  }
  return { targetingKey, attributes };
}

function resolve<T, E extends string>(
  store: ChangesStore,
  flagKey: string,
  defaultValue: T,
  context: ChangesProviderContext | undefined,
  fits: (value: unknown) => boolean,
): ChangesResolution<T, E> {
  let r: Result;
  try {
    r = store.evaluate(flagKey, evalContext(context));
  } catch {
    r = {
      variant: "",
      value: undefined,
      reason: "unknown",
      run: "",
      enrolled: false,
    };
  }
  const flagMetadata: Record<string, string | number> = {
    reason: r.reason,
    revision: store.revision,
  };
  if (r.run !== "") {
    flagMetadata.run = r.run;
  }
  if (r.reason === "unknown") {
    return {
      value: defaultValue,
      reason: "ERROR",
      errorCode: "FLAG_NOT_FOUND" as E,
      errorMessage: `forge changes: no change "${flagKey}" in the snapshot`,
      flagMetadata,
    };
  }
  if (r.reason === "invalid") {
    return {
      value: defaultValue,
      reason: "ERROR",
      errorCode: "PARSE_ERROR" as E,
      errorMessage: `forge changes: change "${flagKey}" failed validation`,
      flagMetadata,
    };
  }
  const reason = OPENFEATURE_REASONS[r.reason] ?? "DEFAULT";
  if (r.value === undefined) {
    // The change declares no default variant: the call site's applies.
    return { value: defaultValue, reason, flagMetadata };
  }
  if (!fits(r.value)) {
    return {
      value: defaultValue,
      reason: "ERROR",
      errorCode: "TYPE_MISMATCH" as E,
      errorMessage: `forge changes: variant "${r.variant}" of "${flagKey}" is not the type this call asked for`,
      flagMetadata,
    };
  }
  return { value: r.value as T, variant: r.variant, reason, flagMetadata };
}

/**
 * An OpenFeature web provider over a loaded Forge Changes store:
 * `OpenFeature.setProvider(forgeChangesProvider(await loadChanges()))`.
 * Synchronous, as the web SDK expects; never throws.
 */
export function forgeChangesProvider<E extends string = string>(
  store: ChangesStore,
): ChangesProvider<E> {
  return {
    metadata: { name: FORGE_CHANGES_PROVIDER },
    runsOn: "client",
    resolveBooleanEvaluation: (flagKey, defaultValue, context) =>
      resolve(
        store,
        flagKey,
        defaultValue,
        context,
        (v) => typeof v === "boolean",
      ),
    resolveStringEvaluation: (flagKey, defaultValue, context) =>
      resolve(
        store,
        flagKey,
        defaultValue,
        context,
        (v) => typeof v === "string",
      ),
    resolveNumberEvaluation: (flagKey, defaultValue, context) =>
      resolve(
        store,
        flagKey,
        defaultValue,
        context,
        (v) => typeof v === "number" && Number.isFinite(v),
      ),
    resolveObjectEvaluation: (flagKey, defaultValue, context) =>
      resolve(
        store,
        flagKey,
        defaultValue,
        context,
        (v) => typeof v === "object" && v !== null,
      ),
  };
}
