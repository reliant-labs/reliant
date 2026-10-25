// Copyright (c) 2025 Reliant Labs

/**
 * THE DAEMON TAB ASKS FOR ITSELF ONCE A MACHINE IS KNOWN TO BE UP.
 *
 * The Running tab (a local env's `forge env up` stack) used to wait for
 * GetTopology — a whole `forge env status` run on the user's machine, 26–30s on
 * a real project — before asking its own question. When it failed (prod
 * 2026-10-15: the gateway cut it at 15s with a 504) the tab said "your daemon
 * isn't answering" about a machine that was answering the whole time.
 *
 * The registry's daemon list knows a machine is up in milliseconds. These
 * tests hold topology pending, or fail it, and assert what the tab does with
 * an awake machine, and with none. The per-branch Changes and Checks tabs are
 * gone: their links must land on the default tab and ask the daemon nothing.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import type { LiveEnv } from "@/services/forge/live";

// ── The machine: topology is decided per test; the tabs' own reads answer. ──

const getTopology = vi.fn();
const getEnvStatus = vi.fn(() => Promise.resolve({ kind: "report", report: {}, meta: {} }));

vi.mock("@/api/forge-grpc", () => ({
  forgeGrpc: {
    getTopology: (...args: unknown[]) => getTopology(...args),
    getEnvStatus: () => getEnvStatus(),
    verifyEnv: vi.fn(),
  },
}));

// ── The registry's daemon list (answered at the transport, below). ──
const fetchDaemonList = vi.fn();

const routeState: { tab: string } = { tab: "running" };
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
  useParams: () => ({ env: "dev" }),
  useSearch: () => ({ tab: routeState.tab }),
}));

// What the console layout says the page is about (Console/consoleContext.ts).
vi.mock("@/components/Forge/Console/consoleContext", () => ({
  useForgeConsole: () => ({
    forgeProject: "hounders",
    projectId: "proj-1",
    env: "prod",
    href: (page: { kind: string }) => `/forge/p/hounders/${page.kind}`,
  }),
}));

vi.mock("@/store/projectStore", () => {
  const currentProject = { id: "proj-1", name: "barksocial", is_forge: true, forge_project_name: "hounders" };
  return {
    useProjectStore: (selector: (s: unknown) => unknown) => selector({ currentProject, projects: [currentProject] }),
  };
});

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://127.0.0.1:8090",
  hasControlPlane: true,
}));

const DEV: LiveEnv = {
  id: "cp-dev",
  name: "dev",
  project: "hounders",
  kind: "local",
  declaredShape: null,
  declaredBy: null,
  release: "v12",
  releaseProvenance: null,
  promotedByActor: "ci",
  promotedByUserId: "",
  phase: "succeeded",
  observed: { state: "not-reported" },
  drift: { state: "not-reported" },
  driftDetail: "",
  provenance: "",
  holds: [],
};

vi.mock("@/services/forge/live", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/forge/live")>()),
  getLiveView: () => Promise.resolve([DEV]),
  listEnvironmentConvergences: () => Promise.resolve([]),
}));

vi.mock("@/services/forge/cloudEnvs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/forge/cloudEnvs")>()),
  getEnvironmentStatus: () =>
    Promise.resolve({ verdict: "converged", workloads: [], currentPromotion: null, observedAt: "" }),
  listEnvironmentPromotions: () => Promise.resolve([]),
}));

import { ForgeEnvPage } from "../ForgeEnvPage";
import { setTransport } from "@/lib/connect";
import { daemonRegistryTransport } from "@/test/daemonRegistry";

// The daemon list is read through connectClient(DaemonRegistryService).
beforeEach(() => setTransport(daemonRegistryTransport({ listDaemons: async () => ({ daemons: await fetchDaemonList() }) })));
afterEach(() => setTransport(null));

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <ForgeEnvPage />
    </QueryClientProvider>
  );
}

const AWAKE = [{ daemonId: "d1", status: DaemonStatus.ACTIVE }];

beforeEach(() => {
  vi.clearAllMocks();
  routeState.tab = "running";
  // Topology still running — the 26–30s forge run nobody should wait on.
  getTopology.mockImplementation(() => new Promise(() => {}));
  fetchDaemonList.mockResolvedValue(AWAKE);
});

describe("the Running tab", () => {
  it("asks for the env status while topology is still running, once the registry shows an awake machine", async () => {
    renderPage();
    await waitFor(() => expect(getEnvStatus).toHaveBeenCalledTimes(1));
    expect(screen.queryByTestId("daemon-needed")).not.toBeInTheDocument();
  });

  it("asks for the env status after topology FAILED, while the machine is awake", async () => {
    // prod 2026-10-15: the gateway cut topology at 15s.
    getTopology.mockRejectedValue(new ConnectError("upstream request timeout", Code.Unavailable));
    renderPage();
    await waitFor(() => expect(getEnvStatus).toHaveBeenCalledTimes(1));
    expect(screen.queryByTestId("daemon-needed")).not.toBeInTheDocument();
  });

  it("waits for topology when no machine is awake", async () => {
    getEnvStatus.mockImplementation(() => new Promise(() => {}));
    fetchDaemonList.mockResolvedValue([{ daemonId: "d1", status: DaemonStatus.SUSPENDED }]);
    renderPage();
    await waitFor(() => expect(fetchDaemonList).toHaveBeenCalled());
    expect(await screen.findByTestId("running-loading")).toBeInTheDocument();
  });

  it("says the daemon isn't answering when topology failed and no machine is awake", async () => {
    getTopology.mockRejectedValue(new ConnectError("no daemon connected", Code.Unavailable));
    fetchDaemonList.mockResolvedValue([]);
    renderPage();
    // forgeRetry retries an Unavailable topology once (react-query's ~1s
    // default delay) before the page may conclude the daemon is gone.
    expect(await screen.findByTestId("daemon-needed", undefined, { timeout: 5_000 })).toBeInTheDocument();
  });
});

describe("the retired Changes and Checks links", () => {
  it.each(["changes", "checks", "preview"])("?tab=%s lands on the env's default tab", async (tab) => {
    routeState.tab = tab;
    renderPage();
    const page = await screen.findByTestId("forge-env-page");
    // The page settles on the default tab once it learns the env is local.
    await waitFor(() => expect(page).toHaveAttribute("data-tab", "running"));
    expect(screen.queryByTestId("changes-tab")).not.toBeInTheDocument();
    expect(screen.queryByTestId("checks-tab")).not.toBeInTheDocument();
  });
});
