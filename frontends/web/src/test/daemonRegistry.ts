/**
 * The one daemon list (hooks/daemon-registry-queries) in tests.
 *
 * Code reads DaemonRegistryService through `connectClient`, so a test answers
 * it at the transport, not by mocking a client module:
 *
 *   const listDaemons = vi.fn(async () => ({ daemons: [...] }));
 *   beforeEach(() => setTransport(daemonRegistryTransport({ listDaemons })));
 *   afterEach(() => setTransport(null));
 *
 * `seedDaemonList` puts an answer in the cache as a read would have left it
 * (the cache holds the ListDaemons response; readers select `.daemons`).
 */

import { create, type MessageInitShape } from "@bufbuild/protobuf";
import type { QueryClient } from "@tanstack/react-query";

import { ListDaemonsResponseSchema, type DaemonInfoSchema } from "@/gen/reliant/v1/daemon_registry_pb";
import { DAEMON_LIST_QUERY_KEY } from "@/hooks/daemon-registry-queries";
import { mockTransport, type MockHandlers } from "@/lib/test-utils";
import { serviceHandlers } from "./serviceTransport";

/**
 * A transport answering DaemonRegistryService from client-shaped fakes
 * (`{ listDaemons, clearMachineCaches, … }`), plus any other handlers the
 * test needs on the same transport.
 */
export function daemonRegistryTransport(
  fakes: Record<string, (request: never) => unknown>,
  others: MockHandlers = {},
) {
  return mockTransport({ ...serviceHandlers("DaemonRegistryService", fakes), ...others });
}

/** Seed the daemon list as a successful ListDaemons read would leave it. */
export function seedDaemonList(
  client: QueryClient,
  daemons: readonly MessageInitShape<typeof DaemonInfoSchema>[] = [],
): void {
  client.setQueryData(DAEMON_LIST_QUERY_KEY, create(ListDaemonsResponseSchema, { daemons: [...daemons] }));
}
