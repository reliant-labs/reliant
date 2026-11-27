// Copyright (c) 2026 Reliant Labs

/**
 * The console's observability pages read from the nearest TransportScope
 * (lib/transportScope.tsx), not the app's transport: overview, traces, logs,
 * issues, alerts and dashboards render the shop pack's data under a scope over
 * `createShopTransport()`, while the module transport records every call and
 * refuses it.
 *
 * The family is the observability reads (services/controlPlane/observability:
 * the ObservabilityApi and DataBrowserApi defaults, the billing and cost
 * hooks): none may reach the module transport. Nor does the app transport's
 * observability read stage (obsInterceptors.ts) reach them: the sample app's
 * reads spend none of the person's server slots or quota, so the person's
 * exhausted quota never refuses them. Reads outside the family
 * (forge's, the generated hooks') move onto the scope in their own PRs, and
 * P4.3's enumeration test holds the whole tour to zero; the app-wide reads a
 * page makes in any scope (the person's org list, their own user) are answered
 * here by a second pack, so the pages have an org to read in.
 */

import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { Code, ConnectError, type Transport } from "@connectrpc/connect";

vi.mock("@/services/controlPlane/config", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/config")>()),
  CONTROL_PLANE_API_URL: "https://cp.test",
  hasControlPlane: true,
}));
vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("./obsMocks")).obsMocks.virtual());
vi.mock("@tanstack/react-router", async (original) => ({
  ...(await original<typeof import("@tanstack/react-router")>()),
  useParams: () => ({}),
  useSearch: () => ({}),
  useNavigate: () => vi.fn(),
}));
// The dashboards page reads its project and environments from the console (forge's reads, P4.2b).
vi.mock("../../Console/consoleContext", async (original) => ({
  ...(await original<typeof import("../../Console/consoleContext")>()),
  useForgeConsole: () => ({
    forgeProject: "shop",
    env: "prod",
    setEnv: vi.fn(),
    href: () => "/dashboard",
    roster: { envs: [{ id: "env-prod", name: "prod" }], isLoading: false, resolvingName: false, live: {} },
  }),
}));

import { createShopTransport } from "@/fixtures/shop";
import { setTransport, transport as appTransport } from "@/lib/connect";
import { queryClient as appQueryClient } from "@/lib/query-client";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";
import { controlPlaneDataBrowserApi, dataBrowserApiOf } from "@/services/controlPlane/observability/DataBrowserApiContext";
import { controlPlaneObservabilityApi, observabilityApiOf } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { ObsQuotaProvider } from "@/services/controlPlane/observability/ObsQuotaContext";
import { obsQuotaStore, resetObsQuotaStore } from "@/services/controlPlane/observability/quotaStore";

import { AskAgentModeProvider } from "../AskReliant/askAgentMode";
import { DashboardsPage } from "../Dashboards/DashboardsPages";
import ObservabilityPanel from "../ObservabilityPanel";
import type { ObsView } from "../obsSearch";
import { PROJECT_PLACE } from "./obsTestUtils";

/** The services whose reads this family owns: none may reach the module transport. */
const FAMILY = new Set([
  "controlplane.v1.ObservabilityService",
  "controlplane.v1.IssuesService",
  "controlplane.v1.RumService",
  "controlplane.v1.AlertingService",
  "controlplane.v1.DashboardService",
  "controlplane.v1.DeliveryInsightsService",
  "controlplane.v1.AppDatabaseService",
  "controlplane.v1.ActivityService",
  "controlplane.v1.ObservabilityBillingService",
  "controlplane.v1.BillingService",
]);

/** The app-wide reads a page makes whatever its scope: answered from the person's own data (here, another pack). */
const APP_WIDE = new Set(["controlplane.v1.OrganizationService/ListMyOrganizations", "controlplane.v1.UserService/GetCurrentUser"]);

type Recorded = Transport & { calls: string[] };

function recording(name: (method: { parent: { typeName: string }; name: string }) => string, answer: (key: string) => Transport | null): Recorded {
  const calls: string[] = [];
  const refuse = (key: string) => {
    throw new ConnectError(`${key} reached the module transport`, Code.FailedPrecondition);
  };
  return {
    calls,
    unary: (method, ...rest) => {
      const key = name(method);
      calls.push(key);
      const to = answer(key);
      return to ? to.unary(method, ...rest) : Promise.reject(refuse(key));
    },
    stream: (method, ...rest) => {
      const key = name(method);
      calls.push(key);
      const to = answer(key);
      return to ? to.stream(method, ...rest) : Promise.reject(refuse(key));
    },
  } as Recorded;
}

const keyOf = (method: { parent: { typeName: string }; name: string }) => `${method.parent.typeName}/${method.name}`;

let live: Recorded;
let scoped: Recorded;
let restore: () => void;

beforeAll(async () => {
  // Load the page chunks once, so the first test does not race a cold transform.
  await import("../ObservabilityPanel");
  await import("../Dashboards/DashboardsPages");
}, 120_000);

beforeEach(() => {
  const person = createShopTransport();
  live = recording(keyOf, (key) => (APP_WIDE.has(key) ? person : null));
  const shop = createShopTransport();
  scoped = recording(keyOf, () => shop);
  restore = setTransport(live);
  appQueryClient.clear();
});

afterEach(() => {
  restore();
  appQueryClient.clear();
  resetObsQuotaStore();
});

function renderScoped(ui: React.ReactNode) {
  const scope = createTransportScope({ id: "sample", transport: scoped, readOnly: true });
  return render(
    <TransportScopeProvider scope={scope}>
      <AskAgentModeProvider mode="handoff" cli="forge" demo>
        <ObsQuotaProvider>{ui}</ObsQuotaProvider>
      </AskAgentModeProvider>
    </TransportScopeProvider>
  );
}

const panel = (view: ObsView) => <ObservabilityPanel place={PROJECT_PLACE} search={{ view, envs: ["prod"] }} navigate={() => undefined} />;

const familyCalls = (calls: string[]) => calls.filter((key) => FAMILY.has(key.slice(0, key.indexOf("/"))));

function expectFamilyOnScope(...rpcs: string[]) {
  expect(familyCalls(live.calls)).toEqual([]);
  for (const rpc of rpcs) expect(scoped.calls).toContain(rpc);
}

describe("the console's observability pages in a TransportScope", () => {
  it("overview", async () => {
    renderScoped(panel("overview"));
    const overview = await screen.findByTestId("obs-overview", {}, { timeout: 15_000 });
    expect(await screen.findAllByTestId(/^obs-service-/, {}, { timeout: 15_000 })).not.toHaveLength(0);
    expect(overview).toBeInTheDocument();
    expectFamilyOnScope("controlplane.v1.ObservabilityService/GetServiceOverview");
  }, 30_000);

  it("traces", async () => {
    renderScoped(panel("traces"));
    expect(await screen.findAllByTestId("obs-trace-row", {}, { timeout: 15_000 })).not.toHaveLength(0);
    expectFamilyOnScope("controlplane.v1.ObservabilityService/ListTraces");
  }, 30_000);

  it("logs", async () => {
    renderScoped(panel("logs"));
    expect(await screen.findAllByTestId("obs-log-row", {}, { timeout: 15_000 })).not.toHaveLength(0);
    expectFamilyOnScope("controlplane.v1.ObservabilityService/SearchLogs");
  }, 30_000);

  it("issues", async () => {
    renderScoped(panel("errors"));
    expect(await screen.findByTestId("obs-issue-open-iss-orders-deadline", {}, { timeout: 15_000 })).toBeInTheDocument();
    expectFamilyOnScope("controlplane.v1.IssuesService/ListIssues");
  }, 30_000);

  it("alerts", async () => {
    renderScoped(panel("alerts"));
    expect(await screen.findByTestId("obs-alerts-firing", {}, { timeout: 15_000 })).toBeInTheDocument();
    expectFamilyOnScope("controlplane.v1.AlertingService/ListAlerts");
  }, 30_000);

  it("logs, while the person's own org is out of query quota", async () => {
    obsQuotaStore().report({ limit: "queries", lane: "interactive", resetsAtMs: Date.now() + 3_600_000, soft: false });
    renderScoped(panel("logs"));
    expect(await screen.findAllByTestId("obs-log-row", {}, { timeout: 15_000 })).not.toHaveLength(0);
    expectFamilyOnScope("controlplane.v1.ObservabilityService/SearchLogs");
  }, 30_000);

  it("dashboards", async () => {
    renderScoped(<DashboardsPage />);
    expect(await screen.findByTestId("dashboards-gallery", {}, { timeout: 15_000 })).toBeInTheDocument();
    expectFamilyOnScope("controlplane.v1.DashboardService/ListDashboards");
  }, 30_000);
});

describe("the scoped context defaults", () => {
  it("are the app's constants on the app's transport, and one api per scope transport", () => {
    expect(observabilityApiOf(appTransport)).toBe(controlPlaneObservabilityApi);
    expect(dataBrowserApiOf(appTransport)).toBe(controlPlaneDataBrowserApi);
    const shop = createShopTransport();
    expect(observabilityApiOf(shop)).toBe(observabilityApiOf(shop));
    expect(observabilityApiOf(shop)).not.toBe(controlPlaneObservabilityApi);
    expect(dataBrowserApiOf(shop)).toBe(dataBrowserApiOf(shop));
    expect(Object.keys(observabilityApiOf(shop)).sort()).toEqual(Object.keys(controlPlaneObservabilityApi).sort());
  });
});
