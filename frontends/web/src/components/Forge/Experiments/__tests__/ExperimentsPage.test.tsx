// Copyright (c) 2025 Reliant Labs

/**
 * The Experiments page against a fake ChangesService behind the app's
 * transport (fakeChangesService.ts): the generated hooks and the page's
 * readers over an in-memory Connect router, so what is asserted is what the
 * page sends on the wire (protojson) and what it does with each answer — the list, the empty and not-available states, a new change, the
 * approval-code flow, a revision conflict, and Stop.
 */

import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";

import { renderWithQuery } from "@/test/renderWithQuery";
import { useRouteOrgScope } from "@/lib/routeOrg";

import { ChangeDetail } from "../ChangeDetail";
import { ExperimentsPage } from "../ExperimentsPage";
import { installFakeChangesService, type FakeChangesService, type FakeHandler } from "./fakeChangesService";

const state = vi.hoisted(() => ({
  console: {} as Record<string, unknown>,
  navigate: vi.fn(),
  search: {} as Record<string, unknown>,
}));

vi.mock("@/services/controlPlane/orgs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/orgs")>()),
  currentOrgId: async () => "org-1",
}));
vi.mock("../../Console/consoleContext", () => ({ useForgeConsole: () => state.console }));
vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, className }: { href: string; children: ReactNode; className?: string }) => (
    <a href={href} className={className}>
      {children}
    </a>
  ),
}));
vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => state.navigate,
  useSearch: () => state.search,
  useParams: () => ({}),
}));

const ENVS = [
  { id: "env-prod", name: "prod" },
  { id: "env-staging", name: "staging" },
];

/** A change as the control plane sends it (protojson). */
function wireChange(overrides: Record<string, unknown> = {}) {
  return {
    id: "chg-1",
    orgId: "org-1",
    environmentId: "env-prod",
    key: "new-checkout",
    description: "The one-page checkout",
    mode: "CHANGE_MODE_OFF",
    unit: "CHANGE_UNIT_USER",
    percentBp: 1000,
    split: [
      { variant: "control", weightBp: 5000 },
      { variant: "treatment", weightBp: 5000 },
    ],
    metric: { event: "checkout_completed", windowHours: 48 },
    revision: "7",
    declaration: {
      type: "bool",
      defaultVariant: "control",
      variants: [
        { name: "control", valueJson: "false" },
        { name: "treatment", valueJson: "true" },
      ],
    },
    approvalRequired: true,
    ...overrides,
  };
}

let installed: FakeChangesService | null = null;

/** The fake service: one handler per method, every call recorded. */
function fakeService(handlers: Record<string, FakeHandler>) {
  installed?.restore();
  installed = installFakeChangesService(handlers);
  return installed;
}

afterEach(() => {
  installed?.restore();
  installed = null;
});

const reason = (r: string, code: Code, message = r) => new ConnectError(message, code, new Headers({ "x-forge-error-reason": r }));

function roster() {
  return {
    isLoading: false,
    resolvingName: false,
    live: { isLoading: false, data: { availability: "available" } },
    envs: ENVS.map((e) => ({ name: e.name, lifecycle: "deployed", live: { id: e.id, name: e.name }, forge: null, source: "backend" })),
  };
}

function renderPage(_service: FakeChangesService) {
  return renderWithQuery(<ExperimentsPage />);
}

function renderDetail(_service: FakeChangesService) {
  return renderWithQuery(<ChangeDetail changeId="chg-1" envs={ENVS} backHref="/forge/p/shop/experiments" />);
}

beforeEach(() => {
  state.navigate.mockReset();
  state.search = {};
  state.console = {
    forgeProject: "shop",
    env: "all",
    roster: roster(),
    setEnv: vi.fn(),
    href: (page: { kind: string; changeId?: string }) => (page.changeId ? `/forge/p/shop/experiments/${page.changeId}` : `/forge/p/shop/${page.kind}`),
  };
});

describe("ExperimentsPage", () => {
  it("lists the project's changes with their mode, environment and who gets them", async () => {
    const service = fakeService({
      ListChanges: () => ({
        changes: [
          wireChange({ mode: "CHANGE_MODE_EXPERIMENT", percentBp: 2000, currentRun: { id: "r_abcdefgh", kind: "experiment", startedAt: new Date(Date.now() - 3 * 3600_000).toISOString() } }),
          wireChange({ id: "chg-2", key: "dark-mode", environmentId: "env-staging", mode: "CHANGE_MODE_EVERYONE", everyoneVariant: "on", declaration: undefined }),
        ],
      }),
    });
    renderPage(service);

    const row = await screen.findByTestId("change-row-new-checkout");
    expect(within(row).getByText("Experiment")).toBeInTheDocument();
    expect(within(row).getByText("prod")).toBeInTheDocument();
    expect(within(row).getByText("20% of users are in the comparison")).toBeInTheDocument();
    expect(within(row).getByText("3h")).toBeInTheDocument();
    expect(within(row).getByRole("link", { name: "new-checkout" })).toHaveAttribute("href", "/forge/p/shop/experiments/chg-1");
    const other = screen.getByTestId("change-row-dark-mode");
    expect(within(other).getByText("Everyone gets on")).toBeInTheDocument();
    expect(within(other).getByText("Not declared yet")).toBeInTheDocument();
    // Scoped by org and project; "All" sends no environment.
    expect(service.of("ListChanges")[0]!.body).toMatchObject({ orgId: "org-1", project: "shop" });
    expect(service.of("ListChanges")[0]!.body).not.toHaveProperty("environmentId");
  });

  it("reads every page of a project's changes", async () => {
    const service = fakeService({
      ListChanges: (body) =>
        body.pageToken === "p2"
          ? { changes: [wireChange({ id: "chg-2", key: "dark-mode" })] }
          : { changes: [wireChange()], nextPageToken: "p2" },
    });
    renderPage(service);
    expect(await screen.findByTestId("change-row-dark-mode")).toBeInTheDocument();
    expect(screen.getByTestId("change-row-new-checkout")).toBeInTheDocument();
    expect(service.of("ListChanges").map((c) => c.body.pageToken ?? "")).toEqual(["", "p2"]);
    expect(screen.queryByTestId("changes-truncated")).not.toBeInTheDocument();
  });

  it("lists the route's org's changes, and another org's after a switch, for the same project name", async () => {
    const before = useRouteOrgScope.getState();
    try {
      useRouteOrgScope.setState({ scope: { kind: "member", org: { id: "org-a", slug: "a" } } });
      const service = fakeService({
        ListChanges: (body) => ({ changes: [wireChange(body.orgId === "org-a" ? { key: "only-in-a" } : { id: "chg-b", key: "only-in-b" })] }),
      });
      renderPage(service);
      expect(await screen.findByTestId("change-row-only-in-a")).toBeInTheDocument();
      useRouteOrgScope.setState({ scope: { kind: "member", org: { id: "org-b", slug: "b" } } });
      expect(await screen.findByTestId("change-row-only-in-b")).toBeInTheDocument();
      expect(screen.queryByTestId("change-row-only-in-a")).not.toBeInTheDocument();
      expect(service.of("ListChanges").map((c) => c.body.orgId)).toEqual(["org-a", "org-b"]);
    } finally {
      useRouteOrgScope.setState(before);
    }
  });

  it("narrows the list to the top bar's environment", async () => {
    state.console = { ...state.console, env: "staging" };
    const service = fakeService({ ListChanges: () => ({ changes: [] }) });
    renderPage(service);
    await screen.findByText("No changes yet");
    expect(service.of("ListChanges")[0]!.body).toMatchObject({ environmentId: "env-staging" });
  });

  it("offers New change when there are none", async () => {
    renderPage(fakeService({ ListChanges: () => ({ changes: [] }) }));
    expect(await screen.findByText("No changes yet")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /New change/ }).length).toBeGreaterThan(0);
  });

  it("says so when the control plane has no ChangesService", async () => {
    renderPage(fakeService({}));
    expect(await screen.findByText("Experiments aren't available here yet")).toBeInTheDocument();
  });

  it("drafts a new change, noting a key no release declares yet", async () => {
    const service = fakeService({
      ListChanges: () => ({ changes: [wireChange()] }),
      CreateChange: (body) => ({ change: wireChange({ id: "chg-new", key: body.key, environmentId: body.environmentId, declaration: undefined }) }),
    });
    renderPage(service);
    await screen.findByTestId("change-row-new-checkout");
    await userEvent.click(screen.getByRole("button", { name: /New change/ }));

    await userEvent.type(screen.getByTestId("new-change-key"), "pricing-page");
    expect(screen.getByTestId("new-change-undeclared")).toHaveTextContent("Not declared yet");
    await userEvent.selectOptions(screen.getByTestId("new-change-env"), "env-staging");
    await userEvent.click(screen.getByRole("radio", { name: "Compare versions" }));
    await userEvent.click(screen.getByTestId("new-change-submit"));

    await waitFor(() => expect(service.of("CreateChange")).toHaveLength(1));
    expect(service.of("CreateChange")[0]!.body).toEqual({ environmentId: "env-staging", key: "pricing-page", unit: "CHANGE_UNIT_USER" });
    await waitFor(() => expect(state.navigate).toHaveBeenCalledWith({ href: "/forge/p/shop/experiments/chg-new?who=compare" }));
  });
});

describe("ChangeDetail", () => {
  it("asks for an approval code when a protected environment refuses a launch, then retries with it", async () => {
    let launches = 0;
    const service = fakeService({
      GetChange: () => ({ change: wireChange() }),
      ListChangeEvents: () => ({ events: [] }),
      LaunchChange: () => {
        launches++;
        if (launches === 1) throw reason("approval_required", Code.PermissionDenied);
        return { change: wireChange({ mode: "CHANGE_MODE_EXPERIMENT", revision: "8", currentRun: { id: "r_new", kind: "experiment" } }) };
      },
      RequestChangeApproval: () => ({ sentTo: "a***@example.com", expiresAt: "2026-11-20T10:10:00Z" }),
    });
    renderDetail(service);
    await userEvent.click(await screen.findByTestId("who-launch"));

    const panel = await screen.findByTestId("change-approval");
    expect(panel).toHaveTextContent("prod is a protected environment");
    await userEvent.click(within(panel).getByTestId("approval-send"));
    expect(await within(panel).findByText(/We sent a code to a\*\*\*@example.com/)).toBeInTheDocument();
    await userEvent.type(within(panel).getByTestId("approval-code"), "123456");
    await userEvent.click(within(panel).getByRole("button", { name: "Approve and launch" }));

    await waitFor(() => expect(screen.queryByTestId("change-approval")).not.toBeInTheDocument());
    const [first, retry] = service.of("LaunchChange");
    expect(first!.body).toEqual({ changeId: "chg-1", expectedRevision: "7", percentBp: 1000 });
    expect(retry!.body).toEqual({ changeId: "chg-1", expectedRevision: "7", percentBp: 1000, stepUpCode: "123456" });
    expect(await screen.findByText("Revision 8")).toBeInTheDocument();
  });

  it("refetches on a revision conflict and says what moved, saving nothing", async () => {
    let reads = 0;
    const service = fakeService({
      GetChange: () => {
        reads++;
        return { change: reads === 1 ? wireChange() : wireChange({ revision: "9", percentBp: 3000, mode: "CHANGE_MODE_EXPERIMENT" }) };
      },
      ListChangeEvents: () => ({ events: [] }),
      LaunchChange: () => {
        throw reason("change_revision_conflict", Code.FailedPrecondition);
      },
    });
    renderDetail(service);
    await userEvent.click(await screen.findByTestId("who-launch"));

    const notice = await screen.findByTestId("change-conflict");
    expect(notice).toHaveTextContent("Someone changed this while you were looking");
    expect(notice).toHaveTextContent("Mode: Off → Experiment");
    expect(notice).toHaveTextContent("Share: 10% → 30%");
    expect(await screen.findByText("Revision 9")).toBeInTheDocument();
    expect(service.of("LaunchChange")).toHaveLength(1);
  });

  it("rolls out another version of a running rollout instead of ramping the old one", async () => {
    const rollout = wireChange({ mode: "CHANGE_MODE_ROLLOUT", rolloutVariant: "treatment", percentBp: 2000, currentRun: { id: "r_roll", kind: "rollout" } });
    const service = fakeService({
      GetChange: () => ({ change: rollout }),
      ListChangeEvents: () => ({ events: [] }),
      RampChange: () => ({ change: { ...rollout, revision: "8" } }),
      RollOutChange: () => ({ change: { ...rollout, rolloutVariant: "control", revision: "8" } }),
    });
    renderDetail(service);
    const actions = await screen.findByTestId("who-actions");
    expect(within(actions).getByRole("button", { name: /^Change to 20%/ })).toBeInTheDocument();
    await userEvent.selectOptions(within(actions).getByTestId("who-variant"), "control");
    await userEvent.click(within(actions).getByRole("button", { name: /^Roll out to 20%/ }));

    await waitFor(() => expect(service.of("RollOutChange")).toHaveLength(1));
    expect(service.of("RollOutChange")[0]!.body).toMatchObject({ changeId: "chg-1", variant: "control", percentBp: 2000 });
    expect(service.of("RampChange")).toHaveLength(0);
  });

  it("stops with one click and a confirm, sending no revision", async () => {
    const service = fakeService({
      GetChange: () => ({ change: wireChange({ mode: "CHANGE_MODE_EXPERIMENT", currentRun: { id: "r_abcdefgh", kind: "experiment" } }) }),
      ListChangeEvents: () => ({ events: [] }),
      StopChange: () => ({ change: wireChange({ mode: "CHANGE_MODE_OFF", revision: "8" }) }),
    });
    renderDetail(service);
    await userEvent.click(await screen.findByRole("button", { name: "Stop" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Everyone gets the default right away");
    await userEvent.click(within(dialog).getByRole("button", { name: "Stop" }));

    await waitFor(() => expect(service.of("StopChange")).toHaveLength(1));
    expect(service.of("StopChange")[0]!.body).toEqual({ changeId: "chg-1" });
  });

  it("shows the audit trail and holds results until the readout service ships", async () => {
    const service = fakeService({
      GetChange: () => ({ change: wireChange() }),
      ListChangeEvents: () => ({
        events: [{ id: "ev-1", revision: "7", actorKind: "agent", actorId: "alice", action: "update_draft", diffJson: '{"percent_bp":{"from":500,"to":1000}}' }],
      }),
    });
    renderDetail(service);
    expect(await screen.findByTestId("results-not-deployed")).toHaveTextContent("Results appear once the readout service ships");
    const history = await screen.findByTestId("change-history");
    expect(history).toHaveTextContent("alice (an agent acting for them) edited the draft");
    expect(history).toHaveTextContent("percent_bp: 500 → 1000");
  });
});
