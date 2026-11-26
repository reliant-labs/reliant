// Copyright (c) 2025 Reliant Labs

/**
 * `/forge/demo` through its real route (createForgeDemoRoute, under the demo's
 * providers, as the demo shell mounts it): it renders the console's page bar
 * and every Observability view the
 * forge journey visits on fixture data, and the whole walk — a trace, its
 * logs, its flamegraph, an issue, its Ask-an-agent sheet, the firing alert —
 * touches no fetch, no project list and no chat. The console's real client
 * reads the shop pack over the demo's transport, and nothing else.
 */

import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryHistory, createRootRoute, createRouter, Outlet, RouterProvider } from "@tanstack/react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Transport } from "@connectrpc/connect";

const fetchProjects = vi.hoisted(() => vi.fn(async () => []));
vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  fetchProjects,
}));
const startAskReliantChat = vi.hoisted(() => vi.fn());
vi.mock("@/components/Forge/Observability/AskReliant/startAskReliantChat", () => ({ startAskReliantChat }));
// Every read of the demo's sample data, to see the scope the panel asked for.
const fixtureCalls = vi.hoisted(() => [] as { rpc: string; request: unknown }[]);
vi.mock("@/fixtures/shop/model/shopApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/fixtures/shop/model/shopApi")>();
  return {
    ...actual,
    createShopApi: (now?: number) => {
      const api = actual.createShopApi(now);
      const rpcName = (key: string) => key.charAt(0).toUpperCase() + key.slice(1);
      return Object.fromEntries(
        Object.entries(api).map(([key, fn]) => [key, (...args: unknown[]) => (fixtureCalls.push({ rpc: rpcName(key), request: args[0] }), (fn as (...a: unknown[]) => unknown)(...args))]),
      ) as unknown as typeof api;
    },
  };
});
// The demo bundle configures a control plane (config.demo.ts), so the console's real clients run.
vi.mock("@/services/controlPlane/config", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/config")>()),
  CONTROL_PLANE_API_URL: "https://cp.test",
  hasControlPlane: true,
}));
vi.mock("uplot", async () => (await import("../../Observability/__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("../../Observability/__tests__/obsMocks")).obsMocks.virtual());

import { tourEvents } from "@/components/Onboarding/tourEvents";
import { createShopTransport } from "@/fixtures/shop";
import { setTransport } from "@/lib/connect";
import { resetShellForTests } from "@/lib/shell";
import { DemoConsoleProviders } from "@/demoBundle/DemoConsoleProviders";
import { ObsReadOnlyProvider } from "../../Observability/obsReadOnly";

import { FIXTURE_TRACE_ID } from "../../Observability/__fixtures__/fixtureApi";
import { createForgeDemoRoute } from "../demoRoute";

/** What the demo shell installs (demoShellBoot.ts): the shop pack's transport, every read through it, recorded. */
function shopTransport(calls: string[]): Transport {
  const shop = createShopTransport();
  return {
    unary: (method, ...rest) => (calls.push(`${method.parent.typeName}/${method.name}`), shop.unary(method, ...rest)),
    stream: (method, ...rest) => (calls.push(`${method.parent.typeName}/${method.name}`), shop.stream(method, ...rest)),
  } as Transport;
}

function renderDemo(path: string) {
  // The demo shell's root: one query client, every write control off, and its
  // console layout's providers (demoShellRoutes.tsx).
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } });
  const rootRoute = createRootRoute({
    component: () => (
      <QueryClientProvider client={queryClient}>
        <ObsReadOnlyProvider value={{ readOnly: true, reason: "Read-only demo" }}>
          <DemoConsoleProviders>
            <Outlet />
          </DemoConsoleProviders>
        </ObsReadOnlyProvider>
      </QueryClientProvider>
    ),
  });
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

  const shopCalls: string[] = [];
  let fetchSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    resetShellForTests("demo");
    shopCalls.length = 0;
    fixtureCalls.length = 0;
    setTransport(shopTransport(shopCalls));
    fetchSpy = vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("the demo fetched"));
    fetchProjects.mockClear();
    startAskReliantChat.mockClear();
  });

  afterEach(() => {
    resetShellForTests();
    setTransport(null);
    fetchSpy.mockRestore();
  });

  const expectNothingTouched = () => {
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(fetchProjects).not.toHaveBeenCalled();
    expect(startAskReliantChat).not.toHaveBeenCalled();
  };

  it("renders the console's overview on fixture data, under the console's page bar and no frame of its own", async () => {
    renderDemo("/forge/demo?view=overview");
    expect(await screen.findByTestId("obs-overview", {}, { timeout: 15_000 })).toBeInTheDocument();
    // A page of the shell (the nav is the shell's: DemoNav): the console's bar, with the breadcrumb and the scope.
    expect(screen.getByTestId("forge-console-header")).toBeInTheDocument();
    expect(within(screen.getByRole("navigation", { name: "Breadcrumb" })).getByText("shop")).toBeInTheDocument();
    expect(screen.queryByTestId("forge-shell")).toBeNull();
    expect(document.querySelector("[data-tour='forge-console']")).toBeNull();
    expectNothingTouched();
  }, 30_000);

  it("scopes the views to the environment picked by name, as the fixture's id", async () => {
    // The panel's `envs` are env NAMES (obsSearch.ts); the project place maps them to the fixture's ids.
    const router = renderDemo("/forge/demo?view=traces");
    await screen.findAllByTestId("obs-trace-row", {}, { timeout: 15_000 });
    const scopes = () => fixtureCalls.filter((c) => c.rpc === "ListTraces").map((c) => (c.request as { envIds: string[] }).envIds);
    // It opens on prod, as the console's pages do (Console/envScope DEFAULT_ENV).
    expect(scopes()).toContainEqual(["env-prod"]);
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
    expect(writeText).toHaveBeenLastCalledWith("forge obs issue iss-orders-deadline prod");
    await user.click(within(sheet).getByTestId("ask-agent-copy-prompt"));
    expect(writeText).toHaveBeenLastCalledWith(within(sheet).getByTestId("ask-agent-prompt").textContent);
    expect(copied).toHaveBeenCalledWith({ what: "command" });
    expect(copied).toHaveBeenCalledWith({ what: "prompt" });
    await user.click(screen.getByRole("button", { name: "Done" }));

    // F7: the firing alert.
    await router.navigate({ to: "/forge/demo", search: { view: "alerts" } as never });
    expect(await screen.findByTestId("obs-alerts-firing")).toBeInTheDocument();

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

  describe("its own pages", () => {
    it("draws no banner (the action row says it once)", async () => {
      renderDemo("/forge/demo?view=overview");
      expect(await screen.findByTestId("obs-overview", {}, { timeout: 15_000 })).toBeInTheDocument();
      expect(screen.queryByTestId("forge-demo-banner")).toBeNull();
      // The overview's reads went to the shop pack, through the console's real client.
      expect(shopCalls).toEqual(expect.arrayContaining(["controlplane.v1.ObservabilityService/GetServiceOverview"]));
      expect(fixtureCalls.map((c) => c.rpc)).toContain("GetServiceOverview");
      expectNothingTouched();
    }, 30_000);

    it("names the build page in the breadcrumb, not Overview", async () => {
      renderDemo("/forge/demo?section=built");
      expect(await screen.findByTestId("how-built", {}, { timeout: 15_000 })).toBeInTheDocument();
      const crumbs = screen.getByRole("navigation", { name: "Breadcrumb" });
      expect(within(crumbs).getByText("How it was built")).toHaveAttribute("aria-current", "page");
      expect(within(crumbs).queryByText("Overview")).toBeNull();
    }, 30_000);

    it("orders the environments as the app does: prod, dev, staging, then All", async () => {
      renderDemo("/forge/demo?view=overview");
      const scope = await screen.findByTestId("env-scope", {}, { timeout: 15_000 });
      expect(within(scope).getAllByRole("button").map((b) => b.textContent)).toEqual(["prod", "dev", "staging", "All"]);
    }, 30_000);
  });
});
