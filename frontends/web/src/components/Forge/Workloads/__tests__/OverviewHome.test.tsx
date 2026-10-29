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
    expect(worker).toHaveTextContent(/14\s*· Ran out of memory/);
    expect(worker).not.toHaveTextContent("OOMKilled");
    // A workload with restarts that the server calls degraded reads Degraded (not Crash-looping).
    expect(within(table).getByTestId("overview-service-litellm")).toHaveTextContent("Degraded");
    // The whole row opens the workload, in its environment.
    expect(within(worker).getByRole("link", { name: /^reliant-temporal-worker\b/ })).toHaveAttribute("href", "/w/prod/reliant-temporal-worker");
    // Only prod's: no environment column, no other env's rows.
    expect(within(table).queryByText("Environment")).toBeNull();
    expect(within(table).getAllByRole("row")).toHaveLength(1 + 6);
    expect(screen.getByTestId("overview-counts")).toHaveTextContent("6 services and 1 job in prod");
    expect(within(table).getByRole("columnheader", { name: "Restarts 24h" })).toBeInTheDocument();
  });

  it("says when a release is declared by the promotion rather than verified by digest", async () => {
    renderOverview();
    await ready();
    const web = screen.getByTestId("overview-service-web");
    expect(web).toHaveTextContent("declared");
    expect(screen.getByTestId("overview-service-admin-server")).not.toHaveTextContent("declared");
    // No limit set: there is nothing to draw a meter against, and it says so.
    expect(within(web).getAllByTestId("usage-meter")[0]).toHaveTextContent("no limit");
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

  it("adds an Environment column with every environment in scope", async () => {
    renderOverview({ env: "all" });
    await ready();
    const table = screen.getByTestId("overview-services-table");
    expect(within(table).getByText("Environment")).toBeInTheDocument();
    expect(within(table).getByTestId("overview-service-admin-server-e2e")).toBeInTheDocument();
    expect(screen.getByTestId("overview-counts")).toHaveTextContent("all environments");
  });

  it("filters services, and says so when nothing matches", async () => {
    const harness = renderOverview();
    await ready();
    await userEvent.click(screen.getByTestId("overview-chip-status"));
    expect(harness.current().status).toBe("attention");
    expect(within(screen.getByTestId("overview-services-table")).getAllByRole("row")).toHaveLength(1 + 2);
    await userEvent.type(screen.getByTestId("overview-filter"), "nope");
    expect(screen.getByTestId("overview-services-empty")).toHaveTextContent("No service matches these filters");
  });

  it("names what is missing for an environment Reliant has no record of", async () => {
    renderOverview({ env: "qa" });
    await waitFor(() => expect(screen.getByTestId("overview-unrecorded-env")).toHaveTextContent("Reliant has no record of qa"));
  });
});

describe("its states", () => {
  it("says nothing is deployed, with the command that deploys", async () => {
    renderOverview({ envs: [] });
    expect(screen.getByTestId("overview")).toHaveAttribute("data-state", "empty");
    expect(screen.getByText("Nothing deployed yet")).toBeInTheDocument();
    expect(screen.getByText(/forge env deploy <env>/)).toBeInTheDocument();
  });

  it("loads at the tables' own row height", () => {
    renderOverview({ obs: createWorkloadsFixtureApi("loading") });
    expect(screen.getByTestId("overview")).toHaveAttribute("data-state", "loading");
    expect(within(screen.getByTestId("overview-services-table")).getAllByText("Loading…").length).toBeGreaterThan(0);
  });

  it("explains an empty environment without calling it healthy", async () => {
    renderOverview({ obs: createWorkloadsFixtureApi("empty") });
    await ready();
    expect(screen.getByTestId("overview-services-empty")).toHaveTextContent("No service has reported from prod");
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
    expect(screen.getByRole("button", { name: /try again|retry/i })).toBeInTheDocument();
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
