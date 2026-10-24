/**
 * mockTransport handlers for one service from a client-shaped object of
 * fakes: `{ listPresetsForWorkflow: fn }` answers
 * `"PresetService.ListPresetsForWorkflow"` with `fn(request)`.
 *
 * For tests that used to mock a hand `api/<svc>-grpc.ts` client by method
 * name, now that the code under test reaches the service through
 * `connectClient` (the generated hooks' seam) and so through the transport.
 */

import type { MockHandlers } from "@/lib/test-utils";

export function serviceHandlers(
  service: string,
  fakes: Record<string, (request: never) => unknown>,
): MockHandlers {
  const handlers: MockHandlers = {};
  for (const [method, fake] of Object.entries(fakes)) {
    const rpc = method.charAt(0).toUpperCase() + method.slice(1);
    handlers[`${service}.${rpc}`] = (request: unknown) => fake(request as never);
  }
  return handlers;
}
