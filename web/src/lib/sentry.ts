import * as Sentry from "@sentry/react";
import { getPrivacySettings } from "../store/privacyStore";
import { isDev } from "./constants";

// Regex to detect prerelease versions (RC, alpha, beta)
const PRERELEASE_REGEX = /-rc\.|rc[0-9]+|-beta\.|beta[0-9]+|-alpha\.|alpha[0-9]+/i;

const SENTRY_STORAGE_KEY = "sentry_session_tracking";
const FUNNEL_SESSION_LIMIT = 5;
const FUNNEL_TIME_LIMIT_MS = 15 * 60 * 1000; // 15 minutes
const FUNNEL_SAMPLE_RATE = 1.0;
const DEFAULT_SAMPLE_RATE = 0.1;

interface SessionTracking {
  firstSeenAt: number;
  sessionCount: number;
}

/**
 * Compute the replay sample rate based on session history.
 * New users (first 5 sessions or first 15 minutes) get 100% capture
 * for funnel analysis. After that, drops to 10%.
 */
function getReplaySessionSampleRate(isPrerelease: boolean): number {
  // Prerelease always captures everything
  if (isPrerelease) return 1.0;

  // Env var override takes precedence
  const envRate = parseFloat(import.meta.env.VITE_SENTRY_REPLAYS_SESSION_SAMPLE_RATE);
  if (!isNaN(envRate)) return envRate;

  try {
    const raw = localStorage.getItem(SENTRY_STORAGE_KEY);
    const now = Date.now();
    let tracking: SessionTracking;

    if (raw) {
      tracking = JSON.parse(raw);
      tracking.sessionCount += 1;
    } else {
      tracking = { firstSeenAt: now, sessionCount: 1 };
    }

    localStorage.setItem(SENTRY_STORAGE_KEY, JSON.stringify(tracking));

    const isNewUser =
      tracking.sessionCount <= FUNNEL_SESSION_LIMIT ||
      now - tracking.firstSeenAt < FUNNEL_TIME_LIMIT_MS;

    return isNewUser ? FUNNEL_SAMPLE_RATE : DEFAULT_SAMPLE_RATE;
  } catch {
    // localStorage unavailable — fall back to default
    return DEFAULT_SAMPLE_RATE;
  }
}

export async function initSentry() {
  // Skip Sentry initialization in development or if explicitly disabled
  if (isDev || import.meta.env.VITE_SENTRY_ENABLED === "false") {
    return;
  }

  // Respect user privacy settings
  const { crashReportingEnabled } = getPrivacySettings();
  if (!crashReportingEnabled) {
    return;
  }

  const dsn = import.meta.env.VITE_SENTRY_DSN;

  if (!dsn) {
    console.warn("Sentry DSN not configured. Error tracking disabled.");
    return;
  }

  // Get version from Electron API to determine environment
  let version = "unknown";
  let isPrerelease = false;
  
  try {
    if (window.electronAPI?.getVersion) {
      version = await window.electronAPI.getVersion();
      isPrerelease = PRERELEASE_REGEX.test(version);
    }
  } catch (e) {
    console.warn("[Sentry] Failed to get version:", e);
  }

  // Set environment based on release type for filtering in Sentry dashboard
  const sentryEnvironment = isPrerelease ? "prerelease" : "production";

  const replaySessionRate = getReplaySessionSampleRate(isPrerelease);

  Sentry.init({
    dsn,
    environment: sentryEnvironment,
    release: `reliant@${version}`,
    integrations: [
      Sentry.browserTracingIntegration(),
      Sentry.replayIntegration({
        // Show UI text and layout in replays for funnel analysis.
        // User inputs stay masked. Sensitive displayed text (API keys, error
        // messages with tokens) is protected via data-sentry-mask attributes.
        maskAllText: false,
        maskAllInputs: true,
        blockAllMedia: false,
      }),
    ],
    tracesSampleRate: parseFloat(import.meta.env.VITE_SENTRY_TRACES_SAMPLE_RATE) || (isPrerelease ? 1.0 : 0.1),
    replaysSessionSampleRate: replaySessionRate,
    replaysOnErrorSampleRate: parseFloat(import.meta.env.VITE_SENTRY_REPLAYS_ERROR_SAMPLE_RATE) || 1.0,
    tracePropagationTargets: ["localhost", /^\//],
    beforeSend(event, hint) {
      // Check privacy settings before sending
      const { crashReportingEnabled } = getPrivacySettings();
      if (!crashReportingEnabled) {
        return null;
      }

      // Filter out noise errors that aren't actionable bugs
      if (shouldDropEvent(event, hint)) {
        return null;
      }

      if (isDev) {
        console.error("Sentry Event:", event, hint);
      }
      return event;
    },
  });

  console.log(`[Sentry] Initialized (environment: ${sentryEnvironment}, release: reliant@${version}, replayRate: ${replaySessionRate})`);
}

/**
 * Patterns that indicate non-actionable errors which should not be reported to Sentry.
 * These are user-initiated cancellations, expected transient failures, or configuration issues.
 */
const NOISE_ERROR_PATTERNS = [
  // User-initiated cancellations
  "aborted a request",
  "signal is aborted",
  "streaming cancelled by user",
  "the operation was aborted",
  // Transient network errors (backend unavailable during startup/shutdown)
  "failed to fetch",
  "load failed",
  "networkerror",
] as const;

/** Error types (by name) that are always dropped. */
const NOISE_ERROR_NAMES = new Set([
  "AbortError",
]);

function shouldDropEvent(event: Sentry.ErrorEvent, hint: Sentry.EventHint): boolean {
  const error = hint.originalException;

  // Drop by error name
  if (error instanceof Error && NOISE_ERROR_NAMES.has(error.name)) {
    return true;
  }

  // Drop by error message pattern
  const message = (error instanceof Error ? error.message : String(error ?? "")).toLowerCase();
  if (NOISE_ERROR_PATTERNS.some((p) => message.includes(p))) {
    return true;
  }

  // Also check event exception values (for errors where hint doesn't have the original)
  const exceptionValues = event.exception?.values;
  if (exceptionValues) {
    for (const ex of exceptionValues) {
      if (ex.type && NOISE_ERROR_NAMES.has(ex.type)) {
        return true;
      }
      const val = (ex.value ?? "").toLowerCase();
      if (NOISE_ERROR_PATTERNS.some((p) => val.includes(p))) {
        return true;
      }
    }
  }

  return false;
}

export function setSentryUser(user: { id: string; email?: string } | null) {
  if (isDev) return;
  if (user) {
    Sentry.setUser({ id: user.id, email: user.email });
  } else {
    Sentry.setUser(null);
  }
}