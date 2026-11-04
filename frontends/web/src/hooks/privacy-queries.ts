/**
 * Privacy consent (crash reporting, analytics) on the generated
 * GetPrivacySettings entry: the Privacy settings switches read it with a
 * hook, and telemetry, error reporting, analytics and the error boundary read it
 * imperatively (privacyConsent / subscribePrivacyConsent). There is no
 * zustand copy.
 *
 * AuthInitializer loads it once per sign-in (loadPrivacySettings), before
 * telemetry starts, because telemetry only starts with consent.
 *
 * Two rules keep the imperative readers honest:
 *  - the entry is never garbage-collected (gcTime Infinity). Nothing observes
 *    it most of the time, and a collected entry would read as the default —
 *    consent — for a user who opted out;
 *  - a sign-out clears the query cache, so the last answer seen is kept for
 *    the signed-out stretch: an opt-out still holds on the sign-in screen.
 *
 * This module deliberately does not import the auth store: lib/telemetry and
 * lib/errorReporting import it, and the auth store imports them.
 */

import { matchQuery, useQueryClient } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";

import {
  GetPrivacySettingsResponseSchema,
  type GetPrivacySettingsResponse,
} from "../gen/reliant/v1/settings_pb";
import { waitForConfig } from "../lib/configReady";
import { logger } from "../lib/logger";
import { queryClient } from "../lib/query-client";
import {
  settingsServiceKeys,
  settingsServiceQueries,
  useGetPrivacySettings,
  useUpdatePrivacySettings as useUpdatePrivacySettingsRpc,
} from "./settings-service-hooks_gen";

export interface PrivacyConsent {
  crashReportingEnabled: boolean;
  analyticsEnabled: boolean;
}

/** Before the first answer: consent, as the server defaults it. */
const DEFAULT_CONSENT: PrivacyConsent = { crashReportingEnabled: true, analyticsEnabled: true };

export const privacySettingsKey = () => settingsServiceKeys.getPrivacySettings({});

function consentOf(response: GetPrivacySettingsResponse): PrivacyConsent {
  return {
    crashReportingEnabled: response.crashReportingEnabled,
    analyticsEnabled: response.analyticsEnabled,
  };
}

const isPrivacyEntry = (query: Parameters<typeof matchQuery>[1]) =>
  matchQuery({ queryKey: privacySettingsKey(), exact: true }, query);

let consentAfterClear: PrivacyConsent | undefined;
let cacheRulesInstalled = false;

/**
 * Lazily, not at import: this module sits in an import cycle with the auth
 * store (see the header), so its body must not touch another module's
 * bindings while the graph is still evaluating.
 */
function installCacheRules(): void {
  if (cacheRulesInstalled) return;
  cacheRulesInstalled = true;
  queryClient.setQueryDefaults(privacySettingsKey(), { gcTime: Infinity });
  queryClient.getQueryCache().subscribe((event) => {
    if (event.type !== "removed" || !isPrivacyEntry(event.query)) return;
    const data = event.query.state.data as GetPrivacySettingsResponse | undefined;
    if (data) consentAfterClear = consentOf(data);
  });
}

/** The user's consent now, for a non-React reader (no fetch). */
export function privacyConsent(): PrivacyConsent {
  installCacheRules();
  const data = queryClient.getQueryData<GetPrivacySettingsResponse>(privacySettingsKey());
  return data ? consentOf(data) : (consentAfterClear ?? DEFAULT_CONSENT);
}

/**
 * Whether the signed-in account's own settings are loaded, so privacyConsent()
 * is that account's answer. Not the default, and not the answer kept across a
 * sign-out: that one belongs to whoever signed out, and the next account to
 * sign in may have opted out. Until it is, a reader that would act on consent
 * (an event queued for replay) waits. A sign-out clears the cache, so a loaded
 * entry is always the current account's.
 */
export function privacyConsentKnown(): boolean {
  installCacheRules();
  return queryClient.getQueryData(privacySettingsKey()) !== undefined;
}

/** Call `listener` with the consent each time the entry's answer changes (a load, a write, a rollback). */
export function subscribePrivacyConsent(listener: (consent: PrivacyConsent) => void): () => void {
  installCacheRules();
  return queryClient.getQueryCache().subscribe((event) => {
    if (event.type !== "updated" || event.action.type !== "success" || !isPrivacyEntry(event.query)) return;
    const data = event.query.state.data as GetPrivacySettingsResponse | undefined;
    if (data) listener(consentOf(data));
  });
}

/**
 * Load the user's consent into the entry. Never throws: a backend that is
 * down keeps the previous answer (the app does not work then anyway). A
 * transport that is not configured yet is waited for, twice at most.
 */
export async function loadPrivacySettings(): Promise<void> {
  installCacheRules();
  for (let attempt = 0; ; attempt++) {
    try {
      // The not-ready wait below is the retry policy, not the query's ladder.
      await queryClient.fetchQuery(settingsServiceQueries.getPrivacySettings({}, { staleTime: 0, retry: false }));
      logger.info("[Privacy] Settings loaded successfully");
      return;
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      if (message.includes("not ready") && attempt < 2) {
        logger.warn("[Privacy] gRPC not ready, waiting for config and retrying...");
        try {
          await waitForConfig(5000);
          continue;
        } catch (configErr) {
          logger.error("[Privacy] Config wait failed:", configErr);
        }
      }
      logger.error("[Privacy] Failed to load privacy settings:", message);
      return;
    }
  }
}

/** The consent the Privacy switches show; the last known (or default) answer until the entry loads. */
export function usePrivacyConsent(): PrivacyConsent {
  installCacheRules();
  const q = useGetPrivacySettings({}, { select: consentOf });
  return q.data ?? privacyConsent();
}

/**
 * Flip a consent switch. It flips at once and flips back if the server
 * refuses (logged, as before — a privacy switch has no error surface). Being
 * the generated mutation, success refetches the entry.
 */
export function useUpdatePrivacySettings() {
  const qc = useQueryClient();
  const rpc = useUpdatePrivacySettingsRpc({
    onMutate: async (req) => {
      installCacheRules();
      await qc.cancelQueries({ queryKey: privacySettingsKey() });
      const previous =
        qc.getQueryData<GetPrivacySettingsResponse>(privacySettingsKey()) ??
        create(GetPrivacySettingsResponseSchema, privacyConsent());
      qc.setQueryData<GetPrivacySettingsResponse>(privacySettingsKey(), {
        ...previous,
        crashReportingEnabled: req.crashReportingEnabled ?? previous.crashReportingEnabled,
        analyticsEnabled: req.analyticsEnabled ?? previous.analyticsEnabled,
      });
      return { previous };
    },
    onError: (err, _req, context) => {
      logger.error("[Privacy] Failed to save privacy settings:", err);
      if (context) qc.setQueryData(privacySettingsKey(), context.previous);
      // `previous` may have been the default rather than an answer.
      void qc.invalidateQueries({ queryKey: privacySettingsKey() });
    },
  });
  return {
    isPending: rpc.isPending,
    setCrashReporting: (enabled: boolean) => rpc.mutate({ crashReportingEnabled: enabled }),
    setAnalytics: (enabled: boolean) => rpc.mutate({ analyticsEnabled: enabled }),
  };
}
