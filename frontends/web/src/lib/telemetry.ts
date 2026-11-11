/**
 * Browser telemetry on forge's web runtime (the HyperDX / ClickStack SDK).
 *
 * Errors, page loads, fetch/XHR spans and masked session replay go to the
 * runtime config's OTEL_ENDPOINT. Nothing starts unless that is set
 * (telemetryEndpoint.ts). A failure the app catches itself is reported through
 * errorReporting.ts.
 *
 * Consent: init only when crash reporting is on. The SDK has no off switch for
 * its own auto-instrumentation (page loads, fetch/XHR, uncaught errors), which
 * is why init, not a flag, is the gate. So turning it off mid-session calls the
 * runtime's disableTelemetry() at once (replay, product events, reported
 * errors) and then, once the switch is saved, reloads the page: the reload
 * starts without the SDK, so its automatic spans stop too.
 *
 * Privacy rules the runtime forces while replay is on: all text and inputs
 * masked, media blocked, no canvas, no request/response bodies, no query
 * strings on fetch/XHR URLs. consoleCapture is off here because console output
 * can hold chat content.
 *
 * The SDK records any Error passed to console.error regardless. A
 * console.error about an RPC failure the transport already reported, or
 * decided not to, goes around it (keepHandledRpcFailuresOutOfSdk).
 */

import { disableTelemetry, setTelemetryUser } from "@reliantlabs/forge-web-runtime";
import { supabaseAuthTelemetryListener } from "@reliantlabs/forge-web-runtime/auth/supabase";
import type { Session } from "@supabase/supabase-js";
import {
  enableAttribution,
  initBrowserTelemetry,
  type BrowserTelemetryConfig,
} from "@reliantlabs/forge-web-runtime/telemetry";
import {
  privacyConsent,
  privacyWritesSettled,
  subscribePrivacyConsent,
} from "../hooks/privacy-queries";
import { appBuild } from "./appBuild";
import { loadConfig, type ReliantWebConfig } from "./config_gen";
import { isDev } from "./constants";
import { reportFirstLaunch } from "./appFirstLaunch";
import { dropLandingAttribution, readLandingAttribution } from "./landingAttribution";
import { reportError } from "./errorReporting";
import { isRpcFailureDecided } from "./handledRpcFailures";
import { clickstackEnabled } from "./telemetryEndpoint";
import { SERVICE_NAME } from "./otel_gen";
import { getShell, type AppShell } from "./shell";
import { demoTelemetry, type DemoTelemetry } from "./demoTelemetry";

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

/** The demo's replay share: OTEL_REPLAY_SAMPLE_RATE when it is a number, else every session. */
export function demoReplayRate(rateOverride?: string): number {
  const override = parseFloat(rateOverride ?? "");
  return Number.isNaN(override) ? 1 : override;
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
  /** The app shell this page mounts (lib/shell.ts). Default "reliant". */
  shell?: AppShell;
  /** In the demo shell: what it records (demoTelemetry.ts). Replay follows this, not OTEL_REPLAY. */
  demoTelemetry?: DemoTelemetry;
  /** This bundle is the forge demo's own build (demo.html, `__WEB_TARGET__`), the `forge-demo` frontend. */
  demoBuild?: boolean;
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

/**
 * The service.name the forge shell reports (lib/shell.ts), so forge's funnels
 * and dashboards separate from Reliant's by ServiceName with no new column.
 * Only the SERVICE changes: the bundle is still the `reliant-web` frontend,
 * which is the name its source maps are filed under.
 */
export const FORGE_SHELL_SERVICE_NAME = "forge-web";

/**
 * The service.name the public demo reports (lib/shell.ts `demo`): its
 * anonymous visitors must not move forge-web's error rate or funnels, and
 * `product_events` is keyed by ServiceName, so no new column is needed.
 * It is forge's derivation for the demo's own frontend (`<project>-<frontend>`,
 * reliant's `forge-demo` in deploy/kcl/demo), the name its generated dashboard
 * and alert defaults filter on (deploy/observability/).
 */
export const DEMO_SHELL_SERVICE_NAME = "reliant-forge-demo";

/**
 * The forge demo's own build (demo.html) is the `forge-demo` frontend of the
 * `demo` env: its source maps are filed under that name, not reliant-web's.
 */
export const DEMO_FRONTEND = "forge-demo";
export const DESKTOP_FRONTEND = "desktop";

const SHELL_SERVICE_NAME: Record<AppShell, string> = {
  reliant: SERVICE_NAME,
  forge: FORGE_SHELL_SERVICE_NAME,
  demo: DEMO_SHELL_SERVICE_NAME,
};

/** The runtime config for this build. Pure; the privacy-relevant options are fixed here. */
export function buildTelemetryConfig(
  input: TelemetryConfigInput,
): BrowserTelemetryConfig {
  const { config } = input;
  const demo = input.shell === "demo";
  return {
    // forge's derivation for this frontend ("<project>-<frontend>"), the
    // service.name the generated dashboards and alert defaults filter on
    // (deploy/observability/). A literal here drifted from it once, and every
    // web alert matched nothing.
    serviceName: SHELL_SERVICE_NAME[input.shell ?? "reliant"],
    frontend: input.desktop ? DESKTOP_FRONTEND : input.demoBuild ? DEMO_FRONTEND : WEB_FRONTEND,
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
    // The demo records replays by its own switch (DEMO_TELEMETRY, owner
    // default on); the runtime masks all text and inputs whenever replay runs.
    replay: demo ? input.demoTelemetry === "replay" : config.OTEL_REPLAY === "true",
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

/**
 * Route a console.error that names an RPC failure the transport already
 * decided on (lib/handledRpcFailures.ts) around the SDK, to `beneathSdk`, so
 * it is logged but not recorded as an exception a second time. Every other
 * call goes through the SDK as before, so an error nothing handled is still
 * recorded — and so is an uncaught RPC failure, which reaches the SDK through
 * "unhandledrejection", not console.error.
 *
 * Installed outermost, after the SDK and the runtime's own console tap.
 * Returns the undo.
 */
export function keepHandledRpcFailuresOutOfSdk(
  beneathSdk: (...args: unknown[]) => void,
): () => void {
  const throughSdk = console.error;
  const filtered = function (this: unknown, ...args: unknown[]): void {
    if (args.some(isRpcFailureDecided)) {
      beneathSdk.apply(console, args);
      return;
    }
    throughSdk.apply(this, args);
  };
  console.error = filtered;
  return () => {
    if (console.error === filtered) console.error = throughSdk;
  };
}

let undoRpcConsoleFilter: (() => void) | null = null;
let started: Promise<boolean> | null = null;
let unsubscribeConsent: (() => void) | null = null;
let withdrawing = false;
let reloadPage: () => void = () => window.location.reload();

/**
 * Crash reporting switched off while the SDK runs: stop what the runtime can
 * stop now, then reload into the consent-gated startup, which leaves the SDK
 * out. The reload waits for the write, which a reload would abandon; a write
 * the server refused rolls the switch back, and then there is nothing to
 * reload for (this page's replay and events stay off until the next load).
 */
function onConsentChange(consent: { crashReportingEnabled: boolean }): void {
  if (consent.crashReportingEnabled || withdrawing) return;
  withdrawing = true;
  disableTelemetry();
  void privacyWritesSettled().then(() => {
    if (privacyConsent().crashReportingEnabled) {
      withdrawing = false;
      return;
    }
    reloadPage();
  });
}

/**
 * Start browser telemetry. Idempotent, never throws, and resolves to whether
 * it is running. Concurrent callers share one attempt; an attempt that ends
 * not running is forgotten, so a later call tries again (and can still hand
 * over the held landing attribution).
 */
export function initTelemetry(): Promise<boolean> {
  if (started) return started;
  if (isDev || !clickstackEnabled()) return Promise.resolve(false);
  if (!privacyConsent().crashReportingEnabled) return Promise.resolve(false);
  // The demo's anonymous visitors: nothing at all when it is switched off or
  // the browser sent Do Not Track / Global Privacy Control (demoTelemetry.ts).
  if (getShell() === "demo" && demoTelemetry() === "off") return Promise.resolve(false);

  const attempt: Promise<boolean> = start()
    .catch((err) => {
      console.warn("[telemetry] init failed:", err);
      return false;
    })
    .then((running) => {
      if (!running && started === attempt) started = null;
      return running;
    });
  started = attempt;
  return attempt;
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
  const shell = getShell();
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
    shell,
    demoBuild: __WEB_TARGET__ === "demo",
    demoTelemetry: shell === "demo" ? demoTelemetry() : undefined,
    // The demo records every visit (a numeric override still wins); its
    // visitors have no sessions to count toward the new-user policy.
    replayRate: shell === "demo"
      ? demoReplayRate(runtime.OTEL_REPLAY_SAMPLE_RATE)
      : replaySampleRate({
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

  // The landing site's rid / utm_* handoff, held since boot: by now the router and
  // the sign-in have replaced the URL it arrived on (landingAttribution.ts). Handed
  // to the runtime only with analytics consent, as the landing site gave it only
  // with that consent, and dropped without it. It stays held until the runtime is
  // running, so a failed start leaves it for the next page load's attempt.
  const handoff = readLandingAttribution();
  if (handoff) {
    if (privacyConsent().analyticsEnabled) {
      enableAttribution({ search: handoff });
    } else {
      dropLandingAttribution();
    }
  }

  // console.error as it is before the SDK wraps it (the logger's redirect):
  // where a handled RPC failure's console line goes instead of the SDK.
  const beneathSdk = console.error;
  const running = await initBrowserTelemetry(config);
  if (!running) return false;
  if (handoff) dropLandingAttribution();
  undoRpcConsoleFilter?.();
  undoRpcConsoleFilter = keepHandledRpcFailuresOutOfSdk(beneathSdk);

  unsubscribeConsent = subscribePrivacyConsent(onConsentChange);
  // The subscription does not replay the current consent, and initBrowserTelemetry
  // awaited visibility and the SDK import: a withdrawal saved in that window
  // fired no event we heard. Reconcile now, so it still stops and reloads.
  onConsentChange(privacyConsent());

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
  forwardUserToElectron(user);
  if (!clickstackEnabled()) return;
  setTelemetryUser(user ? user.id : null);
}

function forwardUserToElectron(user: { id: string } | null): void {
  if (isDev) return;
  try {
    void window.electronAPI?.setTelemetryUser?.(user ? user.id : null)?.catch(() => {});
  } catch {
    // Best-effort.
  }
}

// forge's Supabase adapter: identifyUser for a signed-in session (the id on
// spans and replays, and the daily auth_signed_in {kind: resumed}), and
// auth_signed_out on SIGNED_OUT, each with provider "supabase". It skips
// anonymous sessions, which are not signed-in users.
const supabaseAuthTelemetry = supabaseAuthTelemetryListener();

/**
 * Report a Supabase auth change (onAuthStateChange's event and session) to
 * telemetry. An anonymous session is nobody: no id is attached, here or in
 * the Electron main process, and it is never counted as a signed-in user (the
 * API server's daily heartbeat skips it too). The standard auth events are
 * product events, so they follow the analytics switch; with it off, only the
 * id is attached, for error correlation, as before.
 */
export function reportAuthChange(event: string, session: Session | null): void {
  const user =
    session?.user && session.user.is_anonymous !== true ? { id: session.user.id } : null;
  if (isDev || !privacyConsent().analyticsEnabled) {
    setTelemetryUserId(user);
    return;
  }
  forwardUserToElectron(user);
  if (!clickstackEnabled()) return;
  supabaseAuthTelemetry(event, session);
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
  undoRpcConsoleFilter?.();
  undoRpcConsoleFilter = null;
  unsubscribeConsent?.();
  unsubscribeConsent = null;
  withdrawing = false;
  reloadPage = () => window.location.reload();
}

/** Test seam: what a consent withdrawal reloads with. */
export function setReloadForTests(reload: () => void): void {
  reloadPage = reload;
}
