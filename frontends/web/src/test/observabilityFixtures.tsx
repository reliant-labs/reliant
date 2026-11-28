// Copyright (c) 2025 Reliant Labs

/**
 * A model-level ObservabilityApi fake for a component or hook test, served the
 * way the app reads it: through ObservabilityApiProvider for the reads that
 * still go through the context, AND on the wire for ObservabilityService's
 * reads on generated hooks (useServiceOverview, useWorkloads, useTrace, …)
 * and DashboardService's and AlertingService's reads and writes, which
 * call their QueryClient's transport and never the context.
 *
 * The wire half is the shop fixture pack's handlers (fixtures/shop/handlers/
 * observability.ts) over the fake: each decodes the request into the model,
 * asks the fake, and encodes its answer, so the hook's real request builder
 * and `select` mapper run. A relative window resolves against one fixed
 * instant per scope: the clock when the scope is made (a test that fakes
 * Date.now before rendering gets its own instant), never re-read per call.
 *
 * The test's QueryClient becomes a TransportScope bound to that transport. A
 * QueryClient binds one transport for good, so the transport reads the
 * newest `api` given for its client at call time.
 *
 *   <QueryClientProvider client={client}>
 *     <ObservabilityFixtures api={createFixtureApi()}>{ui}</ObservabilityFixtures>
 *   </QueryClientProvider>
 */

import { useMemo, type ReactNode } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import type { Transport } from "@connectrpc/connect";

import { observabilityHandlers } from "@/fixtures/shop/handlers/observability";
import type { PackClock } from "@/fixtures/shop/handlers/wire";
import { transport as appTransport } from "@/lib/connect";
import { createTransportScope, TransportScopeProvider, type TransportScope } from "@/lib/transportScope";
import { mockTransport, type MockHandlers } from "@/lib/test-utils";
import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

/**
 * The fake on the wire: every procedure the observability handlers serve
 * (ObservabilityService's, and the other observability services' the shop
 * pack answers: alerting, delivery, issues, rum, dashboards, user, billing)
 * goes to them, over the fake; a relative window (or an alert history's
 * lookback) resolves against `clock`.
 * A procedure they do not serve (a project list, a deploy read) goes to the
 * app's transport, as it would outside this scope, so a test's
 * setTransport(...) answers it.
 */
export function observabilityFixtureTransport(api: () => ObservabilityApi, clock: PackClock): Transport {
  const pick = (method: { parent: { typeName: string }; name: string }): Transport => {
    // Typed per rpc; the mock transport hands each the decoded request message of its own rpc.
    const handlers = observabilityHandlers(api(), { clock }) as unknown as MockHandlers;
    return `${method.parent.typeName}/${method.name}` in handlers ? mockTransport(handlers) : appTransport;
  };
  return {
    unary: (method, ...rest) => pick(method).unary(method, ...rest),
    stream: (method, ...rest) => pick(method).stream(method, ...rest),
  };
}

const scopes = new WeakMap<QueryClient, { scope: TransportScope; api: ObservabilityApi }>();

function fixtureScope(client: QueryClient, api: ObservabilityApi): TransportScope {
  let entry = scopes.get(client);
  if (!entry) {
    const holder = { api } as { scope: TransportScope; api: ObservabilityApi };
    // One instant for the scope's life: the fakes lay their data out around the clock the test runs at.
    const at = Date.now();
    holder.scope = createTransportScope({ id: "observability-fixtures", transport: observabilityFixtureTransport(() => holder.api, at), queryClient: client });
    scopes.set(client, holder);
    entry = holder;
  }
  entry.api = api;
  return entry.scope;
}

export function ObservabilityFixtures({ api, children }: { api: ObservabilityApi; children?: ReactNode }) {
  const client = useQueryClient();
  const scope = useMemo(() => fixtureScope(client, api), [client, api]);
  return (
    <TransportScopeProvider scope={scope}>
      <ObservabilityApiProvider api={api}>{children}</ObservabilityApiProvider>
    </TransportScopeProvider>
  );
}
