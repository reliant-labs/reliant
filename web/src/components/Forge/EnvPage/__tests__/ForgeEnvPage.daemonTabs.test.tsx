// Copyright (c) 2025 Reliant Labs

/**
 * THE DAEMON TABS ASK FOR THEMSELVES ONCE A MACHINE IS KNOWN TO BE UP.
 *
 * Changes and Checks used to wait for GetTopology — a whole `forge env status`
 * run on the user's machine, 26–30s on a real project — before asking their
 * own question. When it failed (prod 2026-10-15: the gateway cut it at 15s with
 * a 504) both tabs said "your daemon isn't answering" about a machine that was
 * answering the whole time, and never asked anything.
 *
 * The registry's daemon list knows a machine is up in milliseconds. These
 * tests hold topology pending, or fail it, and assert what the tab does with
 * an awake machine, and with none.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import type { LiveEnv } from "@/services/forge/live";

// ── The machine: topology is decided per test; the tabs' own reads answer. ──

const getTopology = vi.fn();
const listCheckouts = vi.fn(() => Promise.resolve({ kind: "report", report: { checkouts: [] }, meta: {} }));
const diffEnv = vi.fn(() => Promise.resolve({ kind: "report", report: { environments: [] }, meta: {} }));
const getAudit = vi.fn(() => Promise.resolve({ kind: "report", report: {}, meta: {} }));
const getEnvStatus = vi.fn(() => Promise.resolve({ kind: "report", report: {}, meta: {} }));

vi.mock("@/api/forge-grpc", () => ({
  forgeGrpc: {
    getTopology: (...args: unknown[]) => getTopology(...args),
    listCheckouts: () => listCheckouts(),
    diffEnv: () => diffEnv(),
    getAudit: () => getAudit(),
    getEnvStatus: () => getEnvStatus(),
    verifyEnv: vi.fn(),
  },
}));

// ── The registry's daemon list. ──
const fetchDaemonList = vi.fn();
vi.mock("@/hooks/daemonListQuery", () => ({
  DAEMON_LIST_QUERY_KEY: ["reliant", "daemonRegistry", "list"],
  fetchDaemonList: () => fetchDaemonList(),
}));

const routeState: { tab: string } = { tab: "changes" };
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
  useParams: () => ({ env: "prod" }),
  useSearch: () => ({ project: "proj-1", tab: routeState.tab }),
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

const PROD: LiveEnv = {
  id: "cp-prod",
  name: "prod",
  project: "hounders",
  kind: "persistent",
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
  getLiveView: () => Promise.resolve([PROD]),
  listEnvironmentConvergences: () => Promise.resolve([]),
}));

vi.mock("@/services/forge/cloudEnvs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/forge/cloudEnvs")>()),
  getEnvironmentStatus: () =>
    Promise.resolve({ verdict: "converged", workloads: [], currentPromotion: null, observedAt: "" }),
  listEnvironmentPromotions: () => Promise.resolve([]),
}));

import { ForgeEnvPage } from "../ForgeEnvPage";

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
  routeState.tab = "changes";
  // Topology still running — the 26–30s forge run nobody should wait on.
  getTopology.mockImplementation(() => new Promise(() => {}));
  fetchDaemonList.mockResolvedValue(AWAKE);
});

describe("the Changes tab", () => {
  it("asks for the diff while topology is still running, once the registry shows an awake machine", async () => {
    renderPage();
    await screen.findByTestId("changes-tab");
    await waitFor(() => expect(diffEnv).toHaveBeenCalledTimes(1));
    expect(listCheckouts).toHaveBeenCalled();
  });

  it("asks for the diff after topology FAILED, while the machine is awake", async () => {
    // prod 2026-10-15: the gateway cut topology at 15s.
    getTopology.mockRejectedValue(new ConnectError("upstream request timeout", Code.Unavailable));
    renderPage();
    await screen.findByTestId("changes-tab");
    await waitFor(() => expect(diffEnv).toHaveBeenCalledTimes(1));
    expect(screen.queryByTestId("daemon-needed")).not.toBeInTheDocument();
  });

  it("waits for topology when no machine is awake", async () => {
    fetchDaemonList.mockResolvedValue([{ daemonId: "d1", status: DaemonStatus.SUSPENDED }]);
    renderPage();
    await waitFor(() => expect(fetchDaemonList).toHaveBeenCalled());
    expect(await screen.findByTestId("changes-loading")).toBeInTheDocument();
    expect(diffEnv).not.toHaveBeenCalled();
  });

  it("says the daemon isn't answering when topology failed and no machine is awake", async () => {
    getTopology.mockRejectedValue(new ConnectError("no daemon connected", Code.Unavailable));
    fetchDaemonList.mockResolvedValue([]);
    renderPage();
    // forgeRetry retries an Unavailable topology once (react-query's ~1s
    // default delay) before the page may conclude the daemon is gone.
    expect(await screen.findByTestId("daemon-needed", undefined, { timeout: 5_000 })).toBeInTheDocument();
    expect(diffEnv).not.toHaveBeenCalled();
  });
});

describe("the Checks tab", () => {
  it("asks for the audit while topology is still running, once the registry shows an awake machine", async () => {
    routeState.tab = "checks";
    renderPage();
    await screen.findByTestId("checks-tab");
    await waitFor(() => expect(getAudit).toHaveBeenCalledTimes(1));
  });
});
