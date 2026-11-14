// Copyright (c) 2025 Reliant Labs

/**
 * `/forge/demo` through its real route (createForgeDemoRoute, as both shells
 * mount it): it renders the console and every Observability view the forge
 * journey visits on fixture data, under a demo banner, and the whole walk —
 * a trace, its logs, its flamegraph, an issue, its Ask-an-agent sheet, the
 * firing alert — touches no transport, no fetch, no project list and no chat.
 */

import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryHistory, createRootRoute, createRouter, Outlet, RouterProvider } from "@tanstack/react-router";
import type { Transport } from "@connectrpc/connect";

const fetchProjects = vi.hoisted(() => vi.fn(async () => []));
vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  fetchProjects,
}));
const startAskReliantChat = vi.hoisted(() => vi.fn());
vi.mock("@/components/Forge/Observability/AskReliant/startAskReliantChat", () => ({ startAskReliantChat }));
// Every fixture API read, to see the scope the panel asked for.
const fixtureCalls = vi.hoisted(() => [] as { rpc: string; request: unknown }[]);
vi.mock("../../Observability/__fixtures__/fixtureApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../Observability/__fixtures__/fixtureApi")>();
  return {
    ...actual,
    createFixtureApi: (options: Parameters<typeof actual.createFixtureApi>[0] = {}) =>
      actual.createFixtureApi({ ...options, onCall: (rpc, request) => fixtureCalls.push({ rpc, request }) }),
  };
});
vi.mock("uplot", async () => (await import("../../Observability/__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("../../Observability/__tests__/obsMocks")).obsMocks.virtual());

import { tourEvents } from "@/components/Onboarding/tourEvents";
import { setTransport } from "@/lib/connect";
import { resetShellForTests } from "@/lib/shell";

import { FIXTURE_TRACE_ID } from "../../Observability/__fixtures__/fixtureApi";
import { createForgeDemoRoute } from "../demoRoute";

/** Every RPC any client in the app makes goes through the routed transport; this one records and refuses. */
function recordingTransport(calls: string[]): Transport {
  const refuse = (method: { parent: { typeName: string }; name: string }) => {
    calls.push(`${method.parent.typeName}/${method.name}`);
    return Promise.reject(new Error(`the demo called ${method.parent.typeName}/${method.name}`));
  };
  return { unary: refuse, stream: refuse } as unknown as Transport;
}

function renderDemo(path: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([createForgeDemoRoute(() => rootRoute)]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(<RouterProvider router={router} />);
  return router;
}

const search = (router: ReturnType<typeof renderDemo>) => router.state.location.search as Record<string, unknown>;

describe("the forge demo console", () => {
  // The page is a lazy chunk (the whole Observability area and its fixtures): load it once, up front,
  // so the route renders as soon as it matches rather than racing a cold transform.
  beforeAll(async () => {
    await import("../DemoConsole");
  }, 120_000);

  const rpcCalls: string[] = [];
  let fetchSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    rpcCalls.length = 0;
    fixtureCalls.length = 0;
    setTransport(recordingTransport(rpcCalls));
    fetchSpy = vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("the demo fetched"));
    fetchProjects.mockClear();
    startAskReliantChat.mockClear();
  });

  afterEach(() => {
    setTransport(null);
    fetchSpy.mockRestore();
  });

  const expectNothingTouched = () => {
    expect(rpcCalls).toEqual([]);
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(fetchProjects).not.toHaveBeenCalled();
    expect(startAskReliantChat).not.toHaveBeenCalled();
  };

  it("renders the console's overview on fixture data, under the demo banner", async () => {
    renderDemo("/forge/demo?view=overview");
    expect(await screen.findByTestId("forge-demo-banner", {}, { timeout: 15_000 })).toHaveTextContent("Demo data");
    expect(screen.getByTestId("forge-demo-banner")).toHaveTextContent("Sample data from a made-up app called shop. Nothing here touches your projects.");
    // The notice belongs to the document flow, not a translucent sticky layer
    // that paints over filters and chart actions as the page scrolls.
    expect(screen.getByTestId("forge-demo-banner")).toHaveClass("bg-surface");
    expect(screen.getByTestId("forge-demo-banner").className).not.toMatch(/sticky|fixed|bg-warning-surface/);
    expect(await screen.findByTestId("obs-overview")).toBeInTheDocument();
    // The console's nav (the journey's first spotlight), every row kept on the demo.
    const nav = document.querySelector<HTMLElement>("[data-tour='forge-console'] aside");
    expect(nav).not.toBeNull();
    expect(within(nav!).getByRole("link", { name: /Overview/ })).toHaveAttribute("href", "/forge/demo");
    expect(within(nav!).getByRole("link", { name: /Releases/ })).toHaveAttribute("href", "/forge/demo");
    expectNothingTouched();
  }, 30_000);

  it("opens How this app was built from the banner, on the recorded run, and goes back to the console", async () => {
    const router = renderDemo("/forge/demo?view=overview");
    fireEvent.click(await screen.findByTestId("forge-demo-how-built", {}, { timeout: 15_000 }));
    await waitFor(() => expect(search(router).section).toBe("built"));
    expect(await screen.findByTestId("scaffold-replay", {}, { timeout: 15_000 })).toBeInTheDocument();
    expect(screen.getByTestId("scaffold-headline")).toHaveTextContent(/\d+ lines of API definitions/);
    expect(screen.queryByTestId("obs-overview")).toBeNull();
    // Already open: the banner no longer offers it.
    expect(screen.queryByTestId("forge-demo-how-built")).toBeNull();
    fireEvent.click(screen.getByTestId("how-built-back"));
    await waitFor(() => expect(search(router).section).toBeUndefined());
    expect(await screen.findByTestId("obs-overview")).toBeInTheDocument();
    // It is also last on the page, after the console's view.
    const link = screen.getByTestId("how-built-link");
    expect(screen.getByTestId("obs-overview").compareDocumentPosition(link) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    fireEvent.click(link);
    await waitFor(() => expect(search(router).section).toBe("built"));
    expect(await screen.findByTestId("scaffold-replay")).toBeInTheDocument();
    expectNothingTouched();
  }, 30_000);

  it("scopes the views to the environment picked by name, as the fixture's id", async () => {
    // The panel's `envs` are env NAMES (obsSearch.ts); the project place maps them to the fixture's ids.
    const router = renderDemo("/forge/demo?view=traces");
    await screen.findAllByTestId("obs-trace-row", {}, { timeout: 15_000 });
    const scopes = () => fixtureCalls.filter((c) => c.rpc === "ListTraces").map((c) => (c.request as { envIds: string[] }).envIds);
    expect(scopes()).toContainEqual([]);
    // What the console's environment picker writes (DemoConsole's onEnvChange): the name.
    fixtureCalls.length = 0;
    await router.navigate({ to: "/forge/demo", search: { view: "traces", envs: ["staging"] } as never });
    await waitFor(() => expect(scopes().length).toBeGreaterThan(0));
    for (const envIds of scopes()) expect(envIds).toEqual(["env-staging"]);
    expect(screen.getAllByText("staging").length).toBeGreaterThan(0);
    expectNothingTouched();
  }, 30_000);

  it("walks the journey's views with no daemon, chat or control-plane call", async () => {
    const user = userEvent.setup();
    // user-event installs its own clipboard; watch what lands on it.
    const writeText = vi.spyOn(navigator.clipboard, "writeText");
    const opened = vi.fn();
    const copied = vi.fn();
    const offOpened = tourEvents.on("askAgent:opened", opened);
    const offCopied = tourEvents.on("askAgent:copied", copied);

    const router = renderDemo("/forge/demo?view=traces&tour=forge:fg-trace");

    // F3: the toured trace, listed as failing, opens its waterfall.
    await screen.findAllByTestId("obs-trace-row", {}, { timeout: 15_000 });
    const row = document.querySelector<HTMLElement>(`[data-testid='obs-trace-row'][data-trace-id='${FIXTURE_TRACE_ID}']`)!;
    expect(row).not.toBeNull();
    expect(within(row).getByRole("button", { name: /Open trace .*errors/ })).toBeInTheDocument();
    await user.click(within(row).getByRole("button", { name: /Open trace/ }));
    await waitFor(() => expect(search(router).trace).toBe(FIXTURE_TRACE_ID));
    expect(await screen.findByTestId("trace-waterfall")).toBeInTheDocument();
    // The tour rides along every navigation.
    expect(search(router).tour).toBe("forge:fg-trace");

    // F4: its logs, five lines from three services.
    await user.click(screen.getByTestId("trace-logs"));
    await waitFor(() => expect(search(router)).toMatchObject({ view: "logs", trace: FIXTURE_TRACE_ID }));
    expect(await screen.findByTestId("obs-logs-trace-scope")).toBeInTheDocument();
    await waitFor(() => expect(screen.getAllByTestId("obs-log-row")).toHaveLength(5));

    // F5: back on the trace, its flamegraph.
    await router.navigate({ to: "/forge/demo", search: { view: "traces", trace: FIXTURE_TRACE_ID } as never });
    await user.click(await screen.findByTestId("trace-flamegraph"));
    await waitFor(() => expect(search(router).view).toBe("profiles"));
    expect(await screen.findByTestId("obs-profiles")).toBeInTheDocument();

    // F6: the issue.
    await router.navigate({ to: "/forge/demo", search: { view: "errors" } as never });
    await user.click(await screen.findByTestId("obs-issue-open-iss-orders-deadline"));
    await waitFor(() => expect(search(router).issue).toBe("iss-orders-deadline"));
    expect(await screen.findByTestId("obs-issue-header")).toBeInTheDocument();

    // F8: Ask an agent is the handoff, one button.
    const actions = screen.getByTestId("ask-reliant-actions");
    expect(within(actions).queryByTestId("ask-reliant-investigate")).toBeNull();
    expect(within(actions).queryByTestId("ask-reliant-open-chat")).toBeNull();
    await user.click(within(actions).getByTestId("ask-agent"));
    const sheet = await screen.findByTestId("ask-agent-sheet");
    expect(opened).toHaveBeenCalledWith({ subjectKind: "issue", mode: "handoff" });
    const prompt = within(sheet).getByTestId("ask-agent-prompt").textContent ?? "";
    expect(prompt).toContain("Issue iss-orders-deadline in environment prod (forge project shop).");
    expect(prompt).not.toMatch(/workspace/i);
    expect(screen.getByTestId("ask-agent-demo-note")).toBeInTheDocument();
    expect(screen.queryByTestId("ask-agent-open-in-reliant")).toBeNull();
    // Fix: still no workspace claimed.
    await user.click(within(sheet).getByTestId("ask-agent-intent-fix"));
    expect(within(sheet).getByTestId("ask-agent-prompt").textContent).toMatch(/Then fix it on a new branch/);
    expect(within(sheet).getByTestId("ask-agent-prompt").textContent).not.toMatch(/workspace/i);
    // Copy puts exactly what is shown on the clipboard.
    await user.click(within(sheet).getByTestId("ask-agent-copy-cmd-0"));
    expect(writeText).toHaveBeenLastCalledWith("reliant forge obs issue iss-orders-deadline prod");
    await user.click(within(sheet).getByTestId("ask-agent-copy-prompt"));
    expect(writeText).toHaveBeenLastCalledWith(within(sheet).getByTestId("ask-agent-prompt").textContent);
    expect(copied).toHaveBeenCalledWith({ what: "command" });
    expect(copied).toHaveBeenCalledWith({ what: "prompt" });
    await user.click(screen.getByRole("button", { name: "Done" }));

    // F7: the firing alert.
    await router.navigate({ to: "/forge/demo", search: { view: "alerts" } as never });
    expect(await screen.findByTestId("obs-alerts-firing")).toBeInTheDocument();
    expect(screen.getByTestId("forge-demo-banner")).toBeInTheDocument();

    offOpened();
    offCopied();
    expectNothingTouched();
  }, 60_000);

  it("turns every write off, visibly: triage, assignee, comments and alerting", async () => {
    renderDemo("/forge/demo?view=errors&issue=iss-orders-deadline");
    expect(await screen.findByTestId("obs-issue-header", {}, { timeout: 15_000 })).toBeInTheDocument();
    const resolve = await screen.findByTestId("obs-issue-resolve");
    expect(resolve).toBeDisabled();
    expect(resolve).toHaveAttribute("title", "Read-only demo");
    expect(screen.getByTestId("obs-issue-ignore")).toBeDisabled();
    expect(await screen.findByTestId("obs-issue-assignee-pick")).toBeDisabled();
    const box = await screen.findByTestId("obs-comment-box");
    expect(box).toHaveAttribute("readonly");
    expect(box).toHaveAttribute("placeholder", "Comments are off in the demo.");
    expect(screen.getByTestId("obs-comment-submit")).toBeDisabled();

    renderDemo("/forge/demo?view=alerts");
    expect(await screen.findByTestId("obs-alerts-read-only")).toHaveTextContent("This is a demo");
    expectNothingTouched();
  }, 30_000);

  describe("on the demo host", () => {
    beforeEach(() => resetShellForTests("demo"));
    afterEach(() => resetShellForTests());

    it("offers Install forge and Sign up instead of Leave the demo, and says what it records", async () => {
      const user = userEvent.setup();
      renderDemo("/forge/demo?view=overview");
      const banner = await screen.findByTestId("forge-demo-banner", {}, { timeout: 15_000 });
      expect(banner).toHaveTextContent("Nothing you click here changes anything.");
      expect(within(banner).queryByTestId("forge-demo-exit")).toBeNull();
      expect(within(banner).queryByTestId("demo-cta-signup")).toBeNull();
      expect(within(banner).getByTestId("forge-demo-telemetry-notice")).toHaveTextContent("This demo records usage and session replays to improve the product.");
      expect(within(banner).queryByTestId("demo-cta-install")).toBeNull();
      void user; // Screen actions are mounted by DemoTips at the shell root, not the banner.
      expectNothingTouched();
    }, 30_000);
  });
});
