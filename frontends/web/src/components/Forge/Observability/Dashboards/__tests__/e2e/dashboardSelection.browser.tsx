// Copyright (c) 2026 Reliant Labs
// Test-only entry mounting the real selection and detail surfaces with fixture APIs.
import { useState } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import "@/index.css";
import { ObservabilityApiProvider } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { DashboardRef, DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";
import { createFixtureApi, FIXTURE_PROJECT } from "../../../__fixtures__/fixtureApi";
import { DashboardView } from "../../DashboardView";
import { DashboardsList } from "../../DashboardsList";
import { organizationCostFixture } from "../organizationCostFixtures";

const summary = organizationCostFixture();
const builtin: DashboardSummary = { ref: { kind: "builtin", projectId: "", key: "org-costs" }, source: "builtin", viewKind: "organization-costs", title: "Organization costs", description: "Canonical organization accounting", readOnly: true, starred: false, lastViewedAtMs: null, panelCount: 0, previewPanels: [], release: null, workspace: null, category: "organization" };
const api = createFixtureApi();
api.listDashboards = async () => ({ dashboards: [builtin], canWrite: false });
api.getDashboard = async () => ({ summary: builtin, spec: { id: "org-costs", title: builtin.title, filters: [], panels: [] }, version: 0, canWrite: false });
api.recordDashboardView = async () => undefined;
api.currentOrgId = async () => summary.orgId;
const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false } } });
client.setQueryData(["organization-cost-summary", summary.orgId], summary);

function Selection() {
  const [selected, setSelected] = useState(false);
  const dashboard = (_ref: DashboardRef) => "#org-costs";
  const navigateTo = () => setSelected(true);
  return <main className="forge-ui min-h-screen bg-background p-4" onClick={(event) => {
    if ((event.target as HTMLElement).closest('a[href="#org-costs"]')) { event.preventDefault(); setSelected(true); }
  }}>
    {selected ? <DashboardView project={FIXTURE_PROJECT} dashRef={builtin.ref} env="unrecorded" environments={[]} search={{ service: "worker", range: "7d", rpcMethod: "Get" }} onSearch={() => undefined} onEnvChange={() => undefined} navigateTo={navigateTo}
      hrefs={{ dashboard, list: "#dashboards", release: () => "#release", trace: () => "#trace", workload: () => "#workload", logs: () => "#logs", traces: () => "#traces" }} />
      : <DashboardsList project={FIXTURE_PROJECT} env="all" environments={[]} search={{}} onSearch={() => undefined} hrefs={{ dashboard }} navigateTo={navigateTo} />}
  </main>;
}
createRoot(document.getElementById("root")!).render(<QueryClientProvider client={client}><ObservabilityApiProvider api={api}><Selection /></ObservabilityApiProvider></QueryClientProvider>);
