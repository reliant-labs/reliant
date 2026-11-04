// Copyright (c) 2026 Reliant Labs
// Test-only Vite entry. No product route, RPC or authentication bypass.
import React from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import "@/index.css";
import { OrganizationCostsView } from "../../OrganizationCostsView";
import { organizationCostFixture } from "../organizationCostFixtures";

const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false } } });
const summary = organizationCostFixture();
client.setQueryData(["organization-cost-summary", summary.orgId], summary);
if (new URLSearchParams(location.search).get("dark") === "1") document.documentElement.classList.add("dark");
createRoot(document.getElementById("root")!).render(
  <React.StrictMode><QueryClientProvider client={client}><main className="min-h-screen bg-background"><OrganizationCostsView orgId={summary.orgId} /></main></QueryClientProvider></React.StrictMode>,
);
