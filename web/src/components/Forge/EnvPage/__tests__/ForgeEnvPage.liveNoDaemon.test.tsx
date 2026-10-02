// Copyright (c) 2025 Reliant Labs

/**
 * THE OWNER'S TEST: LIVE MAKES ZERO DAEMON CALLS.
 *
 * Not "degrades gracefully", not "renders something useful" — ZERO. The daemon
 * transport is mocked to THROW on any call, and every daemon module is mocked
 * to throw on import-time access, so a Live code path that reaches for one
 * fails the test rather than quietly falling back.
 *
 * ── WHY THIS IS THE TEST THAT MATTERS ───────────────────────────────────────
 *
 * The env page used to call `forge.env_status` on the daemon, and forge then
 * called control-plane ListEnvironments with the DAEMON's token. Two failures
 * came out of that one indirection:
 *
 *   - a 403 on a page the user was perfectly entitled to see, because the
 *     daemon's token was not the user's;
 *   - a page describing a production environment going blank because a laptop
 *     went to sleep, while the control plane had been observing it the whole
 *     time.
 *
 * "It works when the daemon is up" cannot catch either. A test that asserts
 * the ABSENCE of a call can, and it keeps catching it: anyone who later adds
 * an innocuous-looking daemon "enrichment" to a Live section — the shape the
 * old code took — fails here, even though the page would still have rendered
 * fine on their machine with their daemon running.
 *
 * ── THE THREE ENVIRONMENTS ──────────────────────────────────────────────────
 *
 * Covered because they are three different states and each one previously had
 * a different way of going wrong:
 *
 *   prod     persistent, with a promotion. The ordinary case.
 *   staging  self_managed, declared and not built. The platform observes
 *            nothing there, so its Live answer is its declaration.
 *   fresh    never built — no control-plane row at all. A NORMAL state that
 *            must not render as an error.
 *
 * Plus the write: setting a secret from Live makes zero daemon calls either.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import type { CloudEnvStatus, CloudPromotion } from "@/services/forge/cloudEnvs";
import type { LiveEnv } from "@/services/forge/live";

// ── The daemon is not merely offline: TOUCHING IT IS A TEST FAILURE. ────────

/**
 * Every daemon call that happens, recorded with the method name so a failure
 * says WHICH call leaked rather than just that the count was wrong.
 */
const daemonCalls: string[] = [];

function daemonTripwire(method: string) {
  return (...args: unknown[]) => {
    daemonCalls.push(method);
    void args;
    throw new Error(
      `LIVE CALLED THE DAEMON: ${method}. Live must read the control plane only (design §8.0, O-14).`
    );
  };
}

vi.mock("@/api/forge-grpc", () => ({
  forgeGrpc: {
    getTopology: daemonTripwire("getTopology"),
    verifyEnv: daemonTripwire("verifyEnv"),
    listSecrets: daemonTripwire("listSecrets"),
    getAudit: daemonTripwire("getAudit"),
    getEnvStatus: daemonTripwire("getEnvStatus"),
    getEnvShape: daemonTripwire("getEnvShape"),
    planPromote: daemonTripwire("planPromote"),
    applyPromote: daemonTripwire("applyPromote"),
    planDeploy: daemonTripwire("planDeploy"),
    startDeploy: daemonTripwire("startDeploy"),
    getDeployStatus: daemonTripwire("getDeployStatus"),
  },
}));

// The generated daemon client itself, in case anything bypasses forge-grpc.
vi.mock("@/api/grpc-client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/grpc-client")>()),
  createForgeClient: daemonTripwire("createForgeClient"),
}));

const routeState: { env: string; tab?: string } = { env: "prod" };
const navigate = vi.fn();

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
  useParams: () => ({ env: routeState.env }),
  useSearch: () => ({ project: "proj-1", tab: routeState.tab }),
}));

// The Reliant project's display name and forge's name for it DIFFER, as they
// do for the real Bark Social checkout (forge.yaml `name: hounders`). So the
// page finding its environment at all proves the join key came from the
// project row — not from a daemon report, and not guessed from the display
// name.
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

// ── The control plane's answers ─────────────────────────────────────────────

function liveEnv(overrides: Partial<LiveEnv>): LiveEnv {
  return {
    id: "cp-x",
    name: "x",
    project: "hounders",
    kind: "persistent",
    declaredShape: null,
    declaredBy: null,
    release: "",
    releaseProvenance: null,
    promotedByActor: "",
    promotedByUserId: "",
    phase: "unspecified",
    provenance: "",
    ...overrides,
  };
}

const PROD: LiveEnv = liveEnv({
  id: "cp-prod",
  name: "prod",
  kind: "persistent",
  release: "v12",
  promotedAt: "2026-10-01T10:00:00.000Z",
  promotedByActor: "ci",
  phase: "succeeded",
  provenance: "v12 · images main@abc1234 · config feat-x@def5678, unmerged, dirty",
  declaredShape: {
    kind: "persistent",
    workloads: [{ name: "api", runtime: "hosted", cluster: "" }],
    secrets: [{ name: "STRIPE_WEBHOOK_SECRET", provider: "hosted", declaredBy: ["api"] }],
    domains: [],
    clusters: [],
  },
});

/** Declared from a Preview render, nothing promoted. Secrets must be settable. */
const STAGING: LiveEnv = liveEnv({
  id: "cp-staging",
  name: "staging",
  kind: "self_managed",
  release: "",
  declaredAt: "2026-10-02T09:00:00.000Z",
  provenance: "feat-x@def5678, unmerged",
  declaredShape: {
    kind: "self_managed",
    workloads: [{ name: "api", runtime: "cluster", cluster: "prod-gke" }],
    secrets: [{ name: "DATABASE_URL", provider: "hosted", declaredBy: ["api"] }],
    domains: [],
    clusters: ["prod-gke"],
  },
});

const getLiveView = vi.fn(() => Promise.resolve([PROD, STAGING]));
vi.mock("@/services/forge/live", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/forge/live")>()),
  getLiveView: (project: string) => getLiveView(project),
}));

const STATUS: CloudEnvStatus = {
  verdict: "converged",
  workloads: [
    {
      name: "api",
      tier: "backend",
      verdict: "converged",
      url: "https://wild-mongoose.reliantapps.dev",
    },
  ],
  currentPromotion: null,
  observedAt: "2026-10-01T10:05:00.000Z",
};

const PROMOTIONS: CloudPromotion[] = [
  {
    id: "promo-1",
    releaseVersion: "v12",
    kind: "promote",
    fromEnvironmentId: "",
    promotedByUserId: "",
    promotedByActor: "ci",
    note: "",
    createdAt: "2026-10-01T10:00:00.000Z",
    artifacts: [{ name: "api", digest: "sha256:aaaaaaaaaaaa" }],
  },
];

const getEnvironmentStatus = vi.fn(() => Promise.resolve(STATUS));
const listEnvironmentPromotions = vi.fn((environmentId: string) =>
  Promise.resolve(environmentId === "cp-prod" ? PROMOTIONS : [])
);
vi.mock("@/services/forge/cloudEnvs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/forge/cloudEnvs")>()),
  getEnvironmentStatus: (id: string) => getEnvironmentStatus(id),
  listEnvironmentPromotions: (id: string) => listEnvironmentPromotions(id),
}));

// The managed store IS the control plane, so the secrets half of Live is a
// control-plane read too.
const listSecrets = vi.fn(() =>
  Promise.resolve([{ name: "STRIPE_WEBHOOK_SECRET", currentVersion: 3, oldestVersion: 1, versions: [] }])
);
const setSecret = vi.fn(() => Promise.resolve({ version: 4 }));
vi.mock("@/services/forge/secretStore", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/forge/secretStore")>()),
  listSecrets: (...args: unknown[]) => listSecrets(...(args as [])),
  setSecret: (...args: unknown[]) => setSecret(...(args as [])),
  getSecretVersions: () => Promise.resolve({ name: "", versions: [] }),
}));

import { ForgeEnvPage } from "../ForgeEnvPage";
import { ForgeOverviewPage } from "../../Overview/ForgeOverviewPage";

function renderWithQuery(ui: React.ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

beforeEach(() => {
  daemonCalls.length = 0;
  routeState.env = "prod";
  routeState.tab = undefined;
  vi.clearAllMocks();
  getLiveView.mockResolvedValue([PROD, STAGING]);
  getEnvironmentStatus.mockResolvedValue(STATUS);
  listEnvironmentPromotions.mockImplementation((id: string) =>
    Promise.resolve(id === "cp-prod" ? PROMOTIONS : [])
  );
  listSecrets.mockResolvedValue([
    { name: "STRIPE_WEBHOOK_SECRET", currentVersion: 3, oldestVersion: 1, versions: [] },
  ]);
});

/** No banner, no error text, anywhere. The page is not "degraded". */
function expectNoBannerOrError() {
  expect(screen.queryByTestId("forge-daemon-offline")).not.toBeInTheDocument();
  expect(screen.queryByTestId("forge-cloud-unreachable")).not.toBeInTheDocument();
  expect(screen.queryByTestId("forge-cloud-no-access")).not.toBeInTheDocument();
  expect(screen.queryByText(/daemon/i)).not.toBeInTheDocument();
  expect(screen.queryByText(/could not be read/i)).not.toBeInTheDocument();
  expect(screen.queryByText(/something went wrong/i)).not.toBeInTheDocument();
}

describe("the Live tab makes zero daemon calls", () => {
  it("renders a persistent env with a promotion in full", async () => {
    routeState.env = "prod";
    renderWithQuery(<ForgeEnvPage />);

    await screen.findByTestId("live-section");

    // The header, its provenance, and the release.
    expect(screen.getByTestId("live-provenance")).toHaveTextContent(
      "v12 · images main@abc1234 · config feat-x@def5678, unmerged, dirty"
    );
    expect(screen.getByText("v12")).toBeInTheDocument();

    // Workloads, from the platform's observation.
    const workloads = await screen.findByTestId("live-workloads-observed");
    expect(within(workloads).getByText("api")).toBeInTheDocument();

    // The promotion ledger.
    const releases = await screen.findByTestId("live-releases");
    expect(within(releases).getByTestId("promotion-promo-1")).toBeInTheDocument();

    // Secrets, from the managed store joined with the DECLARED shape.
    await screen.findByText("STRIPE_WEBHOOK_SECRET");

    expectNoBannerOrError();
    expect(daemonCalls).toEqual([]);
  });

  it("renders a self_managed env that is declared and not built", async () => {
    routeState.env = "staging";
    renderWithQuery(<ForgeEnvPage />);

    await screen.findByTestId("live-section");

    // Its own state, said plainly and NOT as an error — once as the header's
    // badge and once as the sentence that says what to do about it.
    expect(screen.getByTestId("live-declared-not-built")).toHaveTextContent(
      /declared, not built yet/i
    );
    expect(screen.getAllByText(/declared, not built yet/i).length).toBeGreaterThan(0);

    // The platform places nothing there, so Live shows the DECLARATION and
    // says so rather than implying an observation.
    const declared = await screen.findByTestId("live-workloads-declared");
    expect(within(declared).getByText("api")).toBeInTheDocument();
    expect(within(declared).getByText(/not an observation of your cluster/i)).toBeInTheDocument();

    // Its declared secret is a row even though nothing has ever been set.
    await screen.findByText("DATABASE_URL");

    // GetStatus is NOT called for a non-placed env: the platform has no
    // observer on the user's own cluster.
    expect(getEnvironmentStatus).not.toHaveBeenCalled();

    expectNoBannerOrError();
    expect(daemonCalls).toEqual([]);
  });

  it("renders a never-built env as a normal state, not an error", async () => {
    routeState.env = "fresh";
    renderWithQuery(<ForgeEnvPage />);

    const panel = await screen.findByTestId("live-never-built");
    expect(panel).toHaveTextContent(/not built yet/i);
    expect(panel).toHaveTextContent("forge env build fresh");
    // The remedy is offered as a navigation, not as a daemon requirement.
    expect(screen.getByRole("button", { name: /open preview/i })).toBeInTheDocument();

    expect(screen.queryByTestId("live-section")).not.toBeInTheDocument();
    expectNoBannerOrError();
    expect(daemonCalls).toEqual([]);
  });

  it("sets a secret from Live with zero daemon calls", async () => {
    routeState.env = "prod";
    const user = userEvent.setup();
    renderWithQuery(<ForgeEnvPage />);

    await screen.findByText("STRIPE_WEBHOOK_SECRET");
    await user.click(screen.getByTestId("add-secret"));

    await user.type(await screen.findByLabelText("Name"), "NEW_SECRET");
    await user.type(screen.getByLabelText("Value"), "s3cret");
    await user.click(screen.getByTestId("set-secret-submit"));

    await waitFor(() => expect(setSecret).toHaveBeenCalled());
    // The write went straight to the control plane with the user's session.
    expect(setSecret.mock.calls[0]?.[0]).toMatchObject({
      environmentId: "cp-prod",
      name: "NEW_SECRET",
    });
    expect(daemonCalls).toEqual([]);
  });
});

describe("the Overview makes zero daemon calls", () => {
  it("lists every environment from the control plane, fully rendered", async () => {
    renderWithQuery(<ForgeOverviewPage />);

    await screen.findByTestId("forge-env-table");

    const prod = screen.getByTestId("env-row-prod");
    expect(within(prod).getByText("v12")).toBeInTheDocument();
    expect(within(prod).getByTestId("provenance-prod")).toBeInTheDocument();

    const staging = screen.getByTestId("env-row-staging");
    expect(within(staging).getByText(/declared, not built/i)).toBeInTheDocument();

    // The retired copy. It asked the user to go and start a daemon so the
    // tool could show facts it already had.
    expect(screen.queryByText(/start your daemon once to see them/i)).not.toBeInTheDocument();

    expectNoBannerOrError();
    expect(daemonCalls).toEqual([]);
  });

  it("shows no environments as a normal state when the project has none", async () => {
    getLiveView.mockResolvedValue([]);
    renderWithQuery(<ForgeOverviewPage />);

    const empty = await screen.findByTestId("forge-overview-empty");
    expect(empty).toHaveTextContent(/no environments have been built yet/i);
    expect(empty).toHaveTextContent("forge env build");

    expectNoBannerOrError();
    expect(daemonCalls).toEqual([]);
  });
});
