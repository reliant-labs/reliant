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
// An evaluation that failed (an OpenFeature `errorCode`) is no exposure. A
// variant is a name: an object-valued flag with no variant name is recorded
// as `object`, never its contents.
//
// Dependency-free apart from track.ts, like it: an app's flag provider calls
// the hook whether or not the SDK is installed.
import { recordProductEvent, stampedTelemetryUser } from "./track.js";

/** The product event an exposure is recorded as. */
export const FLAG_EVALUATED_EVENT = "flag_evaluated";

/** Longest flag key, variant or provider name recorded. */
export const MAX_FLAG_VALUE_CHARS = 128;

/** trackExposure's options. */
export interface ExposureOptions {
  /** The flag provider ("launchdarkly", "flagd", "statsig", your own). */
  provider?: string;
}

let seen = new Set<string>();

function clean(value: string): string {
  // eslint-disable-next-line no-control-regex
  return value
    .replace(/[\u0000-\u001f\u007f]/g, "")
    .trim()
    .slice(0, MAX_FLAG_VALUE_CHARS);
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
 * flag, variant) per UTC day per page load; never throws; inherits track()'s
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
    const day = new Date().toISOString().slice(0, 10);
    const mark = `${stampedTelemetryUser()}\u0000${key}\u0000${name}\u0000${day}`;
    if (seen.has(mark)) {
      return;
    }
    seen.add(mark);
    const props: Record<string, string> = { flag: key, variant: name };
    const provider =
      typeof options?.provider === "string"
        ? clean(options.provider).toLowerCase()
        : "";
    if (provider !== "") {
      props.provider = provider;
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
      if (details?.errorCode) {
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
