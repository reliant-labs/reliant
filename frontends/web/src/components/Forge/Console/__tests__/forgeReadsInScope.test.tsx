// Copyright (c) 2026 Reliant Labs

/**
 * The console's forge reads take their client from the nearest TransportScope
 * (lib/transportScope.tsx). The environments, releases (list and detail),
 * workloads, domains and secrets pages render under a scope over the shop
 * fixture pack while the app's transport (lib/connect.ts) is a recording
 * transport that FAILS on use, and:
 *
 *  - each page shows the shop's rows, so its reads were answered by the scope;
 *  - the app's transport saw no call to the services this family reads
 *    (DeployService, DomainService, SecretStoreService): services/forge/*,
 *    hooks/forge-queries.ts and their callers.
 *
 * Other families (observability, the generated hooks) still read the app's
 * transport until their own PRs land; their calls fail here and are not this
 * test's business. The full enumeration of the tour's routes is P4.3's.
 */

import type { ReactNode } from "react";
import { useMemo } from "react";
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { Code, ConnectError, type Transport } from "@connectrpc/connect";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { DomainService } from "@/gen/controlplane/services/domain/v1/domain_pb";
import { SecretStoreService } from "@/gen/controlplane/services/secret_store/v1/secret_store_pb";
import { useForgeRoster } from "@/hooks/forge-queries";
import { setTransport } from "@/lib/connect";
import { createAppQueryClient } from "@/lib/query-client";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";
import { createShopTransport } from "@/fixtures/shop";
import { DEMO_PROJECT } from "@/fixtures/shop/handlers/scope";
import { resetDemoNowForTests } from "@/fixtures/shop/shopStory";

import { ForgeConsoleContext, type ForgeConsole } from "../consoleContext";
import type { ConsolePage } from "../paths";

const route = vi.hoisted(() => ({ params: {} as Record<string, string>, search: {} as Record<string, unknown> }));

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
  useParams: () => route.params,
  useSearch: () => route.search,
  useLocation: () => ({ pathname: "/forge/p/shop", search: {} }),
}));
vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, ...rest }: { href: string; children: ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));
vi.mock("uplot", async () => (await import("../../Observability/__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@/components/Forge/Observability/charts/UPlotChart", () => ({ UPlotChart: ({ label }: { label: string }) => <figure data-testid="chart">{label}</figure> }));
vi.mock("@/components/Forge/Observability/AskReliant/AskReliant", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/components/Forge/Observability/AskReliant/AskReliant")>()),
  useAskReliant: () => ({ ask: vi.fn(async () => true), busy: false, starting: null, dialog: null, mode: "handoff" }),
}));

const { EnvironmentsPage } = await import("../../Overview/EnvironmentsPage");
const { ReleasePage, ReleasesPage } = await import("../../Releases/ReleasesPages");
const { WorkloadPage } = await import("../../Workloads/WorkloadPages");
const { ForgeDomainsPage } = await import("../../Domains/ForgeDomainsPage");
const { SecretsPage } = await import("../SecretsPage");

/** The services whose console reads this family moved onto the scope. */
const FAMILY = new Set([DeployService.typeName, DomainService.typeName, SecretStoreService.typeName]);

/** Every call the app's transport was asked to make, as `<service>.<method>`. */
const liveCalls: string[] = [];

/** The app's transport for this file: records the call, then fails it, as a backend that must not be reached. */
const failingLive: Transport = {
  unary: async (method) => {
    liveCalls.push(`${method.parent.typeName}.${method.name}`);
    throw new ConnectError(`live transport used inside a scope: ${method.parent.typeName}.${method.name}`, Code.FailedPrecondition);
  },
  stream: async (method) => {
    liveCalls.push(`${method.parent.typeName}.${method.name}`);
    throw new ConnectError(`live transport used inside a scope: ${method.parent.typeName}.${method.name}`, Code.FailedPrecondition);
  },
};

const familyLiveCalls = () => liveCalls.filter((call) => FAMILY.has(call.slice(0, call.lastIndexOf("."))));

/** What ConsoleLayout provides, with the roster read the way it reads it: useForgeRoster, by the URL's forge project. */
function ConsoleHarness({ page, env, children }: { page: ConsolePage; env: string; children: ReactNode }) {
  const roster = useForgeRoster(null, DEMO_PROJECT);
  const value = useMemo<ForgeConsole>(
    () => ({ forgeProject: DEMO_PROJECT, projectId: null, page, env, setEnv: vi.fn(), roster, href: (target) => `/forge/p/${DEMO_PROJECT}/${target.kind}` }),
    [page, env, roster]
  );
  return <ForgeConsoleContext.Provider value={value}>{children}</ForgeConsoleContext.Provider>;
}

function renderInScope(page: ConsolePage, ui: ReactNode, { env = "prod", params = {}, search = {} }: { env?: string; params?: Record<string, string>; search?: Record<string, unknown> } = {}) {
  route.params = params;
  route.search = { env, ...search };
  // A scope per render, so no page answers from another's cache; the app's query defaults, as the tour's scope has.
  const scope = createTransportScope({ id: "sample", transport: createShopTransport(), queryClient: createAppQueryClient(), readOnly: true });
  return render(
    <TransportScopeProvider scope={scope}>
      <ConsoleHarness page={page} env={env}>
        {ui}
      </ConsoleHarness>
    </TransportScopeProvider>
  );
}

let restore = () => undefined as void;
beforeAll(() => {
  resetDemoNowForTests(Date.UTC(2026, 10, 10, 15, 0, 0));
  restore = setTransport(failingLive);
});
afterAll(() => {
  restore();
  resetDemoNowForTests();
});
beforeEach(() => {
  liveCalls.length = 0;
});

describe("the forge console's reads, inside a TransportScope over the shop pack", () => {
  it("Environments lists the shop's environments, with none of its reads sent live", async () => {
    renderInScope({ kind: "environments" }, <EnvironmentsPage />, { env: "all" });
    expect(await screen.findByTestId("env-row-prod")).toBeInTheDocument();
    expect(screen.getByTestId("env-row-staging")).toBeInTheDocument();
    expect(familyLiveCalls()).toEqual([]);
  });

  it("Releases lists the shop's releases, with none of its reads sent live", async () => {
    renderInScope({ kind: "releases" }, <ReleasesPage />);
    expect(await screen.findByText("v1.8.4")).toBeInTheDocument();
    expect(familyLiveCalls()).toEqual([]);
  });

  it("a release opens from the scope, with none of its reads sent live", async () => {
    renderInScope({ kind: "release", version: "v1.8.3" }, <ReleasePage />, { params: { version: "v1.8.3" } });
    expect((await screen.findAllByText(/v1\.8\.3/)).length).toBeGreaterThan(0);
    expect(await screen.findByText(/Retry the payment charge once on a timeout/)).toBeInTheDocument();
    expect(familyLiveCalls()).toEqual([]);
  });

  it("a workload reads its environment's hosted status from the scope, with none of its reads sent live", async () => {
    renderInScope({ kind: "workload", env: "prod", name: "api" }, <WorkloadPage />, { params: { env: "prod", name: "api" } });
    expect(await screen.findByTestId("workload-detail")).toBeInTheDocument();
    expect(familyLiveCalls()).toEqual([]);
  });

  it("Domains lists the shop's domains, with none of its reads sent live", async () => {
    renderInScope({ kind: "domains" }, <ForgeDomainsPage />);
    expect(await screen.findByText("shop.example.com")).toBeInTheDocument();
    expect(familyLiveCalls()).toEqual([]);
  });

  it("Secrets lists the shop's secrets by name, with none of its reads sent live", async () => {
    renderInScope({ kind: "secrets" }, <SecretsPage />, { env: "all" });
    const matrix = await screen.findByTestId("secrets-matrix");
    expect(await within(matrix).findByText("DATABASE_URL")).toBeInTheDocument();
    expect(familyLiveCalls()).toEqual([]);
  });
});
