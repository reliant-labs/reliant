// Copyright (c) 2026 Reliant Labs
// Test-only Vite entry. No product route, RPC or authentication bypass: the
// views render typed fixture data seeded into the query cache, and the
// reliant usage RPC answers from a mock transport.
//
//   organization-usage.html?view=usage|billing|llm&case=data|unavailable|denied&dark=1
import React from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import "@/index.css";

import { organizationUsageKeys } from "@/hooks/organization-usage-queries";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import type { GetLLMUsageRequest } from "@/gen/reliant/v1/usage_pb";

import { LLMUsageView } from "../../LLMUsageView";
import { OrganizationBillingView } from "../../OrganizationBillingView";
import { OrganizationUsageView } from "../../OrganizationUsageView";
import { monthToDateRequest, usageWindow } from "../../organizationUsageModel";
import { answer } from "../llmUsageFixtures";
import { organizationCostFixture } from "../organizationCostFixtures";
import { NOW_MS, ORG, respond, respondUnavailable } from "../organizationUsageFixtures";

const params = new URLSearchParams(location.search);
const view = params.get("view") ?? "usage";
const scenario = params.get("case") ?? "data";
if (params.get("dark") === "1") document.documentElement.classList.add("dark");

// retryOnMount false: a seeded refusal stays a refusal instead of refetching from a live service.
const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, retryOnMount: false, refetchOnWindowFocus: false } } });
const span = usageWindow("30d", NOW_MS);
const requests = [monthToDateRequest(ORG, NOW_MS), ...(["category", "env", "model"] as const).map((groupBy) => ({ orgId: ORG, ...span, groupBy }))];
const denied = new ConnectError("denied", Code.PermissionDenied);
for (const req of requests) {
  const key = organizationUsageKeys.usage(req);
  if (scenario === "denied") void client.prefetchQuery({ queryKey: key, queryFn: () => Promise.reject(denied) });
  else client.setQueryData(key, scenario === "unavailable" ? respondUnavailable(req) : respond(req));
}
if (scenario === "denied") void client.prefetchQuery({ queryKey: organizationUsageKeys.costSummary(ORG), queryFn: () => Promise.reject(denied) });
else client.setQueryData(organizationUsageKeys.costSummary(ORG), organizationCostFixture());

setTransport(
  mockTransport({
    "UsageService.GetLLMUsage": scenario === "unavailable" ? { error: new ConnectError("not served", Code.Unimplemented) } : (req: unknown) => answer(req as GetLLMUsageRequest),
  })
);

const body = view === "billing" ? <OrganizationBillingView orgId={ORG} nowMs={NOW_MS} /> : view === "llm" ? <LLMUsageView orgId={ORG} nowMs={NOW_MS} /> : <OrganizationUsageView orgId={ORG} nowMs={NOW_MS} />;

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <QueryClientProvider client={client}>
      <main className="forge-ui min-h-screen bg-background">{body}</main>
    </QueryClientProvider>
  </React.StrictMode>
);
