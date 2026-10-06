// Copyright (c) 2025 Reliant Labs

/**
 * THE OWNER'S TEST: AN ENVIRONMENT THE BACKEND RECORDS MAKES ZERO DAEMON CALLS
 * ON ITS BACKEND TABS (Overview, Releases, Secrets) — and the Overview list.
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
 *   fresh    no control-plane row at all. The ONE case that asks the daemon
 *            (only the checkout knows what an unregistered env is), and it
 *            must still render — as "not registered", not an error — when
 *            that asking fails.
 *
 * Plus the write: setting a secret from Live makes zero daemon calls either.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import type { CloudEnvStatus, CloudPromotion } from "@/services/forge/cloudEnvs";
import type { LiveConvergence, LiveEnv } from "@/services/forge/live";

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
      `A BACKEND TAB CALLED THE DAEMON: ${method}. Overview, Releases and Secrets read the control plane only (design §8.0, O-14).`
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

const routeState: { env: string; tab?: string; forgeProject?: string } = { env: "prod" };
const navigate = vi.fn();

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
  useParams: () => ({ env: routeState.env }),
  useSearch: () => ({ project: "proj-1", tab: routeState.tab, forgeProject: routeState.forgeProject }),
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
    // NOTHING OBSERVED is the default because it is the common case: the
    // platform's convergence observer is dark by default, so an environment
    // with no reading is the state most screens render today.
    observed: { state: "not-reported" },
    drift: { state: "not-reported" },
    driftDetail: "",
    provenance: "",
    holds: [],
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
  observed: { state: "converged", observedAt: "2026-10-01T10:04:00.000Z" },
  drift: { state: "in_sync", observedAt: "2026-10-01T10:04:00.000Z" },
  driftDetail: "",
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

/**
 * The observation timeline, which is ALSO a control-plane read. It is listed
 * for every environment kind, so a daemon call could not hide behind the
 * self-managed case.
 */
const CONVERGENCES: LiveConvergence[] = [
  {
    id: "conv-1",
    state: "converged",
    reason: "ReconciliationSucceeded",
    message: "",
    cluster: "hosted-us-central1",
    observedAt: "2026-10-01T10:04:00.000Z",
  },
];

const getLiveView = vi.fn(() => Promise.resolve([PROD, STAGING]));
const listEnvironmentConvergences = vi.fn((environmentId: string) =>
  Promise.resolve(environmentId === "cp-prod" ? CONVERGENCES : [])
);
vi.mock("@/services/forge/live", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/forge/live")>()),
  getLiveView: (project: string) => getLiveView(project),
  listEnvironmentConvergences: (id: string) => listEnvironmentConvergences(id),
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

// Custom domains are an org-wide control-plane read. One is bound to prod,
// one to staging, so the env-scoped view has something to filter out.
vi.mock("@/services/forge/domains", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/forge/domains")>()),
  hasControlPlane: () => true,
  listDomains: () =>
    Promise.resolve([
      {
        id: "dom-1",
        hostname: "hounders.club",
        state: "live",
        origin: "external",
        requiredRecords: [],
        lastError: "",
        binding: { id: "b1", domainId: "dom-1", environmentId: "cp-prod", target: "web", redirectTo: "" },
      },
      {
        id: "dom-2",
        hostname: "staging.hounders.club",
        state: "live",
        origin: "external",
        requiredRecords: [],
        lastError: "",
        binding: { id: "b2", domainId: "dom-2", environmentId: "cp-staging", target: "web", redirectTo: "" },
      },
    ]),
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
  routeState.forgeProject = undefined;
  vi.clearAllMocks();
  getLiveView.mockResolvedValue([PROD, STAGING]);
  listEnvironmentConvergences.mockImplementation((id: string) =>
    Promise.resolve(id === "cp-prod" ? CONVERGENCES : [])
  );
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

    // Intent and the observed reading, both from the control plane.
    const state = screen.getByTestId("live-state");
    expect(within(state).getByTestId("live-state-intent")).toHaveTextContent(
      /should be running v12, promoted by ci/i
    );
    expect(within(state).getByTestId("live-state-observed")).toHaveTextContent(/confirmed running/i);

    // The header names the release and offers the actions, on every tab.
    expect(screen.getByTestId("env-page-release")).toHaveTextContent("v12");
    expect(screen.getByTestId("env-action-deploy")).toBeInTheDocument();
    expect(screen.getByTestId("env-action-promote")).toBeInTheDocument();
    expectNoBannerOrError();
    expect(daemonCalls).toEqual([]);
  });

  it("renders the Releases tab — releases only, no observations — with zero daemon calls", async () => {
    routeState.env = "prod";
    routeState.tab = "releases";
    renderWithQuery(<ForgeEnvPage />);

    const releases = await screen.findByTestId("live-releases");
    expect(within(releases).getByTestId("promotion-promo-1")).toBeInTheDocument();
    // Convergence activity is the Activity tab's, never interleaved here.
    expect(within(releases).queryByTestId("convergence-conv-1")).not.toBeInTheDocument();
    expect(releases.querySelector('[data-entry="observation"]')).toBeNull();
    expectNoBannerOrError();
    expect(daemonCalls).toEqual([]);
  });

  it("renders the Activity tab — promotions and observations interleaved — with zero daemon calls", async () => {
    routeState.env = "prod";
    routeState.tab = "activity";
    renderWithQuery(<ForgeEnvPage />);

    const activity = await screen.findByTestId("live-activity");
    expect(within(activity).getByTestId("activity-promotion-promo-1")).toBeInTheDocument();
    expect(within(activity).getByTestId("convergence-conv-1")).toHaveTextContent(/confirmed running/i);
    expect(screen.queryByTestId("live-releases")).not.toBeInTheDocument();
    expectNoBannerOrError();
    expect(daemonCalls).toEqual([]);
  });

  it("renders the Domains tab with the domains bound to this env only, with zero daemon calls", async () => {
    routeState.env = "prod";
    routeState.tab = "domains";
    renderWithQuery(<ForgeEnvPage />);

    await screen.findByTestId("env-domain-hounders.club");
    expect(screen.queryByTestId("env-domain-staging.hounders.club")).not.toBeInTheDocument();
    expect(daemonCalls).toEqual([]);
  });

  it("renders the Secrets tab from the managed store with zero daemon calls", async () => {
    routeState.env = "prod";
    routeState.tab = "secrets";
    renderWithQuery(<ForgeEnvPage />);

    // From the managed store joined with the DECLARED shape.
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

    // GetStatus is NOT called for a non-placed env: the platform has no
    // observer on the user's own cluster.
    expect(getEnvironmentStatus).not.toHaveBeenCalled();

    expectNoBannerOrError();
    expect(daemonCalls).toEqual([]);
  });

  it("lists a declared-not-built env's secrets on Secrets even though none was ever set", async () => {
    routeState.env = "staging";
    routeState.tab = "secrets";
    renderWithQuery(<ForgeEnvPage />);
    await screen.findByText("DATABASE_URL");
    expect(daemonCalls).toEqual([]);
  });

  it("renders an env Reliant has no record of as 'not registered', even when the daemon throws", async () => {
    routeState.env = "fresh";
    renderWithQuery(<ForgeEnvPage />);

    const panel = await screen.findByTestId("env-overview-unregistered", {}, { timeout: 5000 });
    expect(panel).toHaveTextContent(/not registered in reliant/i);
    // NOT "not built yet": no record in Reliant says nothing about whether it
    // was ever built — control-plane's prod is live with no row here.
    expect(panel).not.toHaveTextContent(/not built yet/i);
    expect(screen.queryByTestId("live-section")).not.toBeInTheDocument();
    // Only the checkout can say what an unregistered env is, so this is the
    // one case allowed to ask — and only for its identity.
    expect(daemonCalls.every((call) => call === "getTopology" || call === "getEnvStatus")).toBe(true);
  });

  it("sets a secret from the Secrets tab with zero daemon calls", async () => {
    routeState.env = "prod";
    routeState.tab = "secrets";
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

/**
 * The Overview TABLE is the backend. The daemon is asked for one thing only —
 * the checkout's topology, which feeds the separately labelled "not
 * registered" list — and the table must render in full when that throws.
 */
describe("the Overview table needs no daemon", () => {
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
    expect(daemonCalls.every((call) => call === "getTopology")).toBe(true);
    expect(screen.queryByTestId("forge-overview-unregistered")).not.toBeInTheDocument();
  });

  it("shows no environments as a normal state when the project has none", async () => {
    getLiveView.mockResolvedValue([]);
    renderWithQuery(<ForgeOverviewPage />);

    const empty = await screen.findByTestId("forge-overview-empty", {}, { timeout: 5000 });
    expect(empty).toHaveTextContent(/no environments recorded yet/i);
    expect(empty).toHaveTextContent("forge env build");

    expectNoBannerOrError();
    expect(daemonCalls.every((call) => call === "getTopology")).toBe(true);
  });
});

/**
 * The tab is URL-addressable: `?tab=` selects it, an absent param is the
 * default (Overview for a deployed env), and a click writes a history entry
 * so Back steps through the tabs a reader opened.
 */
describe("tab routing", () => {
  it("opens a deployed env on Overview when the URL names no tab", async () => {
    routeState.env = "prod";
    renderWithQuery(<ForgeEnvPage />);
    await screen.findByTestId("live-section");
    expect(screen.getByTestId("forge-env-page")).toHaveAttribute("data-tab", "overview");
    expect(screen.getByTestId("env-tab-overview")).toHaveAttribute("aria-selected", "true");
  });

  it.each(["releases", "activity", "secrets", "domains"])("selects the %s tab from the URL", async (tab) => {
    routeState.env = "prod";
    routeState.tab = tab;
    renderWithQuery(<ForgeEnvPage />);
    await waitFor(() => expect(screen.getByTestId("forge-env-page")).toHaveAttribute("data-tab", tab));
    expect(screen.getByTestId(`env-tab-${tab}`)).toHaveAttribute("aria-selected", "true");
  });

  it("writes the clicked tab to the URL as a history entry, and the default as no param", async () => {
    routeState.env = "prod";
    const user = userEvent.setup();
    renderWithQuery(<ForgeEnvPage />);
    await screen.findByTestId("live-section");

    await user.click(screen.getByTestId("env-tab-activity"));
    const call = navigate.mock.calls.at(-1)?.[0] as {
      search: (prev: Record<string, unknown>) => Record<string, unknown>;
      replace?: boolean;
    };
    expect(call.replace).toBeFalsy();
    expect(call.search({ project: "proj-1" })).toEqual({ project: "proj-1", tab: "activity" });
    expect(screen.getByTestId("forge-env-page")).toHaveAttribute("data-tab", "activity");

    await user.click(screen.getByTestId("env-tab-overview"));
    const back = navigate.mock.calls.at(-1)?.[0] as {
      search: (prev: Record<string, unknown>) => Record<string, unknown>;
    };
    expect(back.search({ project: "proj-1", tab: "activity" })).toEqual({ project: "proj-1", tab: undefined });
  });

  it("follows the URL when it moves after a click (Back/Forward)", async () => {
    routeState.env = "prod";
    const user = userEvent.setup();
    const { rerender } = renderWithQuery(<ForgeEnvPage />);
    await screen.findByTestId("live-section");

    await user.click(screen.getByTestId("env-tab-releases"));
    expect(screen.getByTestId("forge-env-page")).toHaveAttribute("data-tab", "releases");

    // The router lands the click, then Back moves the URL to Secrets' entry.
    routeState.tab = "secrets";
    rerender(
      <QueryClientProvider client={new QueryClient()}>
        <ForgeEnvPage />
      </QueryClientProvider>
    );
    await waitFor(() => expect(screen.getByTestId("forge-env-page")).toHaveAttribute("data-tab", "secrets"));
  });

  it("shows one status line in the header, on every tab", async () => {
    routeState.env = "prod";
    routeState.tab = "secrets";
    renderWithQuery(<ForgeEnvPage />);
    const status = await screen.findByTestId("env-page-status");
    expect(status).toHaveTextContent(/^Running · confirmed/);
    expect(status).toHaveAttribute("data-tone", "ok");
  });
});

/**
 * A QUEUED deploy: prod's v13 was accepted and is waiting on billing, while
 * v12 keeps running (and is confirmed running). The page must say the new
 * release is waiting — above every tab — and offer the way to unblock it,
 * from the control plane alone.
 */
describe("a queued deploy", () => {
  const QUEUED_PROD: LiveEnv = liveEnv({
    ...PROD,
    release: "v13",
    phase: "held",
    observed: { state: "queued" },
    holds: [
      {
        kind: "billing",
        promotionId: "promo-2",
        reason: "this runs compute (1 workload) and the organization has no active compute plan",
        fix: "Subscribe to a Reliant Compute plan in Reliant → Settings → Billing (an org admin can). The deploy starts automatically once the plan is active; nothing needs to be re-run.",
        actionUrl: "https://app.reliant.dev/forge/env/prod?forgeProject=hounders",
        callerCanResolve: true,
        heldSince: "2026-10-06T12:00:00.000Z",
      },
    ],
  });

  it("says it is waiting on billing, above every tab, and sends an admin to billing and back", async () => {
    getLiveView.mockResolvedValue([QUEUED_PROD, STAGING]);
    routeState.tab = "releases";
    const user = userEvent.setup();
    renderWithQuery(<ForgeEnvPage />);

    const banner = await screen.findByTestId("queued-deploy-banner");
    expect(banner).toHaveTextContent("Waiting on billing");
    expect(banner).toHaveTextContent("Release v13 is recorded.");
    expect(screen.getByTestId("env-page-status")).toHaveTextContent("Queued · waiting on billing");
    expect(screen.getByTestId("env-page-status")).toHaveAttribute("data-tone", "waiting");

    await user.click(within(banner).getByTestId("queued-deploy-set-up-billing"));
    expect(navigate).toHaveBeenCalledWith(
      expect.objectContaining({
        to: "/settings/$section",
        params: { section: "billing" },
        search: expect.objectContaining({ tab: "plans", from: "forge", returnTo: expect.stringMatching(/^\//) }),
      })
    );
    expect(daemonCalls).toEqual([]);
  });

  it("says nothing of a queue when nothing is queued", async () => {
    renderWithQuery(<ForgeEnvPage />);
    await screen.findByTestId("live-section");
    expect(screen.queryByTestId("queued-deploy-banner")).not.toBeInTheDocument();
  });

  /**
   * The control plane's link names only the FORGE project. Opened by someone
   * with no Reliant project declaring it — the admin a teammate sent it to —
   * the page reads the control plane by that name, and asks no daemon: the
   * current project's checkout is a different project, whose `prod` is not
   * this one.
   */
  it("opens a control-plane link by its forge project name, with no daemon", async () => {
    getLiveView.mockResolvedValue([{ ...QUEUED_PROD, project: "barkshop" }]);
    routeState.forgeProject = "barkshop";
    renderWithQuery(<ForgeEnvPage />);

    await screen.findByTestId("queued-deploy-banner");
    const asked = getLiveView.mock.calls.map((call) => (call as unknown[])[0]);
    expect(asked.length).toBeGreaterThan(0);
    expect(new Set(asked)).toEqual(new Set(["barkshop"]));
    expect(daemonCalls).toEqual([]);
  });
});
