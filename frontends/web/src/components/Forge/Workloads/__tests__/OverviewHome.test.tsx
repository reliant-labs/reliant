// Copyright (c) 2025 Reliant Labs

/**
 * The Overview (project home with the workloads) against the workload
 * fixtures: the environment strip, needs attention, the Services and Jobs
 * tables reading the server's verdict, the scope, the filters, and the
 * empty / loading / error / not-set-up states.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";

import { createWorkloadsFixtureApi } from "../__fixtures__/workloadFixtures";
import { renderOverview } from "./workloadsTestUtils";

vi.mock("uplot", async () => (await import("../../Observability/__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

const ready = async () => waitFor(() => expect(screen.getByTestId("overview")).toHaveAttribute("data-state", "ready"));

describe("the Overview", () => {
  it("shows every environment's worst state in the strip, with its live release", async () => {
    renderOverview();
    await ready();
    const strip = screen.getByTestId("overview-env-strip");
    expect(within(strip).getByTestId("overview-env-prod")).toHaveAttribute("data-tone", "danger");
    expect(within(strip).getByTestId("overview-env-prod")).toHaveTextContent("1 failing");
    expect(within(strip).getByTestId("overview-env-prod")).toHaveAttribute("aria-current", "page");
    expect(within(strip).getByTestId("overview-env-e2e")).toHaveTextContent("1 job failed");
    expect(within(strip).getByTestId("overview-env-dev")).toHaveTextContent("Healthy");
    expect(within(strip).getByTestId("overview-env-previews")).toHaveTextContent("3 running");
  });

  it("makes the strip status, each cell a link to that environment — not a second scope switch", async () => {
    renderOverview();
    await ready();
    const cell = screen.getByTestId("overview-env-e2e");
    expect(cell.tagName).toBe("A");
    expect(cell).toHaveAttribute("href", "/overview?env=e2e");
    expect(cell).not.toHaveAttribute("aria-pressed");
    expect(screen.getByTestId("overview-env-prod")).toHaveAttribute("aria-current", "page");
  });

  it("lists the scoped environment's services with the server's verdict, never re-derived", async () => {
    renderOverview();
    await ready();
    const table = screen.getByTestId("overview-services-table");
    const worker = within(table).getByTestId("overview-service-reliant-temporal-worker");
    expect(worker).toHaveAttribute("data-health", "crash_looping");
    expect(worker).toHaveTextContent("Crash-looping");
    expect(worker).toHaveTextContent("StatefulSet");
    expect(worker).toHaveTextContent("0/2");
    expect(worker).toHaveTextContent(/14 restarts · Ran out of memory/);
    expect(worker).not.toHaveTextContent("OOMKilled");
    // A workload with restarts that the server calls degraded reads Degraded (not Crash-looping).
    expect(within(table).getByTestId("overview-service-litellm")).toHaveTextContent("Degraded");
    // The whole row opens the workload, in its environment.
    expect(within(worker).getByRole("link", { name: /^reliant-temporal-worker\b/ })).toHaveAttribute("href", "/w/prod/reliant-temporal-worker");
    // Only prod's, in ONE list: its 6 services, its 1 job and the 3 processes that send telemetry with no workload.
    expect(within(table).queryByText("Environment")).toBeNull();
    const rows = within(table).getAllByRole("listitem");
    expect(rows).toHaveLength(10);
    expect(rows.filter((row) => !["job", "process"].includes(row.dataset.kind!))).toHaveLength(6);
    expect(rows.filter((row) => row.dataset.kind === "job")).toHaveLength(1);
    expect(rows.filter((row) => row.dataset.kind === "process")).toHaveLength(3);
    // Worst first: the crash loop, then the degraded one.
    expect(rows[0]).toBe(worker);
    expect(screen.getByTestId("overview-counts")).toHaveTextContent("6 services and 1 job in prod");
    // CPU and memory are the workload page's (the row opens it), not the list's.
    expect(within(worker).queryByTestId("usage-meter")).toBeNull();
  });

  it("says when a release is declared by the promotion rather than verified by digest", async () => {
    renderOverview();
    await ready();
    const web = screen.getByTestId("overview-service-web");
    expect(web).toHaveTextContent("declared");
    expect(screen.getByTestId("overview-service-admin-server")).not.toHaveTextContent("declared");
    // Its CPU and memory meters are on the workload page, not in the list.
    expect(within(web).queryByTestId("usage-meter")).toBeNull();
  });

  it("says a hosted workload runs the wrong version when its digest is not the published one, instead of 'declared'", async () => {
    renderOverview();
    await ready();
    const litellm = screen.getByTestId("overview-service-litellm");
    expect(litellm).toHaveTextContent("wrong version");
    expect(litellm).not.toHaveTextContent("declared");
  });

  it("lists a self-managed environment's declared workloads as unobserved", async () => {
    renderOverview({ env: "staging" });
    await ready();
    const row = screen.getByTestId("overview-service-admin-server");
    expect(row).toHaveAttribute("data-health", "unobserved");
    expect(row).toHaveTextContent("Unobserved");
    expect(within(row).queryByRole("link")).toBeNull();
    expect(screen.getByTestId("overview-env-staging")).toHaveTextContent("Unobserved");
  });

  it("puts what needs attention first, each opening its page", async () => {
    const harness = renderOverview();
    await ready();
    const attention = screen.getByTestId("overview-attention");
    const rows = within(attention).getAllByRole("row");
    expect(rows.map((row) => row.getAttribute("data-tone"))).toEqual(["warning", "warning", "danger", "warning"]);
    expect(rows[2]).toHaveTextContent("reliant-temporal-worker crash-looping");
    await userEvent.click(within(rows[2]!).getByText(/Restarting repeatedly \(14 times in the last 24 hours\)/));
    expect(harness.navigateTo).toHaveBeenCalledWith("/w/prod/reliant-temporal-worker");
  });

  it("offers the way back on each item, naming the environment and the release", async () => {
    const onRollback = vi.fn();
    const target = { env: "prod", current: "v2", previous: "20261017.120000-abc" };
    renderOverview({ rollback: { targets: new Map([["prod", target]]), onRollback, blockedReason: null } });
    await ready();
    const attention = screen.getByTestId("overview-attention");
    const button = within(attention).getAllByRole("button", { name: /Roll back prod to 20261017\.120000/ })[0]!;
    await userEvent.click(button);
    expect(onRollback).toHaveBeenCalledWith(target);
  });

  it("says nothing is deployed without counting telemetry it is about to disown", async () => {
    renderOverview({ envs: [] });
    expect(await screen.findByTestId("overview-counts")).toHaveTextContent("Not deployed");
    expect(screen.getByText("Nothing deployed yet")).toBeInTheDocument();
  });

  it("shows the latest run of each job with the release that ran it", async () => {
    renderOverview({ env: "e2e" });
    await ready();
    const job = screen.getByTestId("overview-job-control-plane-migrate");
    expect(job).toHaveAttribute("data-result", "failed");
    expect(job).toHaveTextContent(/Failed\s*· Failed after exhausting its retries/);
    expect(job).not.toHaveTextContent("BackoffLimitExceeded");
    expect(job).toHaveTextContent("2 runs, 1 failed");
    expect(job).toHaveTextContent("5m 12s");
  });

  it("names each card's environment with every environment in scope", async () => {
    renderOverview({ env: "all" });
    await ready();
    const table = screen.getByTestId("overview-services-table");
    expect(within(within(table).getByTestId("overview-service-admin-server-e2e")).getByText("e2e")).toBeInTheDocument();
    expect(screen.getByTestId("overview-counts")).toHaveTextContent("all environments");
  });

  it("filters services, and says so when nothing matches", async () => {
    const harness = renderOverview();
    await ready();
    await userEvent.click(screen.getByTestId("overview-chip-status"));
    expect(harness.current().status).toBe("attention");
    // The crash loop, the degraded service, and the process whose alerts are firing.
    expect(within(screen.getByTestId("overview-services-table")).getAllByRole("listitem").map((row) => row.dataset.testid)).toEqual([
      "overview-service-reliant-temporal-worker",
      "overview-service-litellm",
      "overview-service-process-api",
    ]);
    await userEvent.type(screen.getByTestId("overview-filter"), "nope");
    expect(screen.getByTestId("overview-services-empty")).toHaveTextContent("Nothing matches these filters");
  });

  it("lists jobs and telemetry-only processes in the same list, each with its kind and status", async () => {
    renderOverview({ env: "e2e" });
    await ready();
    const job = screen.getByTestId("overview-service-job-control-plane-migrate");
    expect(job).toHaveAttribute("data-kind", "job");
    expect(within(job).getByTestId("overview-row-kind")).toHaveTextContent("Job");
    // A failed latest run is the row's status and its reason, as it is in Needs attention.
    expect(job).toHaveAttribute("data-tone", "danger");
    expect(job).toHaveTextContent("Failed");
    expect(within(job).getByTestId("overview-row-why")).toHaveTextContent("Failed after exhausting its retries");
    expect(within(job).getByRole("link", { name: "control-plane-migrate" })).toHaveAttribute("href", "/w/e2e/control-plane-migrate");
    expect(screen.getByTestId("attention-job/e2e/control-plane-migrate")).toBeInTheDocument();
  });

  it("keeps what the Processes table alone showed: telemetry-only processes, error logs and firing alerts", async () => {
    renderOverview();
    await ready();
    // There is no separate Processes table on the Overview.
    expect(screen.queryByTestId("observability-services")).toBeNull();
    const api = await screen.findByTestId("overview-service-process-api");
    expect(within(api).getByTestId("overview-row-kind")).toHaveTextContent("Process");
    expect(within(api).getByTestId("overview-row-alerts")).toHaveTextContent("2 firing");
    const logs = within(api).getByTitle("Error logs from api");
    expect(logs.tagName).toBe("A");
    expect(logs.getAttribute("href")).toMatch(/logs.*service=api/);
    expect(within(api).getByTestId("service-card-red")).toHaveTextContent("req/s");
  });

  it("names what is missing for an environment Reliant has no record of", async () => {
    renderOverview({ env: "qa" });
    await waitFor(() => expect(screen.getByTestId("overview-unrecorded-env")).toHaveTextContent("Reliant has no record of qa"));
  });
});

describe("its states", () => {
  it("says nothing is deployed, leads with an agent, and keeps the command that deploys", async () => {
    renderOverview({ envs: [] });
    expect(screen.getByTestId("overview")).toHaveAttribute("data-state", "empty");
    expect(screen.getByText("Nothing deployed yet")).toBeInTheDocument();
    expect(within(screen.getByTestId("overview-getting-started-agent")).getByRole("button", { name: "Deploy with an agent" })).toBeInTheDocument();
    expect(screen.getByText("reliant forge env deploy <env>")).toBeInTheDocument();
  });

  it("never says nothing is deployed when the deploy state could not be read", async () => {
    const onRetry = vi.fn();
    renderOverview({ envs: [], envsUnread: { onRetry, retrying: false } });
    expect(screen.getByTestId("overview")).toHaveAttribute("data-state", "unread");
    expect(screen.getByTestId("overview-unread")).toHaveTextContent("Couldn't load deployment status");
    expect(screen.queryByText("Nothing deployed yet")).toBeNull();
    expect(screen.queryByTestId("overview-counts")).toBeNull();
    await userEvent.setup().click(screen.getByTestId("overview-unread-retry"));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("offers Ask Reliant on every row that needs attention", async () => {
    renderOverview();
    await ready();
    const rows = within(screen.getByTestId("overview-attention")).getAllByRole("row");
    expect(rows.length).toBeGreaterThan(0);
    for (const row of rows) expect(within(row).getByTestId("ask-reliant-menu")).toBeInTheDocument();
  });

  it("loads at the tables' own row height", () => {
    renderOverview({ obs: createWorkloadsFixtureApi("loading") });
    expect(screen.getByTestId("overview")).toHaveAttribute("data-state", "loading");
    expect(within(screen.getByTestId("overview-services-table")).getAllByText("Loading…").length).toBeGreaterThan(0);
  });

  it("explains an empty environment without calling it healthy", async () => {
    renderOverview({ obs: createWorkloadsFixtureApi("empty") });
    await ready();
    // No workload reported, so the list holds only what sends telemetry, each saying it has no workload behind it.
    const listed = within(screen.getByTestId("overview-services-table")).getAllByRole("listitem");
    expect(listed.length).toBeGreaterThan(0);
    expect(listed.every((row) => row.dataset.kind === "process" && row.dataset.tone === "neutral")).toBe(true);
    expect(screen.getByTestId("overview-services-table")).not.toHaveTextContent("Healthy");
    expect(screen.getByTestId("overview-jobs-empty")).toHaveTextContent("No job has run in prod");
    expect(screen.getByTestId("overview-env-prod")).toHaveTextContent("No data");
    // The alerts still firing are all that needs attention: no workload is listed as failing.
    const attention = within(screen.getByTestId("overview-attention")).getAllByRole("row");
    expect(attention.every((row) => row.getAttribute("data-testid")?.startsWith("attention-alert/"))).toBe(true);
  });

  it("keeps the page when telemetry cannot be read, and says what it cannot show", async () => {
    renderOverview({ obs: createWorkloadsFixtureApi("error") });
    // Unavailable is retried once before the page gives up.
    await waitFor(() => expect(screen.getByTestId("overview")).toHaveAttribute("data-state", "error"), { timeout: 5000 });
    expect(screen.getByTestId("overview-env-prod")).toHaveTextContent("health unavailable");
    expect(screen.getByTestId("overview-attention")).toHaveTextContent("Workload health could not be read");
    // Each tile the read feeds says so with its own Try again (OverviewTiles.test.tsx).
    expect(within(screen.getByTestId("overview-services")).getByRole("button", { name: /try again|retry/i })).toBeInTheDocument();
    expect(within(screen.getByTestId("overview-jobs")).getByRole("button", { name: /try again|retry/i })).toBeInTheDocument();
  });

  it("still lists the firing alerts when the workload read times out", async () => {
    // 2026-11-03: ListWorkloads timed out and "Needs attention" showed only an
    // apology, though the alerts had been read and were firing.
    const base = createWorkloadsFixtureApi();
    renderOverview({
      obs: {
        ...base,
        listWorkloads: async () => {
          throw new ConnectError("ListWorkloads timed out after 10000ms", Code.DeadlineExceeded);
        },
      },
    });
    await waitFor(() => expect(screen.getByTestId("overview")).toHaveAttribute("data-state", "error"));
    const attention = screen.getByTestId("overview-attention");
    await waitFor(() => expect(within(attention).getAllByRole("row").length).toBeGreaterThan(0));
    const rows = within(attention).getAllByRole("row");
    expect(rows.every((row) => row.getAttribute("data-testid")?.startsWith("attention-alert/"))).toBe(true);
    expect(within(attention).getByTestId("overview-attention-unread")).toHaveTextContent("only the firing alerts are listed");
  });

  it("says when the jobs list was cut short", async () => {
    const base = createWorkloadsFixtureApi();
    renderOverview({ obs: { ...base, listWorkloads: async (req) => ({ ...(await base.listWorkloads(req)), jobsTruncated: true }) } });
    await ready();
    expect(screen.getByTestId("overview-jobs-truncated")).toHaveTextContent("newest 200 runs");
  });

  it("explains an organization whose telemetry store does not exist yet", async () => {
    renderOverview({ obs: createWorkloadsFixtureApi("not-set-up") });
    await waitFor(() => expect(screen.getByTestId("obs-not-set-up")).toBeInTheDocument());
  });
});
