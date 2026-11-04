/**
 * Browser telemetry on forge's web runtime (the HyperDX / ClickStack SDK).
 *
 * Errors, page loads, fetch/XHR spans and masked session replay go to the
 * runtime config's OTEL_ENDPOINT. Nothing starts unless that is set
 * (telemetryEndpoint.ts). A failure the app catches itself is reported through
 * errorReporting.ts.
 *
 * Consent: init only when crash reporting is on. Turning it off mid-session
 * calls the runtime's disableTelemetry(). The SDK has no off switch for its own
 * auto-instrumentation, which is why init, not a flag, is the gate.
 *
 * Privacy rules the runtime forces while replay is on: all text and inputs
 * masked, media blocked, no canvas, no request/response bodies, no query
 * strings on fetch/XHR URLs. consoleCapture is off here because console output
 * can hold chat content.
 */

import { disableTelemetry, setTelemetryUser } from "@reliantlabs/forge-web-runtime";
import {
  initBrowserTelemetry,
  type BrowserTelemetryConfig,
} from "@reliantlabs/forge-web-runtime/telemetry";
import { privacyConsent, subscribePrivacyConsent } from "../hooks/privacy-queries";
import { appBuild } from "./appBuild";
import { loadConfig, type ReliantWebConfig } from "./config_gen";
import { isDev } from "./constants";
import { reportFirstLaunch } from "./appFirstLaunch";
import { reportError } from "./errorReporting";
import { clickstackEnabled } from "./telemetryEndpoint";
import { SERVICE_NAME } from "./otel_gen";

const PRERELEASE_REGEX = /-rc\.|rc[0-9]+|-beta\.|beta[0-9]+|-alpha\.|alpha[0-9]+/i;

const TRACKING_KEY = "telemetry_session_tracking";
const COUNTED_KEY = "telemetry_session_counted";
const NEW_USER_SESSION_LIMIT = 5;
const NEW_USER_WINDOW_MS = 15 * 60 * 1000;
const NEW_USER_RATE = 1.0;
const DEFAULT_RATE = 0.1;

/** Replay policy name that records a new user's first sessions in full. */
export const NEW_USER_FUNNEL_POLICY = "new-user-funnel";

/** Longest React component stack attached to a reported error. */
const MAX_COMPONENT_STACK_CHARS = 4000;

interface SessionTracking {
  firstSeenAt: number;
  sessionCount: number;
}

interface ReplayRateInput {
  prerelease: boolean;
  policy?: string;
  rateOverride?: string;
  now: number;
  local: Pick<Storage, "getItem" | "setItem"> | null;
  session: Pick<Storage, "getItem" | "setItem"> | null;
}

/**
 * The share of sessions to record this load.
 *
 * Prerelease records everything. A numeric override wins next. Under the
 * `new-user-funnel` policy a new user (first 5 sessions or first 15 minutes)
 * is recorded in full and everyone else at 10%; with no policy the rate is
 * 10%. A tab counts as one session however often it reloads.
 */
export function replaySampleRate(input: ReplayRateInput): number {
  if (input.prerelease) return 1;

  const override = parseFloat(input.rateOverride ?? "");
  if (!Number.isNaN(override)) return override;

  if (input.policy !== NEW_USER_FUNNEL_POLICY) return DEFAULT_RATE;

  try {
    const raw = input.local?.getItem(TRACKING_KEY);
    let tracking: SessionTracking = raw
      ? JSON.parse(raw)
      : { firstSeenAt: input.now, sessionCount: 0 };

    if (input.session?.getItem(COUNTED_KEY) !== "1") {
      tracking = { ...tracking, sessionCount: tracking.sessionCount + 1 };
      input.local?.setItem(TRACKING_KEY, JSON.stringify(tracking));
      input.session?.setItem(COUNTED_KEY, "1");
    }

    const isNewUser =
      tracking.sessionCount <= NEW_USER_SESSION_LIMIT ||
      input.now - tracking.firstSeenAt < NEW_USER_WINDOW_MS;
    return isNewUser ? NEW_USER_RATE : DEFAULT_RATE;
  } catch {
    return DEFAULT_RATE;
  }
}

export interface TelemetryConfigInput {
  /** The runtime config's fields this module reads (config_gen.ts). */
  config: Partial<
    Pick<
      ReliantWebConfig,
      | "OTEL_ENDPOINT"
      | "OTEL_REPLAY"
      | "API_URL"
      | "CONTROL_PLANE_API_URL"
      | "GATEWAY_URL"
    >
  >;
  prerelease: boolean;
  /** forge's frontend build id (VITE_APP_VERSION), when forge built the bundle. */
  appVersion?: string;
  /** This bundle's baked build version (appBuild().version). */
  buildVersion: string;
  /** True in the Electron renderer (window.electronAPI is present). */
  desktop?: boolean;
  replayRate: number;
  extraApiOrigins?: Array<string | undefined>;
}

/**
 * The forge frontend names this bundle reports as (`forge.frontend`). They are
 * the names control-plane files the build's source maps under, and the key the
 * issues enrichment worker looks them up by: a bundle that reports none cannot
 * be symbolicated. The hosted SPA is the `reliant-web` frontend of the
 * `prod-web` env; the packaged app's keyed ingest key is the `desktop`
 * frontend (deploy/kcl/lib/env.k, reliant_desktop_ingest_keys).
 */
export const WEB_FRONTEND = "reliant-web";
export const DESKTOP_FRONTEND = "desktop";

/** The runtime config for this build. Pure; the privacy-relevant options are fixed here. */
export function buildTelemetryConfig(
  input: TelemetryConfigInput,
): BrowserTelemetryConfig {
  const { config } = input;
  return {
    // forge's derivation for this frontend ("<project>-<frontend>"), the
    // service.name the generated dashboards and alert defaults filter on
    // (deploy/observability/). A literal here drifted from it once, and every
    // web alert matched nothing.
    serviceName: SERVICE_NAME,
    frontend: input.desktop ? DESKTOP_FRONTEND : WEB_FRONTEND,
    serviceVersion: input.appVersion || input.buildVersion,
    environment: input.prerelease ? "prerelease" : "prod",
    endpoint: config.OTEL_ENDPOINT || undefined,
    apiOrigins: [
      config.API_URL || undefined,
      config.CONTROL_PLANE_API_URL || undefined,
      config.GATEWAY_URL || undefined,
      ...(input.extraApiOrigins ?? []),
    ],
    // The product-event contract's client tag (dev-docs/product-events.md).
    // Named here rather than left to the runtime's detection so it follows
    // the same electronAPI test as the rest of the app.
    appClient: input.desktop ? "electron" : "web",
    consoleCapture: false,
    advancedNetworkCapture: false,
    replay: config.OTEL_REPLAY === "true",
    replaySampleRate: input.replayRate,
  };
}

interface GlobalAttributeClient {
  setGlobalAttributes(attributes: Record<string, string>): void;
}

// @hyperdx/browser's module.exports IS the client; a bundler wraps it in
// namespace objects whose `default` leads to it. Take the deepest `default`
// that is still a client, as the runtime does, so both hold the same instance.
function findClient(mod: unknown): GlobalAttributeClient | null {
  const isClient = (v: unknown): v is GlobalAttributeClient =>
    typeof (v as GlobalAttributeClient | null)?.setGlobalAttributes ===
    "function";
  let cur: unknown = mod;
  for (let depth = 0; depth < 4; depth++) {
    const next = (cur as { default?: unknown } | null)?.default;
    if (!isClient(next)) break;
    cur = next;
  }
  return isClient(cur) ? cur : null;
}

// The runtime has no hook for the build, so it rides as a global span
// attribute beside the app.client the runtime stamps.
async function stampClientAttributes(
  attributes: Record<string, string>,
): Promise<void> {
  const client = findClient(await import("@hyperdx/browser"));
  client?.setGlobalAttributes(attributes);
}

let started: Promise<boolean> | null = null;
let unsubscribeConsent: (() => void) | null = null;

/**
 * Start browser telemetry. Idempotent, never throws, and resolves to whether
 * it is running.
 */
export function initTelemetry(): Promise<boolean> {
  if (started) return started;
  if (isDev || !clickstackEnabled()) return Promise.resolve(false);
  if (!privacyConsent().crashReportingEnabled) return Promise.resolve(false);

  started = start().catch((err) => {
    console.warn("[telemetry] init failed:", err);
    return false;
  });
  return started;
}

async function start(): Promise<boolean> {
  const runtime = loadConfig();
  const desktop = typeof window !== "undefined" && !!window.electronAPI;

  let prerelease = false;
  try {
    if (window.electronAPI?.getVersion) {
      prerelease = PRERELEASE_REGEX.test(await window.electronAPI.getVersion());
    }
  } catch (e) {
    console.warn("[telemetry] failed to get version:", e);
  }

  // Consent may have been withdrawn while the version was being read.
  if (!privacyConsent().crashReportingEnabled) return false;

  const buildVersion = appBuild()?.version ?? "unknown";
  const config = buildTelemetryConfig({
    config: runtime,
    // BUILD-TIME ON PURPOSE, the one VITE_ read left: forge stamps its
    // frontend build id when it builds the bundle. It names these bytes, so
    // it is the same in every environment the build is promoted to, and the
    // runtime config (per environment) is the wrong home for it.
    // forge:lint-disable-next-line forgeconv-frontend-process-env: a build stamp, not deployment config
    appVersion: import.meta.env.VITE_APP_VERSION as string | undefined,
    prerelease,
    buildVersion,
    desktop,
    replayRate: replaySampleRate({
      prerelease,
      policy: runtime.OTEL_REPLAY_SAMPLE_POLICY,
      rateOverride: runtime.OTEL_REPLAY_SAMPLE_RATE,
      now: Date.now(),
      local: safeStorage("localStorage"),
      session: safeStorage("sessionStorage"),
    }),
    extraApiOrigins: [
      window.RELIANT_CONFIG?.grpcUrl,
      window.RELIANT_CONFIG?.controlPlaneURL,
      window.RELIANT_CONFIG?.gatewayUrl,
    ],
  });

  const running = await initBrowserTelemetry(config);
  if (!running) return false;

  unsubscribeConsent = subscribePrivacyConsent((consent) => {
    if (!consent.crashReportingEnabled) disableTelemetry();
  });

  try {
    await stampClientAttributes({ "reliant.build": buildVersion });
  } catch (e) {
    console.debug("[telemetry] client attributes not set:", e);
  }
  if (desktop) {
    reportFirstLaunch().catch((e) => {
      console.debug("[telemetry] app_first_launch not reported:", e);
    });
  }
  return true;
}

function safeStorage(
  which: "localStorage" | "sessionStorage",
): Storage | null {
  try {
    return window[which];
  } catch {
    return null;
  }
}

/**
 * Attach the signed-in user's id (never the email) to telemetry, or clear it:
 * this page's, and the Electron main process's, whose own telemetry cannot see
 * the renderer's user.
 */
export function setTelemetryUserId(user: { id: string } | null): void {
  if (!isDev) {
    try {
      void window.electronAPI?.setTelemetryUser?.(user ? user.id : null)?.catch(() => {});
    } catch {
      // Best-effort.
    }
  }
  if (!clickstackEnabled()) return;
  setTelemetryUser(user ? user.id : null);
}

/**
 * Hand an error a React boundary caught to the SDK. The runtime skips an error
 * the SDK already recorded, so a boundary-caught error is one record.
 */
export function reportErrorToTelemetry(
  error: unknown,
  componentStack?: string | null,
): void {
  if (!privacyConsent().crashReportingEnabled) return;
  reportError(
    error,
    componentStack
      ? { componentStack: componentStack.slice(0, MAX_COMPONENT_STACK_CHARS) }
      : undefined,
  );
}

/** Test seam: forget that telemetry started. */
export function resetTelemetryForTests(): void {
  started = null;
  unsubscribeConsent?.();
  unsubscribeConsent = null;
}
