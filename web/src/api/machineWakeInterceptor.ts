/**
 * Records the machine the server woke for a request, so every surface says
 * "Waking up…" for it until it is up.
 *
 * A file or worktree request that finds its machine asleep wakes it on the
 * server and fails with a `DaemonWaking` detail (see `wakingDaemonId`). The
 * surfaces that wait on the machine — the file tree, the editor, the changes
 * panel, Settings → Machines — read the wake from one module-level record
 * (`lib/machineWake`), so recording it here, once, in the transport every
 * request goes through, reaches all of them without any surface having to
 * inspect error details itself.
 *
 * The error is RE-THROWN. The request did not run; the caller's own wait and
 * retry handling (`useDaemonWait`, `sendWithDaemonWait`) takes it from here.
 */

import type { Interceptor } from "@connectrpc/connect";

import { wakingDaemonId } from "../lib/daemon-errors";
import { markWaking } from "../lib/machineWake";

export const machineWakeInterceptor: Interceptor = (next) => async (req) => {
  try {
    return await next(req);
  } catch (error) {
    const daemonId = wakingDaemonId(error);
    if (daemonId) markWaking(daemonId);
    throw error;
  }
};
