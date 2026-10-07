// Copyright (c) 2025 Reliant Labs

/**
 * PREVIEW: the only daemon-dependent surface, and the bootstrap that runs
 * through it.
 *
 * Three things are pinned here, and the third is the owner's acceptance
 * criterion for this whole slice:
 *
 *   1. With the daemon offline, PREVIEW ALONE says so, in one short literal
 *      line — and Live, on the same page, carries no banner at all. The
 *      asymmetry IS the design (§8.0): a daemon that is asleep must not make
 *      an environment look broken.
 *   2. Register sends the kind and shape FROM FORGE'S RENDER to
 *      EnsureEnvironment, from the browser, unaltered.
 *   3. THE ACCEPTANCE FLOW (briefing §6): a never-built env appears in Preview
 *      as "would be created" → Register → the daemon goes offline → Live shows
 *      "Declared, not built yet" and a secret can still be set.
 *
 * The daemon is mocked HERE (unlike the Live test, where touching it fails),
 * because Preview is the one surface that is supposed to use it.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import type { LiveEnv } from "@/services/forge/live";

const routeState: { env: string; tab?: string } = { env: "staging", tab: undefined };
const navigate = vi.fn();

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
  useParams: () => ({ env: routeState.env }),
  useSearch: () => ({ project: "proj-1", tab: routeState.tab }),
}));

vi.mock("@/store/projectStore", () => {
  const currentProject = {
    id: "proj-1",
    name: "barksocial",
    is_forge: true,
    forge_project_name: "hounders",
  };
  return {
    useProjectStore: (selector: (s: unknown) => unknown) =>
      selector({ currentProject, projects: [currentProject] }),
  };
});

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://127.0.0.1:8090",
  hasControlPlane: true,
}));

// ── The daemon. Offline by default; each test opts into an answer. ──

const OFFLINE = () => Promise.reject(new ConnectError("no daemon connected for user", Code.Unavailable));

/**
 * `forge env shape staging --json` — forge's own projection of the render,
 * the SAME one `forge env build` records. This fixture is the JSON contract
 * from briefing §6, and what Register must forward verbatim.
 */
const SHAPE_REPORT = {
  project: "hounders",
  env: "staging",
  kind: "self_managed",
  shape: {
    kind: "self_managed",
    workloads: [{ name: "api", runtime: "cluster", cluster: "prod-gke" }],
    secrets: [{ name: "DATABASE_URL", provider: "hosted", declared_by: ["api"] }],
    domains: ["staging.example.com"],
    clusters: ["prod-gke"],
    objects: [],
  },
  provenance: {
    repo: "github.com/reliant-labs/hounders",
    commit: "def5678abc123",
    branch: "feat-x",
    dirty: true,
    tree: "a1b2c3",
    forge_version: "v0.1.44",
  },
};

const getTopology = vi.fn(OFFLINE);
const getEnvShape = vi.fn(OFFLINE);
const getEnvStatus = vi.fn(OFFLINE);
const getAudit = vi.fn(OFFLINE);

vi.mock("@/api/forge-grpc", () => ({
  forgeGrpc: {
    getTopology: () => getTopology(),
    getEnvShape: () => getEnvShape(),
    getEnvStatus: () => getEnvStatus(),
    getAudit: () => getAudit(),
    verifyEnv: () => OFFLINE(),
    listSecrets: () => OFFLINE(),
    planPromote: () => OFFLINE(),
    applyPromote: () => OFFLINE(),
    planDeploy: () => OFFLINE(),
    startDeploy: () => OFFLINE(),
    getDeployStatus: () => OFFLINE(),
  },
}));

// ── The control plane ──

/** What Live holds. Mutated by Register, as the server would. */
let liveEnvs: LiveEnv[] = [];

const getLiveView = vi.fn(() => Promise.resolve(liveEnvs));
vi.mock("@/services/forge/live", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/forge/live")>()),
  getLiveView: () => getLiveView(),
}));

/** EnsureEnvironment, as called straight from the browser. */
const ensureEnvironment = vi.fn((input: { spec: Record<string, unknown> }) => {
  const spec = input.spec as { project: string; name: string; kind: number };
  // The server's behaviour that matters to the UI: the row now exists, with
  // the declaration it was given, and no promotion.
  liveEnvs = [
    {
      id: "cp-staging",
      name: spec.name,
      project: spec.project,
      kind: "self_managed",
      declaredShape: {
        kind: "self_managed",
        workloads: [{ name: "api", runtime: "cluster", cluster: "prod-gke" }],
        secrets: [{ name: "DATABASE_URL", provider: "hosted", declaredBy: ["api"] }],
        domains: ["staging.example.com"],
        clusters: ["prod-gke"],
      },
      declaredBy: {
        repo: "github.com/reliant-labs/hounders",
        commit: "def5678abc123",
        branch: "feat-x",
        tag: "",
        dirty: true,
        tree: "a1b2c3",
        forgeVersion: "v0.1.44",
        worktree: null,
      },
      declaredAt: "2026-10-02T09:00:00.000Z",
      release: "",
      releaseProvenance: null,
      promotedByActor: "",
      promotedByUserId: "",
      phase: "unspecified",
      provenance: "feat-x@def5678, unmerged, dirty",
      holds: [],
    },
  ];
  return Promise.resolve({ environment: { id: "cp-staging" } });
});

vi.mock("@/services/controlPlane/client", () => ({
  getControlPlaneClient: () => ({
    ensureEnvironment,
    getLiveView: () => Promise.resolve({ environments: [] }),
  }),
}));

const listSecrets = vi.fn(() => Promise.resolve([] as unknown[]));
const setSecret = vi.fn(() => Promise.resolve({ version: 1 }));
vi.mock("@/services/forge/secretStore", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/forge/secretStore")>()),
  listSecrets: () => listSecrets(),
  setSecret: (...args: unknown[]) => setSecret(...(args as [])),
  getSecretVersions: () => Promise.resolve({ name: "", versions: [] }),
}));

vi.mock("@/services/forge/cloudEnvs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/forge/cloudEnvs")>()),
  getEnvironmentStatus: () => Promise.resolve({ verdict: "unknown", workloads: [], currentPromotion: null }),
  listEnvironmentPromotions: () => Promise.resolve([]),
}));

import { ForgeEnvPage } from "../../EnvPage/ForgeEnvPage";
import { DAEMON_OFFLINE_COPY } from "../../EnvPage/DaemonNeeded";

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <ForgeEnvPage />
    </QueryClientProvider>
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  liveEnvs = [];
  routeState.env = "staging";
  routeState.tab = undefined;
  getTopology.mockImplementation(OFFLINE);
  getEnvShape.mockImplementation(OFFLINE);
  getEnvStatus.mockImplementation(OFFLINE);
  getAudit.mockImplementation(OFFLINE);
  getLiveView.mockImplementation(() => Promise.resolve(liveEnvs));
  listSecrets.mockResolvedValue([]);
});

describe("an environment Reliant has no record of, with the daemon offline", () => {
  it("says what the daemon-backed tab needs, and offers a retry-free page otherwise", async () => {
    routeState.tab = "changes";
    renderPage();

    // Longer than the default 1s: forgeRetry allows ONE retry on a transport
    // failure, so a daemon that is down is established on the second attempt.
    const offline = await screen.findByTestId("daemon-needed", {}, { timeout: 5000 });
    expect(offline).toHaveTextContent(DAEMON_OFFLINE_COPY);
    // It says the backend tabs are unaffected, rather than reading as if the
    // whole environment were in question.
    expect(offline).toHaveTextContent(/don't need it/i);
  });

  it("still renders Overview, saying it is not registered rather than 'not built yet'", async () => {
    renderPage();

    const panel = await screen.findByTestId("env-overview-unregistered", {}, { timeout: 5000 });
    expect(panel).toHaveTextContent(/not registered in reliant/i);
    expect(panel).not.toHaveTextContent(/not built yet/i);
  });
});

describe("Register", () => {
  beforeEach(() => {
    // The daemon is up for Preview: forge renders the checkout.
    getTopology.mockResolvedValue({ kind: "report", report: { project: "hounders", environments: [] } });
    getEnvShape.mockResolvedValue({ kind: "report", report: SHAPE_REPORT });
    getEnvStatus.mockResolvedValue({ kind: "report", report: {} });
    getAudit.mockResolvedValue({ kind: "report", report: {} });
  });

  it("shows an unregistered env as 'would be created' and registers forge's shape", async () => {
    const user = userEvent.setup();
    renderPage();

    const panel = await screen.findByTestId("register-env-panel", {}, { timeout: 5000 });
    expect(within(panel).getByText(/would be created/i)).toBeInTheDocument();
    // forge's kind, shown rather than asked for.
    await waitFor(() => expect(within(panel).getByText("Your cluster")).toBeInTheDocument());

    await user.click(await screen.findByTestId("register-staging", {}, { timeout: 5000 }));

    await waitFor(() => expect(ensureEnvironment).toHaveBeenCalled());
    const spec = ensureEnvironment.mock.calls[0]![0].spec as Record<string, unknown>;

    expect(spec.project).toBe("hounders");
    expect(spec.name).toBe("staging");
    // DEPLOY_ENVIRONMENT_KIND_SELF_MANAGED = 4. forge's answer, not a guess.
    expect(spec.kind).toBe(4);
    // The shape is forwarded EXACTLY as forge produced it — snake_case keys
    // and all. A reshape here could only make it disagree with what the next
    // `forge env build` records for the same env.
    expect(spec.shape).toEqual(SHAPE_REPORT.shape);
    expect(spec.declaredBy).toMatchObject({
      branch: "feat-x",
      commit: "def5678abc123",
      dirty: true,
      forgeVersion: "v0.1.44",
    });
  });

  it("refuses rather than guessing when forge did not state the kind", async () => {
    // An environment's kind is immutable, so a guess produces a row that can
    // only be abandoned. No button is offered at all.
    getEnvShape.mockResolvedValue({
      kind: "report",
      report: { ...SHAPE_REPORT, kind: "", shape: { ...SHAPE_REPORT.shape, kind: "" } },
    });
    renderPage();

    const reason = await screen.findByTestId("register-unavailable", {}, { timeout: 5000 });
    expect(reason).toHaveTextContent(/did not say how this environment runs/i);
    expect(screen.queryByTestId("register-staging")).not.toBeInTheDocument();
    expect(ensureEnvironment).not.toHaveBeenCalled();
  });

  it("refuses a render that carries secret values (F-13)", async () => {
    // Defence in depth: the browser must never TRANSMIT one, which is cheaper
    // to enforce before the request exists than to rely on the server alone.
    getEnvShape.mockResolvedValue({
      kind: "report",
      report: {
        ...SHAPE_REPORT,
        shape: {
          ...SHAPE_REPORT.shape,
          secrets: [{ name: "DATABASE_URL", provider: "hosted", value: "postgres://…" }],
        },
      },
    });
    renderPage();

    expect(await screen.findByTestId("register-unavailable", {}, { timeout: 5000 })).toHaveTextContent(
      /carries secret values/i
    );
    expect(ensureEnvironment).not.toHaveBeenCalled();
  });
});

/**
 * THE OWNER'S ACCEPTANCE FLOW, end to end over mocked transports
 * (briefing §6). The real build step is verified by the orchestrator against a
 * live C-LIVE stack; what is provable here is the part that was broken: that
 * registering an environment makes it fully usable from Live with no daemon.
 */
describe("acceptance: unregistered → Register → daemon offline → Overview and Secrets work", () => {
  // A longer budget than the 5s default: this walks four states, and two of
  // them wait out forgeRetry's one retry against a daemon that is down.
  it("runs the whole flow", async () => {
    const user = userEvent.setup();

    // ── 1. An unregistered env's Overview offers "would be created". ──
    getTopology.mockResolvedValue({ kind: "report", report: { project: "hounders", environments: [] } });
    getEnvShape.mockResolvedValue({ kind: "report", report: SHAPE_REPORT });
    getEnvStatus.mockResolvedValue({ kind: "report", report: {} });
    getAudit.mockResolvedValue({ kind: "report", report: {} });

    const first = renderPage();
    const panel = await screen.findByTestId("register-env-panel", {}, { timeout: 5000 });
    expect(within(panel).getByText(/would be created/i)).toBeInTheDocument();

    // ── 2. Register it. ──
    await user.click(await screen.findByTestId("register-staging", {}, { timeout: 5000 }));
    await waitFor(() => expect(ensureEnvironment).toHaveBeenCalledTimes(1));
    await screen.findByTestId("register-done", {}, { timeout: 5000 });
    first.unmount();

    // ── 3. Take the daemon offline. ──
    getTopology.mockImplementation(OFFLINE);
    getEnvShape.mockImplementation(OFFLINE);
    getEnvStatus.mockImplementation(OFFLINE);
    getAudit.mockImplementation(OFFLINE);

    // ── 4. Overview shows "Declared, not built yet"… ──
    routeState.tab = undefined;
    const second = renderPage();

    await screen.findByTestId("live-section", {}, { timeout: 5000 });
    expect(screen.getByTestId("live-declared-not-built")).toHaveTextContent(
      /declared, not built yet/i
    );
    // With the provenance the render claimed about itself.
    expect(screen.getByTestId("live-provenance")).toHaveTextContent("feat-x@def5678, unmerged, dirty");
    // And no banner: the daemon being offline is not this page's problem.
    expect(screen.queryByTestId("forge-daemon-offline")).not.toBeInTheDocument();

    second.unmount();

    // ── …and on Secrets a secret can be set, with the daemon still offline. ──
    // DATABASE_URL is a row because the DECLARED SHAPE names it — the row
    // that used to need `forge.secret_list` on the daemon.
    routeState.tab = "secrets";
    renderPage();
    await screen.findByText("DATABASE_URL", {}, { timeout: 5000 });

    await user.click(screen.getByTestId("add-secret"));
    await user.type(await screen.findByLabelText("Name"), "DATABASE_URL_2");
    await user.type(screen.getByLabelText("Value"), "postgres://localhost");
    await user.click(screen.getByTestId("set-secret-submit"));

    await waitFor(() => expect(setSecret).toHaveBeenCalled());
    expect(setSecret.mock.calls[0]?.[0]).toMatchObject({
      environmentId: "cp-staging",
      name: "DATABASE_URL_2",
    });
  }, 30000);
});
