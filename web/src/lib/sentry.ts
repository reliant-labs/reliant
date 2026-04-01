import * as Sentry from "@sentry/react";
import { getPrivacySettings } from "../store/privacyStore";
import { isDev } from "./constants";

// Regex to detect prerelease versions (RC, alpha, beta)
const PRERELEASE_REGEX = /-rc\.|rc[0-9]+|-beta\.|beta[0-9]+|-alpha\.|alpha[0-9]+/i;

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

  Sentry.init({
    dsn,
    environment: sentryEnvironment,
    release: `reliant@${version}`,
    integrations: [
      Sentry.browserTracingIntegration(),
      Sentry.replayIntegration({
        maskAllText: true,
        maskAllInputs: true,
        blockAllMedia: true,
      }),
    ],
    tracesSampleRate: parseFloat(import.meta.env.VITE_SENTRY_TRACES_SAMPLE_RATE) || (isPrerelease ? 1.0 : 0.1),
    replaysSessionSampleRate: parseFloat(import.meta.env.VITE_SENTRY_REPLAYS_SESSION_SAMPLE_RATE) || (isPrerelease ? 1.0 : 0.1),
    replaysOnErrorSampleRate: parseFloat(import.meta.env.VITE_SENTRY_REPLAYS_ERROR_SAMPLE_RATE) || 1.0,
    tracePropagationTargets: ["localhost"],
    beforeSend(event, hint) {
      // Check privacy settings before sending
      const { crashReportingEnabled } = getPrivacySettings();
      if (!crashReportingEnabled) {
        return null;
      }
      if (isDev) {
        console.error("Sentry Event:", event, hint);
      }
      return event;
    },
  });

  console.log(`[Sentry] Initialized (environment: ${sentryEnvironment}, release: reliant@${version})`);
}

export function setSentryUser(user: { id: string; email?: string } | null) {
  if (isDev) return;
  if (user) {
    Sentry.setUser({ id: user.id, email: user.email });
  } else {
    Sentry.setUser(null);
  }
}