// Copyright (c) 2025 Reliant Labs

/**
 * The Overview's tiles fail, retry and refresh on their own (tileState.tsx).
 *
 * The owner's repro (2026-11-05, prod web e76b1d21): one ListWorkloads timeout
 * replaced Services, Process health and Jobs with ONE "That query took too
 * long"; Retry flipped every tile to loading and the timeout reset the page;
 * a failed refresh replaced tables that had data.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";

import type { ListWorkloadsResult } from "@/services/controlPlane/observability/types";
import { createWorkloadsFixtureApi } from "../__fixtures__/workloadFixtures";
import { keepsStaleResult, sectionWords } from "../tileState";
import { renderOverview } from "./workloadsTestUtils";

vi.mock("uplot", async () => (await import("../../Observability/__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

const timeout = () => new ConnectError("ListWorkloads timed out after 14000ms", Code.DeadlineExceeded);

/** A promise and the functions that settle it. */
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe("the Overview's tiles", () => {
  it("a workload read that times out costs only its own tiles: Process health and the releases stay", async () => {
    const base = createWorkloadsFixtureApi();
    renderOverview({ obs: { ...base, listWorkloads: async () => Promise.reject(timeout()) } });

    await waitFor(() => expect(screen.getByTestId("overview-services-error")).toBeInTheDocument());
    expect(within(screen.getByTestId("overview-services-error")).getByText("That query took too long")).toBeInTheDocument();
    expect(screen.getByTestId("overview-jobs-error")).toBeInTheDocument();
    // Process health is its own read: it is drawn, with its rows, not hidden behind the workload failure.
    const processes = await screen.findByTestId("observability-services");
    await waitFor(() => expect(within(processes).getAllByRole("row").length).toBeGreaterThan(1));
    expect(screen.queryByTestId("process-health-error")).toBeNull();
    // The filters and the release rail are still there.
    expect(screen.getByTestId("overview-toolbar")).toBeInTheDocument();
    expect(screen.getByTestId("overview-recent-releases")).toBeInTheDocument();
  });

  it("a service overview that times out costs only Process health", async () => {
    const base = createWorkloadsFixtureApi();
    renderOverview({ obs: { ...base, getServiceOverview: async () => Promise.reject(timeout()) } });

    await waitFor(() => expect(screen.getByTestId("process-health-error")).toBeInTheDocument());
    expect(screen.getByTestId("overview")).toHaveAttribute("data-state", "ready");
    expect(within(screen.getByTestId("overview-services-table")).getAllByRole("row").length).toBeGreaterThan(1);
    expect(screen.queryByTestId("overview-services-error")).toBeNull();
  });

  it("Retry refetches only the failed read, and the tiles that had data keep it while it runs", async () => {
    const base = createWorkloadsFixtureApi();
    const calls: string[] = [];
    let workloadCalls = 0;
    const second = deferred<ListWorkloadsResult>();
    renderOverview({
      obs: {
        ...base,
        listWorkloads: (req) => {
          calls.push("ListWorkloads");
          workloadCalls += 1;
          return workloadCalls === 1 ? Promise.reject(timeout()) : second.promise.then(() => base.listWorkloads(req));
        },
        getServiceOverview: (req) => {
          calls.push("GetServiceOverview");
          return base.getServiceOverview(req);
        },
      },
    });
    await waitFor(() => expect(screen.getByTestId("overview-services-error")).toBeInTheDocument());
    const processes = screen.getByTestId("observability-services");
    await waitFor(() => expect(within(processes).getAllByRole("row").length).toBeGreaterThan(1));
    const overviewCalls = calls.filter((c) => c === "GetServiceOverview").length;

    await userEvent.click(within(screen.getByTestId("overview-services-error")).getByTestId("obs-retry"));

    // While the retry runs, Process health keeps its rows.
    await waitFor(() => expect(workloadCalls).toBe(2));
    expect(within(screen.getByTestId("observability-services")).getAllByRole("row").length).toBeGreaterThan(1);
    second.resolve({} as ListWorkloadsResult);
    await waitFor(() => expect(screen.getByTestId("overview")).toHaveAttribute("data-state", "ready"));
    expect(within(screen.getByTestId("overview-services-table")).getAllByRole("row").length).toBeGreaterThan(1);
    // Only the failed read went out again.
    expect(calls.filter((c) => c === "GetServiceOverview").length).toBe(overviewCalls);
  });

  it("a refresh that fails keeps the last result on screen and says so", async () => {
    const base = createWorkloadsFixtureApi();
    let workloadCalls = 0;
    renderOverview({
      obs: {
        ...base,
        listWorkloads: async (req) => {
          workloadCalls += 1;
          // The first answer is partial (so its note offers a Retry); the refresh it asks for fails.
          if (workloadCalls === 1) return { ...(await base.listWorkloads(req)), unavailableSections: ["history"] };
          throw timeout();
        },
      },
    });
    const partial = await screen.findByTestId("overview-services-partial");
    const rowsBefore = within(screen.getByTestId("overview-services-table")).getAllByRole("row").length;

    await userEvent.click(within(partial).getByTestId("overview-services-partial-retry"));

    await waitFor(() => expect(screen.getByTestId("overview-services-stale")).toBeInTheDocument());
    expect(within(screen.getByTestId("overview-services-table")).getAllByRole("row")).toHaveLength(rowsBefore);
    expect(screen.queryByTestId("overview-services-error")).toBeNull();
    expect(screen.getByTestId("overview")).toHaveAttribute("data-state", "ready");
  });

  it("a partial answer draws what arrived and names what did not, never as none", async () => {
    const base = createWorkloadsFixtureApi();
    renderOverview({
      obs: { ...base, listWorkloads: async (req) => ({ ...(await base.listWorkloads(req)), jobs: [], unavailableSections: ["jobs", "usage"] }) },
    });
    const note = await screen.findByTestId("overview-services-partial");
    expect(note).toHaveTextContent("CPU and memory did not arrive in time");
    expect(within(screen.getByTestId("overview-services-table")).getAllByRole("row").length).toBeGreaterThan(1);
    expect(screen.getByTestId("overview-jobs-partial")).toHaveTextContent("Jobs did not arrive in time");
    expect(screen.queryByTestId("overview-jobs-empty")).toBeNull();
  });

  it("the Services tile names every missing workload section, not only CPU and memory", async () => {
    const base = createWorkloadsFixtureApi();
    const sections = ["warnings", "replicas", "restarts", "terminations", "history", "versions"];
    renderOverview({
      obs: { ...base, listWorkloads: async (req) => ({ ...(await base.listWorkloads(req)), unavailableSections: sections }) },
    });
    const note = await screen.findByTestId("overview-services-partial");
    expect(note).toHaveTextContent("Warnings, replicas, restarts, last exits, health history and versions did not arrive in time");
    // None of them is jobs, so the Jobs tile draws its table.
    expect(screen.queryByTestId("overview-jobs-partial")).toBeNull();
  });
});

describe("tile state", () => {
  it("keeps the last result through a transient failure, but not through one that is true of the data", () => {
    expect(keepsStaleResult(timeout(), { rows: [] })).toBe(true);
    expect(keepsStaleResult(new ConnectError("x", Code.Unavailable), { rows: [] })).toBe(true);
    expect(keepsStaleResult(new ConnectError("x", Code.PermissionDenied), { rows: [] })).toBe(false);
    expect(keepsStaleResult(new ConnectError("observability is not set up for this organization yet", Code.FailedPrecondition), { rows: [] })).toBe(false);
    expect(keepsStaleResult(timeout(), undefined)).toBe(false);
  });

  it("names sections in words", () => {
    expect(sectionWords(["usage"])).toBe("CPU and memory");
    expect(sectionWords(["usage", "restarts", "jobs"])).toBe("CPU and memory, restarts and jobs");
  });
});
