// Copyright (c) 2025 Reliant Labs

/**
 * A model-level ObservabilityApi fake for a component or hook test, served the
 * way the app reads it: through ObservabilityApiProvider for the reads that
 * still go through the context, AND on the wire for ObservabilityService's
 * reads on generated hooks (useServiceOverview, useWorkloads, useTrace, …)
 * and DeliveryInsightsService's and ActivityService's (useReleases,
 * useDoraMetrics, useActivity), which call their QueryClient's transport and
 * never the context.
 *
 * The wire half is the shop fixture pack's handlers (fixtures/shop/handlers/
 * observability.ts, delivery.ts, activity.ts) over the fake: each decodes the request into the model,
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

import { ActivityService } from "@/gen/controlplane/services/activity/v1/activity_pb";
import { DeliveryInsightsService } from "@/gen/controlplane/services/delivery_insights/v1/delivery_insights_pb";
import { ObservabilityService } from "@/gen/controlplane/services/observability/v1/observability_pb";
import { activityHandlers } from "@/fixtures/shop/handlers/activity";
import { observabilityHandlers } from "@/fixtures/shop/handlers/observability";
import { transport as appTransport } from "@/lib/connect";
import { createTransportScope, TransportScopeProvider, type TransportScope } from "@/lib/transportScope";
import { mockTransport } from "@/lib/test-utils";
import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

/**
 * The fake's ObservabilityService reads on the wire; a relative window
 * resolves against `now`. Every other rpc (a project list, a deploy read, the
 * other observability services, still read through the context) goes to the
 * app's transport, as it would outside this scope, so a test's
 * setTransport(...) still answers it.
 */
export function observabilityFixtureTransport(api: () => ObservabilityApi, now: () => number = Date.now): Transport {
  const handlersOf: Record<string, () => unknown> = {
    // observabilityHandlers carries DeliveryInsightsService's too (handlers/delivery.ts).
    [ObservabilityService.typeName]: () => observabilityHandlers(api(), { now }),
    [DeliveryInsightsService.typeName]: () => observabilityHandlers(api(), { now }),
    [ActivityService.typeName]: () => activityHandlers(api()),
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
