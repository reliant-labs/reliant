// Copyright (c) 2025 Reliant Labs

/**
 * What the console does when the org's hourly query allowance runs out, through
 * the real hooks, the real provider and the real banner, with fake timers:
 *
 *   - a view that has data KEEPS it (marked paused, "as of"), instead of blanking;
 *   - polling stops until the reset, then resumes on its own, a jittered 0-30s later;
 *   - one banner says it, however many views are watching;
 *   - N observers of one key poll once; a hidden tab does not poll;
 *   - a tight (soft) quota slows polls instead of stopping them;
 *   - background polls are the only reads marked as polls.
 */

import { act, render, renderHook, screen, within } from "@testing-library/react";
import { QueryClientProvider, focusManager, useQuery, type QueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createTestQueryClient } from "@/test/renderWithQuery";
import { resolveBillingTab, settingsSearchSchema } from "@/routeSchemas";
import { obsKeys, useServiceOverview } from "@/hooks/observability-queries";
import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { ObsQuotaProvider, RESUME_JITTER_MS, useObsQuota } from "@/services/controlPlane/observability/ObsQuotaContext";
import { formatClock, quotaPausedError } from "@/services/controlPlane/observability/quota";
import { SOFT_POLL_FACTOR, isPollRead } from "@/services/controlPlane/observability/quotaStore";
import type { ObsScope } from "@/services/controlPlane/observability/types";

import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT } from "../__fixtures__/fixtureApi";
import { ObservabilityHome, type ObservabilityHomeHrefs } from "../Home/ObservabilityHome";
import { ObsAsOf, ObsQuotaBanner } from "../ObsStates";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());

const SCOPE: ObsScope = { orgId: "org-fixture", envIds: ["env-prod"], filters: [] };
const START = Date.UTC(2026, 9, 26, 17, 51, 27);
const RESET = Date.UTC(2026, 9, 26, 18, 0, 0);
const POLL = 60_000;

// Real views read both during render. React Query only notifies an observer
// when a property it reads changes; reading error for the first time outside
// render after a refetch cannot retroactively subscribe to that change.
function useOverview(scope: ObsScope | null, range: "1h" | "6h") {
  const result = useServiceOverview(scope, range);
  void result.data;
  void result.error;
  return result;
}

interface Rig {
  client: QueryClient;
  calls: { poll: boolean }[];
  api: ObservabilityApi;
  /** What the next call does: "ok" answers, "quota"/"soft" refuse the way the server does. */
  mode: { value: "ok" | "quota" | "soft" };
  wrapper: ({ children }: { children: ReactNode }) => ReactNode;
}

function rig(random = () => 0.5): Rig {
  const client = createTestQueryClient();
  const calls: Rig["calls"] = [];
  const mode: Rig["mode"] = { value: "ok" };
  const base = createFixtureApi();
  const api: ObservabilityApi = {
    ...base,
    getServiceOverview: async (req) => {
      calls.push({ poll: isPollRead() });
      if (mode.value !== "ok") {
        throw quotaPausedError({ limit: "queries", lane: "interactive", resetsAtMs: RESET, soft: mode.value === "soft" });
      }
      return base.getServiceOverview(req);
    },
  };
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <ObservabilityApiProvider api={api}>
        <ObsQuotaProvider random={random}>{children}</ObsQuotaProvider>
      </ObservabilityApiProvider>
    </QueryClientProvider>
  );
  return { client, calls, api, mode, wrapper };
}

const flush = () => act(async () => void (await vi.advanceTimersByTimeAsync(1)));
const advance = (ms: number) => act(async () => void (await vi.advanceTimersByTimeAsync(ms)));

/** Quota exhaustion is a soft-reporting error too; "soft" mode reuses the exhausted shape with the soft reason. */
beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(START);
  focusManager.setFocused(true);
});
afterEach(() => {
  focusManager.setFocused(undefined);
  vi.useRealTimers();
});

describe("polling", () => {
  it("shares a query key across duplicate tiles despite different titles and layout", () => {
    const panel = { id: "first", title: "Requests", kind: "line", signal: "traces", series: [{ label: "requests", agg: "count" }] } as const;
    // The request is the same computation; presentation isn't part of its identity.
    const a = { ...panel, series: [...panel.series] };
    const b = { ...a, id: "second", title: "Another title", description: "Different layout", width: 12, height: 4 };
    expect(obsKeys.panel(SCOPE, "1h", a, [], [])).toEqual(obsKeys.panel(SCOPE, "1h", b, [], []));
    expect(obsKeys.panel(SCOPE, "1h", a, [], [])).not.toEqual(obsKeys.panel(SCOPE, "6h", b, [], []));
  });

  it("polls on the interval, and marks only the polls as polls", async () => {
    const r = rig();
    renderHook(() => useOverview(SCOPE, "1h"), { wrapper: r.wrapper });
    await flush();
    expect(r.calls).toEqual([{ poll: false }]);

    await advance(POLL);
    await advance(POLL);
    expect(r.calls).toEqual([{ poll: false }, { poll: true }, { poll: true }]);
  });

  it("polls once per interval however many views watch the same query", async () => {
    const r = rig();
    renderHook(
      () => {
        useOverview(SCOPE, "1h");
        useOverview(SCOPE, "1h");
        return useOverview(SCOPE, "1h");
      },
      { wrapper: r.wrapper }
    );
    await flush();
    await advance(POLL);
    // One read to load, one poll: three observers did not make three of either.
    expect(r.calls).toHaveLength(2);
  });

  it("does not poll in a hidden tab", async () => {
    const r = rig();
    renderHook(() => useOverview(SCOPE, "1h"), { wrapper: r.wrapper });
    await flush();
    focusManager.setFocused(false);
    await advance(POLL * 3);
    expect(r.calls).toHaveLength(1);
  });

  it("stops polling an unmounted view", async () => {
    const r = rig();
    const view = renderHook(() => useOverview(SCOPE, "1h"), { wrapper: r.wrapper });
    await flush();
    view.unmount();
    await advance(POLL * 3);
    expect(r.calls).toHaveLength(1);
  });
});

describe("when the allowance runs out", () => {
  it("recovers every refused non-polled panel once when a manual read succeeds during reset jitter", async () => {
    const r = rig(() => 0.5);
    const reads = [vi.fn(async () => "trace"), vi.fn(async () => "issue"), vi.fn(async () => "profile")];
    let releaseManual!: (value: string) => void;
    const manualRead = vi.fn(() => new Promise<string>((resolve) => { releaseManual = resolve; }));
    const invalidate = vi.spyOn(r.client, "invalidateQueries");
    const cancel = vi.spyOn(r.client, "cancelQueries");
    const panel = (name: string, read: () => Promise<string>) => ({
      queryKey: ["observability", "recovery-test", name],
      queryFn: () => store.guard(read),
      retry: false as const,
      staleTime: Infinity,
    });
    let store!: ReturnType<typeof useObsQuota>["store"];
    const view = renderHook(() => {
      store = useObsQuota().store;
      const trace = useQuery(panel("trace", reads[0]));
      const issue = useQuery(panel("issue", reads[1]));
      const profile = useQuery(panel("profile", reads[2]));
      const manual = useQuery({ ...panel("manual", manualRead), enabled: false });
      for (const query of [trace, issue, profile]) { void query.data; void query.error; }
      return { trace, issue, profile, manual };
    }, { wrapper: r.wrapper });
    await flush();
    // Refuse these distinct mounted keys locally, without any server reads.
    await act(async () => {
      store.report({ limit: "queries", lane: "interactive", resetsAtMs: RESET, soft: false });
      await Promise.all([view.result.current.trace.refetch(), view.result.current.issue.refetch(), view.result.current.profile.refetch()]);
    });
    await flush();
    expect(view.result.current.trace.error).not.toBeNull();
    const before = reads.map((read) => read.mock.calls.length);

    await advance(RESET - Date.now() + 1_000); // still inside the 15s jitter
    const manual = view.result.current.manual.refetch();
    await act(async () => { releaseManual("manual"); await manual; });
    await flush();
    await advance(RESUME_JITTER_MS);
    await flush();
    expect(view.result.current.trace.data).toBe("trace");
    expect(view.result.current.issue.data).toBe("issue");
    expect(view.result.current.profile.data).toBe("profile");
    expect(view.result.current.trace.error).toBeNull();
    expect(view.result.current.issue.error).toBeNull();
    expect(view.result.current.profile.error).toBeNull();
    reads.forEach((read, i) => expect(read).toHaveBeenCalledTimes(before[i] + 1));
    expect(manualRead).toHaveBeenCalledTimes(1);
    expect(invalidate).toHaveBeenCalledTimes(1);
    expect(cancel).not.toHaveBeenCalled();
    await advance(RESUME_JITTER_MS * 2);
    expect(invalidate).toHaveBeenCalledTimes(1);
    reads.forEach((read, i) => expect(read).toHaveBeenCalledTimes(before[i] + 1));
  });

  it("does not cancel or restart an explicit read still in flight when recovery invalidates its error", async () => {
    const r = rig(() => 0.5);
    let finish!: (value: string) => void;
    const read = vi.fn(async () => "loaded");
    let store!: ReturnType<typeof useObsQuota>["store"];
    const view = renderHook(() => {
      store = useObsQuota().store;
      const query = useQuery({ queryKey: ["observability", "in-flight"], queryFn: () => store.guard(read), retry: false });
      void query.data; void query.error;
      return query;
    }, { wrapper: r.wrapper });
    await flush();
    await act(async () => {
      store.report({ limit: "queries", lane: "interactive", resetsAtMs: RESET, soft: false });
      await view.result.current.refetch();
    });
    await flush();
    expect(view.result.current.error).not.toBeNull();
    read.mockImplementationOnce(() => new Promise<string>((resolve) => { finish = resolve; }));
    await advance(RESET - Date.now() + 1_000);
    const pending = view.result.current.refetch();
    await advance(RESUME_JITTER_MS);
    expect(read).toHaveBeenCalledTimes(2); // initial + explicit; no restarted read
    await act(async () => { finish("recovered"); await pending; });
    await flush();
    expect(view.result.current.data).toBe("recovered");
    expect(view.result.current.error).toBeNull();
  });

  it("does not loop recovery when the server repeats an already recovered quota interval", async () => {
    const r = rig(() => 0);
    const invalidate = vi.spyOn(r.client, "invalidateQueries");
    renderHook(() => useOverview(SCOPE, "1h"), { wrapper: r.wrapper });
    await flush();
    r.mode.value = "quota";
    await advance(POLL);
    await advance(RESET - Date.now());
    await flush();
    expect(invalidate).toHaveBeenCalledTimes(1);
    const attempts = r.calls.length;
    await advance(RESUME_JITTER_MS * 2);
    expect(invalidate).toHaveBeenCalledTimes(1);
    expect(r.calls).toHaveLength(attempts);
  });

  it("retains main's process-health UI and error-log links with an as-of badge during the pause", async () => {
    const r = rig();
    const hrefs: ObservabilityHomeHrefs = {
      logs: (search = {}) => `/logs?range=${search.range}&service=${search.service}&severity=${search.severity}`,
      traces: () => "/traces",
      profiles: () => "/profiles",
      alerts: "/alerts",
      dashboards: "/dashboards",
      workload: (name) => `/workloads/${name}`,
      release: (version) => `/releases/${version}`,
      releases: "/releases",
    };
    render(
      <ObservabilityHome project={FIXTURE_PROJECT} env="prod" environments={FIXTURE_ENVS} range="1h" onRange={vi.fn()} hrefs={hrefs} navigateTo={vi.fn()} />,
      { wrapper: r.wrapper }
    );
    await flush();
    const table = screen.getByTestId("observability-services");
    expect(within(table).getByRole("heading", { name: "Process health" })).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: "Memory" })).toBeInTheDocument();
    const rows = within(table).getAllByTestId(/^observability-service-/);
    expect(within(table).queryByTestId("obs-as-of")).not.toBeInTheDocument();

    r.mode.value = "quota";
    await advance(POLL);
    await flush();
    expect(within(table).getAllByTestId(/^observability-service-/)).toEqual(rows);
    expect(within(table).getByTitle("Error logs from api")).toHaveAttribute("href", "/logs?range=1h&service=api&severity=error,fatal");
    expect(within(table).getByTestId("obs-as-of")).toHaveTextContent(`as of ${formatClock(START)}`);
    expect(within(table).getByTestId("obs-as-of")).toHaveTextContent(`query budget resets at ${formatClock(RESET)}`);
  });

  it("keeps the last good result, stops polling, and resumes on its own after the reset", async () => {
    const r = rig(() => 0.5);
    const view = renderHook(() => useOverview(SCOPE, "1h"), { wrapper: r.wrapper });
    await flush();
    const good = view.result.current.data;
    expect(good).toBeDefined();

    // The next poll is refused.
    r.mode.value = "quota";
    await advance(POLL);
    await flush(); // observers are notified on a 0ms timer after the fetch settles
    expect(view.result.current.error).not.toBeNull();
    expect(view.result.current.data).toBe(good); // the view still has its result
    const callsWhenRefused = r.calls.length;

    // Nothing is sent for the rest of the hour, however long the tab stays open.
    await advance(RESET - Date.now() - 1_000);
    expect(r.calls).toHaveLength(callsWhenRefused);

    // The reset passes; this tab waits its jitter (0.5 * 30s) before it resumes.
    r.mode.value = "ok";
    await advance(1_000 + RESUME_JITTER_MS * 0.5 - 1_000);
    expect(r.calls).toHaveLength(callsWhenRefused);
    await advance(1_001);
    expect(r.calls.length).toBeGreaterThan(callsWhenRefused);
    expect(view.result.current.error).toBeNull();

    // And it polls again afterwards.
    const resumed = r.calls.length;
    await advance(POLL);
    expect(r.calls.length).toBeGreaterThan(resumed);
  });

  it.each([
    ["the first tab, jitter 0", 0, 0],
    ["the last tab, jitter ~30s", 0.99, RESUME_JITTER_MS * 0.99],
  ])("spreads tabs over the jitter window instead of resuming them together: %s", async (_name, random, wait) => {
    const r = rig(() => random);
    renderHook(() => useOverview(SCOPE, "1h"), { wrapper: r.wrapper });
    await flush();
    r.mode.value = "quota";
    await advance(POLL);
    r.mode.value = "ok";
    const refused = r.calls.length;

    await advance(RESET - Date.now() + Math.max(0, wait - 1_000));
    if (wait === 0) expect(r.calls.length).toBeGreaterThan(refused);
    else expect(r.calls).toHaveLength(refused); // still waiting its turn

    await advance(2_000);
    expect(r.calls.length).toBeGreaterThan(refused);
  });

  it("answers a view that mounts during the pause without a request", async () => {
    const r = rig();
    const view = renderHook(
      ({ late }) => ({
        first: useOverview(SCOPE, "1h"),
        late: useOverview(late ? { ...SCOPE, envIds: ["env-staging"] } : null, "1h"),
      }),
      { wrapper: r.wrapper, initialProps: { late: false } }
    );
    await flush();
    r.mode.value = "quota";
    await advance(POLL);
    const refused = r.calls.length;

    view.rerender({ late: true });
    await flush();
    expect(r.calls).toHaveLength(refused); // it never reached the api
    expect(view.result.current.late.error).not.toBeNull();
  });

  it("does not show one window's result as another's: a changed window says it will load", async () => {
    const r = rig();
    const view = renderHook(({ range }) => useOverview(SCOPE, range), {
      wrapper: r.wrapper,
      initialProps: { range: "1h" } as { range: "1h" | "6h" },
    });
    await flush();
    r.mode.value = "quota";
    await advance(POLL);
    await flush();
    expect(view.result.current.data).toBeDefined(); // same window: kept

    view.rerender({ range: "6h" });
    await flush();
    expect(view.result.current.data).toBeUndefined();
    expect(view.result.current.error).not.toBeNull();
  });

  it("slows polls to a quarter, instead of stopping them, while the quota is only tight", async () => {
    const r = rig();
    renderHook(() => useOverview(SCOPE, "1h"), { wrapper: r.wrapper });
    await flush();
    r.mode.value = "soft";
    await advance(POLL); // refused as soft
    const before = r.calls.length;
    r.mode.value = "ok";
    await advance(POLL * SOFT_POLL_FACTOR - 1_000);
    expect(r.calls).toHaveLength(before);
    await advance(2_000);
    expect(r.calls).toHaveLength(before + 1);
  });
});

function Reporter() {
  const { store } = useObsQuota();
  return (
    <button
      onClick={() => store.report({ limit: "queries", lane: "interactive", resetsAtMs: RESET, soft: false })}
      data-testid="report"
    >
      report
    </button>
  );
}

describe("the banner and the badge", () => {
  it("links the default usage destination to Billing's canonical Usage tab and mounted section", async () => {
    const r = rig();
    render(<><ObsQuotaBanner /><Reporter /></>, { wrapper: r.wrapper });
    await act(async () => screen.getByTestId("report").click());
    const destination = new URL(screen.getByTestId("obs-quota-usage").getAttribute("href")!, "https://app.example.test");
    const search = settingsSearchSchema.parse(Object.fromEntries(destination.searchParams));
    expect(destination.pathname).toBe("/settings/billing");
    expect(resolveBillingTab(search.tab)).toBe("usage");
    expect(destination.hash).toBe("#billing-observability-usage");
  });

  it("says it once, whatever is watching, and goes away at the reset", async () => {
    const r = rig();
    const Wrapper = r.wrapper;
    render(
      <Wrapper>
        <ObsQuotaBanner usageHref="/settings/billing" />
        <Reporter />
      </Wrapper>
    );
    expect(screen.queryByTestId("obs-quota-banner")).not.toBeInTheDocument();

    await act(async () => screen.getByTestId("report").click());
    const banners = screen.getAllByTestId("obs-quota-banner");
    expect(banners).toHaveLength(1);
    expect(banners[0]).toHaveTextContent(`Live updates are paused until ${formatClock(RESET)}`);
    expect(banners[0]).toHaveTextContent("Telemetry keeps arriving and nothing is lost");
    expect(banners[0]).toHaveTextContent("Alerts keep running on their own allowance");
    expect(screen.getAllByTestId("obs-quota-usage")[0]).toHaveAttribute("href", "/settings/billing");

    await advance(RESET - Date.now() + RESUME_JITTER_MS);
    expect(screen.queryByTestId("obs-quota-banner")).not.toBeInTheDocument();
  });

  it("marks a retained result as paused, as of when it loaded, with the reset time", async () => {
    const r = rig();
    function Probe() {
      const overview = useOverview(SCOPE, "1h");
      return overview.data ? <ObsAsOf updatedAt={overview.dataUpdatedAt} /> : <p>loading</p>;
    }
    const Wrapper = r.wrapper;
    render(
      <Wrapper>
        <Probe />
      </Wrapper>
    );
    await flush();
    expect(screen.queryByTestId("obs-as-of")).not.toBeInTheDocument(); // not while reads work
    const loadedAt = START;

    r.mode.value = "quota";
    await advance(POLL);
    const badge = screen.getByTestId("obs-as-of");
    expect(badge).toHaveTextContent(`as of ${formatClock(loadedAt)}`);
    expect(badge).toHaveTextContent(`query budget resets at ${formatClock(RESET)}`);
  });
});
