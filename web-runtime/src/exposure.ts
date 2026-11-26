// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Feature-flag exposures as product events: one `flag_evaluated {flag,
// variant, provider}` when a person is shown a variant, so a dashboard can
// put "who saw which variant" beside what they did next (descriptive, not a
// significance test).
//
// ── Two ways in ──────────────────────────────────────────────────────────
//
//   trackExposure(flag, variant)   from any flag code, by hand.
//   forgeExposureHook              an OpenFeature hook: register it once
//                                  (OpenFeature.addHooks(forgeExposureHook))
//                                  and every evaluation through any
//                                  OpenFeature provider is recorded, with no
//                                  other code. Typed structurally: this
//                                  package does not depend on OpenFeature.
//
// ── Once per person, flag and variant per day ────────────────────────────
//
// Flags are evaluated on every render. An exposure is recorded once per
// (person, flag, variant) per UTC day per page load: the person is the
// signed-in user when the tier lets their id be carried, else this browser.
// The dedupe set is kept in memory, never on the device (track.ts, "Analytics
// tiers"), so a reload may record the same exposure again; the dashboards
// count distinct persons, so it counts once.
//
// A provider that knows more says so in the options: the measured `run` (part
// of the dedupe key, since a new run is a new assignment), the `unit` that
// stays together, the `reason`, the config `revision`, and for an account
// unit the `account` assigned (also part of the dedupe key, and carried only
// in the tiers that carry the user and org ids). Forge Changes
// (changes-store.ts) passes all of them.
//
// An evaluation that failed (an OpenFeature `errorCode`) is no exposure. A
// variant is a name: an object-valued flag with no variant name is recorded
// as `object`, never its contents.
//
// Dependency-free apart from track.ts, like it: an app's flag provider calls
// the hook whether or not the SDK is installed.
import {
  recordProductEvent,
  stampableTelemetryId,
  stampedTelemetryUser,
} from "./track.js";

/** The product event an exposure is recorded as. */
export const FLAG_EVALUATED_EVENT = "flag_evaluated";

/**
 * The provider name Forge Changes records under and its OpenFeature provider
 * reports; forgeExposureHook leaves that provider's evaluations alone.
 */
export const FORGE_CHANGES_PROVIDER = "forge";

/** Longest flag key, variant or provider name recorded. */
export const MAX_FLAG_VALUE_CHARS = 128;

/** trackExposure's options. Each one set becomes a property of the event. */
export interface ExposureOptions {
  /** The flag provider ("launchdarkly", "flagd", "statsig", your own). */
  provider?: string;
  /**
   * The measured run (experiment or rollout) the variant was assigned in.
   * Part of the dedupe key: a new run of a flag is a new assignment.
   */
  run?: string;
  /** What stays together: "user", "account" or "visitor". */
  unit?: string;
  /** Why the provider chose the variant ("experiment", "preview", …). */
  reason?: string;
  /** The revision of the flag configuration that decided it. */
  revision?: number;
  /**
   * The account the variant was assigned to, when the unit is "account": the
   * id the provider hashed. An experiment readout counts an account by it on
   * an event that names no org. Part of the dedupe key; carried only in the
   * `identified` and `full` tiers, like the user and org ids.
   */
  account?: string;
}

let seen = new Set<string>();

function clean(value: string): string {
  // eslint-disable-next-line no-control-regex
  return value
    .replace(/[\u0000-\u001f\u007f]/g, "")
    .trim()
    .slice(0, MAX_FLAG_VALUE_CHARS);
}

/** An optional string option, cleaned; "" when unset or not a string. */
function optional(value: unknown): string {
  return typeof value === "string" ? clean(value) : "";
}

function variantName(variant: unknown): string {
  if (typeof variant === "string") {
    return clean(variant);
  }
  if (typeof variant === "boolean" || typeof variant === "number") {
    return String(variant);
  }
  return variant === null || variant === undefined ? "" : "object";
}

/**
 * Record that this person was shown `variant` of `flag`. Once per (person,
 * flag, variant, run) per UTC day per page load; never throws; inherits track()'s
 * gates (nothing in the `off` tier, before init is held, after
 * disableTelemetry() dropped).
 *
 *   trackExposure("new-checkout", "treatment", { provider: "launchdarkly" });
 */
export function trackExposure(
  flag: string,
  variant: string | boolean | number,
  options?: ExposureOptions,
): void {
  try {
    if (typeof flag !== "string") {
      return;
    }
    const key = clean(flag);
    const name = variantName(variant);
    if (key === "" || name === "") {
      return;
    }
    const run = optional(options?.run);
    const account = stampableTelemetryId(optional(options?.account));
    const day = new Date().toISOString().slice(0, 10);
    const mark = `${stampedTelemetryUser()}\u0000${key}\u0000${name}\u0000${run}\u0000${account}\u0000${day}`;
    if (seen.has(mark)) {
      return;
    }
    seen.add(mark);
    const props: Record<string, string> = { flag: key, variant: name };
    const provider = optional(options?.provider).toLowerCase();
    if (provider !== "") {
      props.provider = provider;
    }
    if (run !== "") {
      props.run = run;
    }
    const unit = optional(options?.unit);
    if (unit !== "") {
      props.unit = unit;
    }
    const reason = optional(options?.reason);
    if (reason !== "") {
      props.reason = reason;
    }
    const revision = options?.revision;
    if (
      typeof revision === "number" &&
      Number.isSafeInteger(revision) &&
      revision >= 0
    ) {
      props.revision = String(revision);
    }
    if (account !== "") {
      props.account = account;
    }
    recordProductEvent(FLAG_EVALUATED_EVENT, props);
  } catch {
    // Telemetry must never break the app.
  }
}

/** The slice of an OpenFeature hook context the hook reads. */
export interface ExposureHookContext {
  flagKey?: string;
  providerMetadata?: { name?: string };
}

/** The slice of OpenFeature evaluation details the hook reads. */
export interface ExposureEvaluationDetails {
  flagKey?: string;
  value?: unknown;
  variant?: string;
  errorCode?: string;
}

/**
 * An OpenFeature hook recording every successful evaluation as an exposure
 * (trackExposure): `OpenFeature.addHooks(forgeExposureHook)`. Works with the
 * web and server SDKs and any provider; the variant is the evaluation's
 * variant name, else its value when that is a string, boolean or number.
 */
export const forgeExposureHook = {
  after(
    hookContext: ExposureHookContext,
    details: ExposureEvaluationDetails,
  ): void {
    try {
      // Forge Changes' own provider records its evaluations, with the run
      // and reason this hook cannot see: counting them here too would
      // record each one twice.
      if (
        details?.errorCode ||
        hookContext?.providerMetadata?.name === FORGE_CHANGES_PROVIDER
      ) {
        return;
      }
      const flag = details?.flagKey ?? hookContext?.flagKey;
      if (typeof flag !== "string") {
        return;
      }
      const variant =
        typeof details.variant === "string" && details.variant !== ""
          ? details.variant
          : details.value;
      if (
        typeof variant !== "string" &&
        typeof variant !== "boolean" &&
        typeof variant !== "number"
      ) {
        if (variant === undefined || variant === null) {
          return;
        }
        trackExposure(flag, "object", {
          provider: hookContext?.providerMetadata?.name,
        });
        return;
      }
      trackExposure(flag, variant, {
        provider: hookContext?.providerMetadata?.name,
      });
    } catch {
      // A hook must never break flag evaluation.
    }
  },
};

/** Test seam. */
export function resetExposureState(): void {
  seen = new Set();
}
