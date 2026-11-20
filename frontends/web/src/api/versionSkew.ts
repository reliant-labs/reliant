/**
 * This app called a reliant API route the server no longer serves.
 *
 * ELECTRON-B3: desktop 1.7.16 called `ChatService/CreateChat`, which the server
 * retired when StartChat became the one launch door (#391). The api-server's
 * router answered a bare 404, connect surfaced `[unimplemented] HTTP 404`, and
 * the user saw a failed send while Sentry recorded a crash. Nothing about that
 * request was a bug; the app was out of date, and the only fix is to update
 * (desktop) or reload onto the current bundle (web).
 *
 * So: tell the user once, in a modal that offers exactly that fix, and record
 * the skew once per session as a warning — the procedure name is how we learn
 * which old builds are still out there.
 *
 * The same modal, forced, is the answer to a server that REQUIRES a newer
 * build (noteUpgradeRequired, below). A 404 is still how a build from before
 * the `Reliant-Client` header is pushed: the server's legacy lever answers its
 * header-less calls with one, and this skew prompt opens.
 */

import { reportFailure } from "../lib/errorReporting";
import { logger } from "../lib/logger";

let _modalShown = false;
const _reported = new Set<string>();

export function noteVersionSkew(serviceTypeName: string, methodName: string): void {
  const procedure = `${serviceTypeName}/${methodName}`;
  if (!_reported.has(procedure)) {
    _reported.add(procedure);
    reportFailure(`version-skew: ${procedure} is not served`, {
      grpc_service: serviceTypeName,
      grpc_method: methodName,
    });
  }
  if (_modalShown) return;
  _modalShown = true;
  void (async () => {
    try {
      // Lazy: modalStore → components → transport → here would be a cycle.
      const { useModalStore } = await import("../store/modalStore");
      const store = useModalStore.getState();
      // Never stack over a modal the user is already dealing with.
      if (store.activeModal && store.activeModal !== "app-out-of-date") return;
      store.openModal("app-out-of-date", { procedure });
    } catch (err) {
      logger.error("[versionSkew] failed to open the update prompt", err);
      _modalShown = false;
    }
  })();
}

/** Why the server requires a newer build: it refused a call, or it publishes a minimum above this one. */
export type UpgradeRequiredSource = "refused" | "served-minimum";

let _forcedShown = false;

/**
 * The server requires a newer build of this app. Unlike a skew this is not a
 * suggestion: the server refuses this build's calls (or will once it enforces
 * the minimum it publishes), so the prompt is forced. It cannot be dismissed,
 * it replaces whatever modal is up (that modal's actions would be refused
 * too), and nothing replaces it (store/modalStore.ts).
 */
export function noteUpgradeRequired(
  source: UpgradeRequiredSource,
  info: { minVersion?: string; upgradeUrl?: string } = {},
): void {
  if (_forcedShown) return;
  _forcedShown = true;
  reportFailure(`upgrade-required: ${source}`, {
    upgrade_source: source,
    min_version: info.minVersion ?? "",
  });
  void (async () => {
    try {
      // Lazy, for the same import cycle as noteVersionSkew.
      const { useModalStore } = await import("../store/modalStore");
      useModalStore.getState().openModal("app-out-of-date", {
        forced: true,
        minVersion: info.minVersion,
        upgradeUrl: info.upgradeUrl,
      });
    } catch (err) {
      logger.error("[versionSkew] failed to open the forced upgrade prompt", err);
      _forcedShown = false;
    }
  })();
}

/** Test seam. */
export function resetVersionSkewForTests(): void {
  _modalShown = false;
  _forcedShown = false;
  _reported.clear();
}
