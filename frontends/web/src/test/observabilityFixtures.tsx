// Copyright (c) 2025 Reliant Labs

/**
 * A model-level ObservabilityApi fake for a component or hook test, served the
 * way the app reads it: through ObservabilityApiProvider for the reads that
 * still go through the context, AND on the wire for ObservabilityService's
 * reads on generated hooks (useServiceOverview, useWorkloads, useTrace, …)
 * and AlertingService's reads and writes (useAlerts, useSilences, …), which
 * call their QueryClient's transport and never the context.
 *
 * The wire half is the shop fixture pack's handlers (fixtures/shop/handlers/
 * observability.ts, alerting.ts) over the fake: each decodes the request into the model,
 * asks the fake, and encodes its answer, so the hook's real request builder
 * and `select` mapper run. A relative window resolves against the wall clock
 * (`Date.now()`), as the fakes' data is laid out around it.
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

import { AlertingService } from "@/gen/controlplane/services/alerting/v1/alerting_pb";
import { ObservabilityService } from "@/gen/controlplane/services/observability/v1/observability_pb";
import { alertingHandlers } from "@/fixtures/shop/handlers/alerting";
import { observabilityHandlers } from "@/fixtures/shop/handlers/observability";
import { transport as appTransport } from "@/lib/connect";
import { createTransportScope, TransportScopeProvider, type TransportScope } from "@/lib/transportScope";
import { mockTransport } from "@/lib/test-utils";
import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

/**
 * The fake's ObservabilityService reads and AlertingService calls on the
 * wire; a relative window (or an alert history's lookback) resolves against `now`. Every other rpc (a project list, a deploy read, the
 * other observability services, still read through the context) goes to the
 * app's transport, as it would outside this scope, so a test's
 * setTransport(...) still answers it.
 */
export function observabilityFixtureTransport(api: () => ObservabilityApi, now: () => number = Date.now): Transport {
  const handlersOf: Record<string, () => unknown> = {
    [ObservabilityService.typeName]: () => observabilityHandlers(api(), { now }),
    // AlertingService's reads and writes, all on generated hooks (hooks/observability-queries.ts).
    [AlertingService.typeName]: () => alertingHandlers(api(), { now }),
  };
  const pick = (method: { parent: { typeName: string }; name: string }): Transport => {
    const of = handlersOf[method.parent.typeName];
    if (!of) return appTransport;
    const handlers = of() as Parameters<typeof mockTransport>[0];
    return handlers && `${method.parent.typeName}/${method.name}` in handlers ? mockTransport(handlers) : appTransport;
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
    holder.scope = createTransportScope({ id: "observability-fixtures", transport: observabilityFixtureTransport(() => holder.api), queryClient: client });
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
