// Copyright (c) 2026 Reliant Labs

/**
 * The console's Tests, Security, Storage and Identity pages read from the
 * nearest TransportScope (lib/transportScope.tsx), not the app's transport:
 * each renders the shop pack's data under a scope over `createShopTransport()`,
 * while the module transport records every call and refuses it.
 *
 * The family is these pages' reads (the Tests and Identity pages' generated
 * hooks, the VulnerabilityApi defaults, the storage browser's calls, which
 * resolve their client from the scope's QueryClient, and the coverage source
 * fetch), plus the observability billing panels' reads: none
 * may reach the module transport. Reads outside the family
 * (forge's, the generated hooks') move onto the scope in their own PRs, and
 * P4.3's enumeration test holds the whole tour to zero; the app-wide reads a
 * page makes in any scope (the person's org list, their own user) are
 * answered here by a second pack.
 */

import type { ReactNode } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { Code, ConnectError, type Transport } from "@connectrpc/connect";

vi.mock("@/services/controlPlane/config", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/config")>()),
  CONTROL_PLANE_API_URL: "https://cp.test",
  hasControlPlane: true,
}));
vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, ...rest }: { href: string; children: ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));
vi.mock("@/components/Forge/Observability/charts/UPlotChart", () => ({ UPlotChart: ({ label }: { label: string }) => <figure data-testid="chart">{label}</figure> }));
vi.mock("@/components/Forge/Observability/AskReliant/AskReliant", () => ({
  useAskReliant: () => ({ ask: vi.fn(async () => true), busy: false, starting: null, dialog: null, mode: "handoff" }),
}));

import { setReaderPrincipal } from "@/components/Forge/AuditedAccess/readerSession";
import { IdentityTab } from "@/components/Forge/EnvPage/IdentityTab";
import { ObsReadOnlyProvider } from "@/components/Forge/Observability/obsReadOnly";
import { UsageView } from "@/components/Forge/Observability/UsageView";
import { SbomsView } from "@/components/Forge/Security/SbomsPage";
import { VulnerabilitiesView } from "@/components/Forge/Security/VulnerabilitiesPage";
import { StorageBrowser } from "@/components/Forge/Storage/StorageBrowser";
import { TestsCoverageView } from "@/components/Forge/Tests/TestsCoveragePage";
import { TestsOverviewView } from "@/components/Forge/Tests/TestsOverviewPage";
import { TestsRunsView } from "@/components/Forge/Tests/TestsRunsPage";
import { setTransport, transport as appTransport } from "@/lib/connect";
import { queryClient as appQueryClient } from "@/lib/query-client";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";
import type { LiveEnv } from "@/services/forge/live";
import { controlPlaneVulnerabilityApi, vulnerabilityApiOf } from "@/services/controlPlane/vulnerabilities/VulnerabilityApiContext";
import { coverageSourceFetchOf } from "@/services/coverageSource/CoverageSourceContext";
import { fetchCoverageSource } from "@/services/coverageSource/coverageSource";

import { createShopTransport } from "..";
import { SHOP_CHARGE_PATH } from "../model/tests";
import { demoNow, resetDemoNowForTests } from "../shopStory";

/** The services whose reads this family owns: none may reach the module transport. */
const FAMILY = new Set([
  "controlplane.v1.TestsService",
  "controlplane.v1.VulnerabilityService",
  "controlplane.v1.StorageBrowserService",
  "controlplane.v1.IdentityService",
  "reliant.v1.CoverageSourceService",
]);

/** The app-wide reads a page makes whatever its scope: answered from the person's own data (here, another pack). */
const APP_WIDE = new Set(["controlplane.v1.OrganizationService/ListMyOrganizations", "controlplane.v1.UserService/GetCurrentUser"]);

type Recorded = Transport & { calls: string[] };

function recording(answer: (key: string) => Transport | null): Recorded {
  const calls: string[] = [];
  const refuse = (key: string) => new ConnectError(`${key} reached the module transport`, Code.FailedPrecondition);
  const keyOf = (method: { parent: { typeName: string }; name: string }) => `${method.parent.typeName}/${method.name}`;
  return {
    calls,
    unary: (method, ...rest) => {
      const key = keyOf(method);
      calls.push(key);
      const to = answer(key);
      return to ? to.unary(method, ...rest) : Promise.reject(refuse(key));
    },
    stream: (method, ...rest) => {
      const key = keyOf(method);
      calls.push(key);
      const to = answer(key);
      return to ? to.stream(method, ...rest) : Promise.reject(refuse(key));
    },
  } as Recorded;
}

const SCOPE = { project: "shop", environmentId: undefined };
const STAGING = { id: "env-staging", name: "staging" };
const PROD = { id: "env-prod", name: "prod" } as LiveEnv;

let live: Recorded;
let scoped: Recorded;
let restore: () => void;

beforeAll(() => resetDemoNowForTests(Date.UTC(2026, 10, 10, 15, 0, 0)));
afterAll(() => resetDemoNowForTests());

beforeEach(() => {
  const person = createShopTransport();
  live = recording((key) => (APP_WIDE.has(key) ? person : null));
  const shop = createShopTransport();
  scoped = recording(() => shop);
  restore = setTransport(live);
  appQueryClient.clear();
  setReaderPrincipal("fixture-reader", true);
});

afterEach(() => {
  restore();
  appQueryClient.clear();
});

function renderScoped(ui: ReactNode) {
  const scope = createTransportScope({ id: "sample", transport: scoped, readOnly: true });
  return render(
    <TransportScopeProvider scope={scope}>
      <ObsReadOnlyProvider value={{ readOnly: true, reason: "Read-only sample" }}>{ui}</ObsReadOnlyProvider>
    </TransportScopeProvider>
  );
}

const familyCalls = (calls: string[]) => calls.filter((key) => FAMILY.has(key.slice(0, key.indexOf("/"))));

function expectFamilyOnScope(...rpcs: string[]) {
  expect(familyCalls(live.calls)).toEqual([]);
  for (const rpc of rpcs) expect(scoped.calls).toContain(rpc);
}

describe("the console's Tests pages in a TransportScope", () => {
  it("summary", async () => {
    const hrefs = { flaky: "/f", runs: "/r", run: (id: string) => `/r/${id}`, coverage: "/c" };
    renderScoped(<TestsOverviewView scope={SCOPE} hrefs={hrefs} now={demoNow()} />);
    expect(await screen.findByTestId("tests-headline-coverage", {}, { timeout: 15_000 })).toHaveTextContent("73.0%");
    expectFamilyOnScope("controlplane.v1.TestsService/GetTestSummary");
  }, 30_000);

  it("runs", async () => {
    renderScoped(<TestsRunsView scope={SCOPE} search={{}} onSearch={() => undefined} runHref={(id) => `/r/${id}`} now={demoNow()} />);
    expect(await screen.findByTestId("tests-runs-list", {}, { timeout: 15_000 })).toBeInTheDocument();
    expectFamilyOnScope("controlplane.v1.TestsService/ListTestRuns");
  }, 30_000);

  it("coverage, with the file's source", async () => {
    renderScoped(<TestsCoverageView scope={SCOPE} search={{ path: SHOP_CHARGE_PATH }} hrefFor={() => "/c"} onSelect={() => undefined} />);
    expect(await screen.findByTestId("tests-coverage-source", {}, { timeout: 15_000 })).toHaveTextContent("chargeDeadline");
    expectFamilyOnScope("controlplane.v1.TestsService/GetCoverage", "controlplane.v1.TestsService/GetFileCoverage", "reliant.v1.CoverageSourceService/GetCoverageSource");
  }, 30_000);
});

describe("the console's Security pages in a TransportScope", () => {
  it("vulnerabilities", async () => {
    renderScoped(
      <VulnerabilitiesView
        scope={SCOPE}
        search={{}}
        onSearch={() => undefined}
        advisoryHref={(id) => `/forge/p/shop/security/vulnerabilities/${id}`}
        sbomsHref={() => "/forge/p/shop/security/sboms"}
      />
    );
    const list = await screen.findByTestId("vuln-list", {}, { timeout: 15_000 });
    expect(list).toHaveTextContent("golang.org/x/net");
    expectFamilyOnScope("controlplane.v1.VulnerabilityService/ListVulnerabilities");
  }, 30_000);

  it("SBOMs", async () => {
    renderScoped(<SbomsView scope={SCOPE} releaseHref={(v) => `/releases/${v}`} />);
    expect(await screen.findByTestId("sboms-release-v1.8.3", {}, { timeout: 15_000 })).toHaveTextContent("4 of 4 with an SBOM, 4 scanned");
    expectFamilyOnScope("controlplane.v1.VulnerabilityService/GetReleaseVulnerabilities");
  }, 30_000);
});

describe("observability billing in a TransportScope", () => {
  const BILLING = "observability_billing.v1.ObservabilityBillingService";
  const billingCalls = (calls: string[]) => calls.filter((key) => key.startsWith(`${BILLING}/`));

  it("usage, storage, limits and daily rows", async () => {
    renderScoped(<UsageView orgId="org-shop" />);
    expect(await screen.findByTestId("obs-billing-charged", {}, { timeout: 15_000 })).toBeInTheDocument();
    await waitFor(() => {
      for (const rpc of ["GetObservabilityUsage", "GetObservabilityStorage", "GetObservabilityLimits", "ListObservabilityUsage"]) expect(scoped.calls).toContain(`${BILLING}/${rpc}`);
    }, { timeout: 15_000 });
    expect(billingCalls(live.calls)).toEqual([]);
  }, 30_000);
});

describe("the console's data and identity pages in a TransportScope", () => {
  it("storage", async () => {
    renderScoped(<StorageBrowser env={STAGING} search={{ bucket: "uploads" }} onNavigate={() => undefined} href={() => "/storage"} />);
    expect(await screen.findByTestId("storage-listing", {}, { timeout: 15_000 })).toBeInTheDocument();
    expect(await screen.findByText("README.txt", {}, { timeout: 15_000 })).toBeInTheDocument();
    expectFamilyOnScope("controlplane.v1.StorageBrowserService/ListBrowsableBuckets", "controlplane.v1.StorageBrowserService/ListObjects");
  }, 30_000);

  it("auth (identity)", async () => {
    renderScoped(<IdentityTab envName="prod" lifecycle="deployed" live={PROD} liveLoading={false} />);
    expect(await screen.findByTestId("identity-issuer", {}, { timeout: 15_000 })).toHaveTextContent("https://accounts.shop.example.com");
    expect(await screen.findByText("Maya", {}, { timeout: 15_000 })).toBeInTheDocument();
    expectFamilyOnScope("controlplane.v1.IdentityService/GetIdentityStatus", "controlplane.v1.IdentityService/ListUsers");
  }, 30_000);
});

describe("the scoped context defaults", () => {
  it("are the app's constants on the app's transport, and one api per scope transport", () => {
    expect(vulnerabilityApiOf(appTransport)).toBe(controlPlaneVulnerabilityApi);
    expect(coverageSourceFetchOf(appTransport)).toBe(fetchCoverageSource);
    const shop = createShopTransport();
    for (const [of, app] of [
      [vulnerabilityApiOf, controlPlaneVulnerabilityApi],
    ] as const) {
      const api = (of as (t: Transport) => object)(shop);
      expect((of as (t: Transport) => object)(shop)).toBe(api);
      expect(api).not.toBe(app);
      expect(Object.keys(api).sort()).toEqual(Object.keys(app).sort());
    }
    expect(coverageSourceFetchOf(shop)).toBe(coverageSourceFetchOf(shop));
    expect(coverageSourceFetchOf(shop)).not.toBe(fetchCoverageSource);
  });
});
