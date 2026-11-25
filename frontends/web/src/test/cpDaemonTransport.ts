/**
 * controlplane.v1.DaemonService at the transport, for tests of code that
 * reads or writes it through the generated hooks
 * (hooks/controlplane/daemon-service-hooks_gen.ts).
 *
 *   beforeEach(() => setTransport(mockTransport(cpDaemonHandlers({ getDaemon: () => ({ daemon }) }))));
 *   afterEach(() => setTransport(null));
 *
 * Keys are fully qualified: reliant.v1.DaemonService shares the short name,
 * so a `DaemonService.GetDaemon` key would answer both.
 */

import type { MockHandlers } from "@/lib/test-utils";

/** Handlers for controlplane.v1.DaemonService from client-shaped fakes (`{ getDaemon, listPortAccessRules, … }`). */
export function cpDaemonHandlers(fakes: Record<string, (request: never) => unknown>): MockHandlers {
  const handlers: MockHandlers = {};
  for (const [method, fake] of Object.entries(fakes)) {
    const rpc = method.charAt(0).toUpperCase() + method.slice(1);
    handlers[`controlplane.v1.DaemonService/${rpc}`] = (request: unknown) => fake(request as never);
  }
  return handlers;
}
