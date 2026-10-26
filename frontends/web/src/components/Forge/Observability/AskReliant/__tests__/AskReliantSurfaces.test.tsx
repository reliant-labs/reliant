// Copyright (c) 2025 Reliant Labs

/**
 * "Ask Reliant" on the four surfaces it ships on, against the real views and
 * the fixture telemetry: an issue, a trace, a firing alert, a dashboard panel.
 * Each asserts the exact prompt the button hands the orchestration, and — the
 * point of the feature — that none of the telemetry on screen is in it.
 */

import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { useProjectStore, type Project } from "@/store/projectStore";

const projectList = vi.hoisted(() => ({ projects: [] as unknown[] }));
vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  fetchProjects: vi.fn(async () => projectList.projects),
}));
const startAskReliantChat = vi.hoisted(() => vi.fn());
vi.mock("../startAskReliantChat", () => ({ startAskReliantChat }));
vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("../../__tests__/obsMocks")).obsMocks.virtual());

import { FIXTURE_DECLARED } from "../../__fixtures__/fixtureAlerting";
import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT, FIXTURE_TRACE_ID } from "../../__fixtures__/fixtureApi";
import type { DashboardSearch } from "../../Dashboards/dashboardModel";
import { DashboardView, type DashboardHrefs } from "../../Dashboards/DashboardView";
import type { ObsPlace } from "../../obsPlace";
import { renderPanel, renderWithApi } from "../../__tests__/obsTestUtils";

const project: Project = {
  id: "reliant-project",
  name: "Shop",
  path: "/work/shop",
  is_git_repo: true,
  last_active: "",
  created_at: "",
  updated_at: "",
  is_forge: true,
  forge_project_name: FIXTURE_PROJECT,
};

const PROJECT: ObsPlace = { kind: "project", project: FIXTURE_PROJECT, environments: FIXTURE_ENVS.map((env) => ({ ...env, declared: FIXTURE_DECLARED[env.id] })) };

const startedWith = async () => {
  await waitFor(() => expect(startAskReliantChat).toHaveBeenCalledTimes(1));
  return startAskReliantChat.mock.calls[0][0] as { project: Project; prompt: string; workspaceName: string };
};

// What each fixture surface shows that a prompt must never repeat.
const TELEMETRY = [/context deadline exceeded/i, /DeadlineExceeded/, /nil pointer/i, /rpc_create_order\.go/, /checkout p95 is 1\.42s/, /team=payments/, /payments\.v1\.PaymentsService/];
const expectNoTelemetry = (prompt: string) => {
  for (const pattern of TELEMETRY) expect(prompt).not.toMatch(pattern);
};

describe("Ask Reliant on the observability surfaces", () => {
  beforeEach(() => {
    startAskReliantChat.mockReset().mockResolvedValue({ worktreeId: "w1" });
    projectList.projects = [project];
    useProjectStore.setState({ currentProject: project });
  });

  it("an issue: its id and environment, and the issue verb", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi(), { view: "errors" }, PROJECT);
    await user.click(await screen.findByTestId("obs-issue-open-iss-orders-deadline"));
    const detail = await screen.findByTestId("obs-issue");
    await waitFor(() => expect(detail).toHaveAttribute("data-state", "ready"));

    await user.click(within(detail).getByTestId("ask-reliant"));
    const started = await startedWith();
    expect(started.project).toBe(project);
    expect(started.workspaceName).toMatch(/^ask-issue-iss-orders-deadline-/);
    expect(started.prompt).toBe(
      `Issue iss-orders-deadline in environment prod (forge project ${FIXTURE_PROJECT}).\n\n` +
        "Fetch the details yourself with the forge CLI, starting with `reliant forge obs issue iss-orders-deadline prod`."
    );
    expectNoTelemetry(started.prompt);
  });

  it("a trace: its id, the environment its scope narrows to, the trace verb and its logs", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi(), { view: "traces", trace: FIXTURE_TRACE_ID, envs: ["prod"] }, PROJECT);
    await user.click(await screen.findByTestId("ask-reliant"));
    const started = await startedWith();
    expect(started.prompt).toBe(
      `Trace ${FIXTURE_TRACE_ID} in environment prod (forge project ${FIXTURE_PROJECT}).\n\n` +
        `Fetch the details yourself with the forge CLI, starting with \`reliant forge obs trace ${FIXTURE_TRACE_ID} prod\` ` +
        `(its logs: \`reliant forge obs logs prod --trace ${FIXTURE_TRACE_ID}\`).`
    );
    expectNoTelemetry(started.prompt);
  });

  it("a firing alert: rule, environment and fingerprint, and what answers them", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi(), { view: "alerts" }, PROJECT);
    const checkout = await screen.findByTestId("obs-alert-CheckoutP95High", {}, { timeout: 10_000 });
    await user.click(within(checkout).getByTestId("ask-reliant"));
    const started = await startedWith();
    expect(started.prompt).toMatch(
      new RegExp(
        `^Alert rule CheckoutP95High \\(fingerprint \\S+\\) in environment prod \\(forge project ${FIXTURE_PROJECT}\\)\\.\\n\\n` +
          "Fetch the details yourself with the forge CLI: `reliant forge obs status prod --json --fields alerts` lists what is firing, and " +
          "`reliant forge obs alerts check --env prod --rule CheckoutP95High --sql` prints the rule's expression\\.$"
      )
    );
    expectNoTelemetry(started.prompt);
  });
});

describe("Ask Reliant on a dashboard panel", () => {
  const hrefs: DashboardHrefs = {
    logs: () => "/logs",
    traces: () => "/traces",
    workload: (name) => `/workloads/${name}`,
    list: "/dashboards",
    release: (v) => `/releases/${v}`,
    trace: (id) => `/trace/${id}`,
    dashboard: (ref) => `/d/${ref.kind}/${ref.key}`,
  };

  function renderDashboard(env: string) {
    function Stateful() {
      const [search, setSearch] = useState<DashboardSearch>({ range: "6h" });
      return (
        <DashboardView
          project={FIXTURE_PROJECT}
          dashRef={{ kind: "workspace", projectId: FIXTURE_PROJECT, key: "checkout-latency" }}
          env={env}
          environments={FIXTURE_ENVS}
          search={search}
          onSearch={(next) => setSearch((prev) => ({ ...prev, ...next }))}
          onEnvChange={vi.fn()}
          hrefs={hrefs}
          navigateTo={vi.fn()}
        />
      );
    }
    renderWithApi(createFixtureApi(), <Stateful />);
  }

  beforeEach(() => {
    startAskReliantChat.mockReset().mockResolvedValue({ worktreeId: "w1" });
    projectList.projects = [project];
    useProjectStore.setState({ currentProject: project });
  });

  it("a panel menu entry: the panel's title and query, over the dashboard's window, in its environment", async () => {
    const user = userEvent.setup();
    renderDashboard("prod");
    await user.click(await screen.findByTestId("dashboard-panel-menu-p95"));
    await user.click(await screen.findByTestId("dashboard-panel-ask-p95"));
    const { prompt } = await startedWith();

    const [head, query, , instruction] = prompt.split("\n");
    expect(head).toMatch(/^Dashboard panel ".+" of dashboard ".+" in environment prod \(forge project .+\), last 6h\.$/);
    expect(query).toMatch(/^Query: \{"id":"p95",/);
    expect(JSON.parse(query.slice("Query: ".length))).toMatchObject({ id: "p95", signal: expect.any(String), series: expect.any(Array) });
    expect(instruction).toMatch(/^Fetch the details yourself with the forge CLI: /);
    expect(instruction).toContain("--since 6h");
    expect(instruction).toMatch(/`reliant forge obs (query prod --panel panel\.json|metrics query prod \S+)/);
  });

  it("does not choose an environment for the agent when the dashboard spans all of them", async () => {
    const user = userEvent.setup();
    renderDashboard("all");
    await user.click(await screen.findByTestId("dashboard-panel-menu-p95"));
    await user.click(await screen.findByTestId("dashboard-panel-ask-p95"));
    const { prompt } = await startedWith();
    expect(prompt).toContain("an environment the console could not name, one of ");
    expect(prompt).toContain("<env>");
  });
});
