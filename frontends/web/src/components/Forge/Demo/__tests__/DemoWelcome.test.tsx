// Copyright (c) 2025 Reliant Labs
import { afterEach, beforeEach, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from "@tanstack/react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createShopTransport } from "@/fixtures/shop";
import { installShellTransport } from "@/lib/connect";
import { DemoTips } from "../tips/DemoTips";
import { DemoHome } from "../sections/DemoHome";
import { demoEntryHref } from "../demoEntry";
import { DEMO_TIP_KEYS, demoTipKey } from "../tips/tipKeys";
import { resetDemoSessionForTests } from "../demoSession";
import { useNavDrawer } from "@/components/Layout/shell/navDrawer";

function mount(path = "/forge/p/shop/observability/logs?range=6h", entry = false) {
  const root = createRootRoute({ component: () => <><Outlet /><DemoTips /></> });
  const route = createRoute({ getParentRoute: () => root, path: "$", validateSearch: (s: Record<string, unknown>) => s, component: () => <div><header data-testid="forge-console-header" /><main data-testid="forge-console-page">{entry && <DemoHome />}<button>Page action</button></main></div> });
  const router = createRouter({ routeTree: root.addChildren([route]), history: createMemoryHistory({ initialEntries: [path] }) });
  // The Home's trace story reads the sample app (its slowest request's timeline), over the demo's transport.
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>);
  return router;
}
beforeEach(() => { resetDemoSessionForTests(); installShellTransport(createShopTransport()); });
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); installShellTransport(null); });

it("leads bare entry with the Home while preserving explicit legacy URLs", () => {
  expect(demoEntryHref("")).toBe("/forge/demo?section=home");
  expect(demoEntryHref("?rid=visitor&utm_source=site")).toBe("/forge/demo?rid=visitor&utm_source=site&section=home");
  expect(demoEntryHref("?tour=forge:fg-intro")).toBe("/forge/p/shop?tour=forge:fg-intro");
  expect(demoEntryHref("?view=logs")).toBe("/forge/p/shop?view=logs");
});
it("is one hero with an H1, then a choice that blocks nothing", async () => {
  const router = mount("/forge/demo?section=home", true);
  const home = await screen.findByTestId("demo-home");
  expect(within(home).getByRole("heading", { level: 1 })).toHaveTextContent("Ship your app safely, see what breaks, and let an agent fix it.");
  // The lede names what the console covers today: observability, product analytics, tests, security and data.
  expect(within(home).getByText(/gives it one console/)).toHaveTextContent(/session replays, who uses it, its tests, its security findings and its database/);
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(screen.queryByTestId("tour-callout")).toBeNull();
  fireEvent.click(within(home).getByTestId("home-primary"));
  await waitFor(() => expect(router.state.location.pathname).toBe("/forge/p/shop"));
  expect(router.state.location.search).not.toHaveProperty("tour");
});
it("starts the journey at its intro on the sample app's console, and Take the tour stays available", async () => {
  const router = mount("/forge/demo?section=home", true);
  fireEvent.click(within(await screen.findByTestId("demo-home")).getByRole("button", { name: "Take the tour" }));
  await waitFor(() => expect(router.state.location.search).toMatchObject({ tour: "forge:fg-intro" }));
  expect(router.state.location.pathname).toBe("/forge/p/shop");
  await act(async () => { await router.navigate({ href: "/forge/p/shop/releases" }); });
  fireEvent.click(within(screen.getByTestId("demo-screen-actions")).getByRole("button", { name: "Take the tour" }));
  await waitFor(() => expect(router.state.location.search).toMatchObject({ tour: "forge:fg-intro" }));
});
it("deep links never show a blocking welcome, even with session storage blocked", async () => {
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {throw new Error("blocked");});
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {throw new Error("blocked");});
  const router = mount();
  expect(await screen.findByTestId("tour-callout")).toBeVisible();
  expect(screen.queryByTestId("demo-welcome")).toBeNull();
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(router.state.location.search).toMatchObject({range:"6h"});
});
it("on a phone the tip is a closed one-line disclosure in the page's flow, hidden behind the drawer", async () => {
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("1023px"), media:query, onchange:null, addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn(),dispatchEvent:vi.fn() }));
  mount();
  const about = await screen.findByTestId("demo-page-info");
  expect(about.closest('[data-testid="demo-mobile-explanation"]')?.closest("main")).not.toBeNull();
  expect(screen.queryByTestId("tour-callout")).toBeNull();
  fireEvent.click(about);
  expect(await screen.findByTestId("tour-callout")).toBeVisible();
  // The shell's nav drawer (a phone's nav) open: the tip and the action row step aside.
  try {
    act(() => useNavDrawer.getState().setOpen(true));
    await waitFor(() => expect(screen.queryByTestId("demo-page-info")).toBeNull());
    expect(screen.queryByTestId("demo-screen-actions")).toBeNull();
    act(() => useNavDrawer.getState().setOpen(false));
    expect(await screen.findByTestId("demo-page-info")).toBeVisible();
  } finally { act(() => useNavDrawer.getState().setOpen(false)); }
});
it("every page has an explanation, and none repeats the build-page action", async () => {
  const paths = ["/forge/p/shop", "/forge/p/shop/releases", "/forge/p/shop/releases/v1", "/forge/p/shop/environments", "/forge/p/shop/environments/prod", "/forge/p/shop/secrets", "/forge/p/shop/domains", "/forge/p/shop/activity", "/forge/p/shop/users", "/forge/p/shop/workloads/prod/api", ...["services", "storage", "data", "security/vulnerabilities", "security/sboms", "tests", "tests/coverage", "tests/runs", "tests/flaky", "analytics", "analytics/events", "events", "experiments", "observability", "speed-insights"].map((page) => `/forge/p/shop/${page}`), ...["dashboards","errors","traces","logs","profiles","alerts","sessions","metrics","rpc","graph"].map(view=>`/forge/demo?view=${view}`), "/forge/demo?view=traces&trace=demo"];
  const visited = new Set<string>();
  for (const path of paths) {
    cleanup(); resetDemoSessionForTests(); mount(path);
    // A section's tip opens by itself; a sub-page's is one click on "?".
    await waitFor(() => expect(screen.queryByTestId("tour-callout") ?? screen.queryByTestId("demo-page-info")).not.toBeNull());
    if (!screen.queryByTestId("tour-callout")) fireEvent.click(screen.getByTestId("demo-page-info"));
    const card = await screen.findByTestId("tour-callout");
    expect(within(card).queryByRole("button", {name:/How (Forge builds this|it was built)/})).toBeNull();
    const url = new URL(path,"http://test");
    visited.add(demoTipKey(url.pathname, Object.fromEntries(url.searchParams))!);
  }
  expect([...visited].sort()).toEqual([...DEMO_TIP_KEYS].sort());
});
