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
 */

import * as Sentry from "@sentry/react";
import { logger } from "../lib/logger";

let _modalShown = false;
const _reported = new Set<string>();

export function noteVersionSkew(serviceTypeName: string, methodName: string): void {
  const procedure = `${serviceTypeName}/${methodName}`;
  if (!_reported.has(procedure)) {
    _reported.add(procedure);
    Sentry.captureMessage(`version-skew: ${procedure} is not served`, {
      level: "warning",
      tags: { grpc_service: serviceTypeName, grpc_method: methodName },
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

/** Test seam. */
export function resetVersionSkewForTests(): void {
  _modalShown = false;
  _reported.clear();
}
