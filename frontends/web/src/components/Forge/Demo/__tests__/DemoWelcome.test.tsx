// Copyright (c) 2025 Reliant Labs
import { afterEach, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from "@tanstack/react-router";
import { DemoTips } from "../tips/DemoTips";
import { DemoWelcome } from "../DemoWelcome";
import { demoEntryHref } from "../demoEntry";
import { DEMO_TIP_KEYS, demoTipKey } from "../tips/tipKeys";

function mount(path = "/forge/p/shop/observability/logs?range=6h", entry = false) {
  const root = createRootRoute({ component: () => <><Outlet /><DemoTips /></> });
  const route = createRoute({ getParentRoute: () => root, path: "$", validateSearch: (s: Record<string, unknown>) => s, component: () => <main>{entry && <DemoWelcome />}<button>Page action</button></main> });
  const router = createRouter({ routeTree: root.addChildren([route]), history: createMemoryHistory({ initialEntries: [path] }) });
  render(<RouterProvider router={router} />);
  return router;
}
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("leads bare entry with the visual build story while preserving explicit legacy URLs", () => {
  expect(demoEntryHref("")).toBe("/forge/demo?section=built");
  expect(demoEntryHref("?rid=visitor&utm_source=site")).toBe("/forge/demo?rid=visitor&utm_source=site&section=built");
  expect(demoEntryHref("?tour=forge:fg-intro")).toBe("/forge/p/shop?tour=forge:fg-intro");
  expect(demoEntryHref("?view=logs")).toBe("/forge/p/shop?view=logs");
});
it("offers an inline summary and a choice without blocking the build", async () => {
  const router = mount("/forge/demo?section=built", true);
  const welcome = await screen.findByTestId("demo-welcome");
  expect(welcome).toHaveTextContent("production framework");
  expect(welcome).toHaveTextContent("read-only sample data");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(screen.queryByTestId("tour-callout")).toBeNull();
  fireEvent.click(within(welcome).getByRole("button", {name:"Explore the running app"}));
  await waitFor(() => expect(router.state.location.pathname).toBe("/forge/p/shop"));
  expect(router.state.location.search).not.toHaveProperty("tour");
});
it("starts the existing journey at its intro, and Start tour stays available", async () => {
  const router = mount("/forge/demo?section=built", true);
  fireEvent.click(await screen.findByRole("button", {name:"Take a guided tour"}));
  await waitFor(() => expect(router.state.location.search).toMatchObject({tour:"forge:fg-intro",view:"overview"}));
  expect(router.state.location.pathname).toBe("/forge/demo");
  expect(screen.queryByTestId("tour-callout")).toBeNull();
  await act(async () => { await router.navigate({href:"/forge/p/shop/releases"}); });
  fireEvent.click(screen.getByRole("button", {name:"Start tour"}));
  await waitFor(() => expect(router.state.location.search).toMatchObject({tour:"forge:fg-intro"}));
});
it("deep links never show a blocking welcome, regardless of old/blocked session storage", async () => {
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {throw new Error("blocked");});
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {throw new Error("blocked");});
  const router = mount();
  expect(await screen.findByTestId("tour-callout")).toBeVisible();
  expect(screen.queryByTestId("demo-welcome")).toBeNull();
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(router.state.location.search).toMatchObject({range:"6h"});
  cleanup(); mount(); await screen.findByTestId("tour-callout");
  expect(screen.queryByRole("dialog")).toBeNull();
});
it("How help preserves URL, pauses playback, and restores trigger focus on Escape", async () => {
  const router = mount();
  const card = await screen.findByTestId("tour-callout");
  const trigger = within(card).getByRole("button", {name:"How Forge builds this"});
  trigger.focus(); fireEvent.click(trigger);
  expect(await screen.findByTestId("how-built")).toBeVisible();
  expect(screen.getAllByRole("dialog")).toHaveLength(1);
  expect(screen.queryByRole("button", {name:"Pause"})).toBeNull();
  fireEvent.keyDown(document.activeElement!, {key:"Escape"});
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(trigger).toHaveFocus();
  expect(router.state.location.pathname).toBe("/forge/p/shop/observability/logs");
  expect(screen.getByTestId("tour-callout")).toBeVisible();
});
it("mobile explanations AND their collapsed control are in flow, hidden behind the drawer", async () => {
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("767px"), media:query, onchange:null, addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn(),dispatchEvent:vi.fn() }));
  mount();
  const card = await screen.findByTestId("tour-callout");
  expect(card.closest("main")).not.toBeNull();
  expect(card.closest('[data-testid="demo-mobile-explanation"]')).not.toBeNull();
  fireEvent.click(screen.getByTestId("tour-next"));
  const about = await screen.findByTestId("demo-page-info");
  expect(about.closest("main")).not.toBeNull();
  fireEvent.click(about);
  const drawer = document.createElement("nav"); drawer.id="sidebar-layout-nav"; document.body.appendChild(drawer);
  try {
    act(() => drawer.setAttribute("aria-modal", "true"));
    await waitFor(() => expect(screen.queryByTestId("tour-callout")).toBeNull());
    expect(screen.queryByTestId("demo-screen-actions")).toBeNull();
    act(() => drawer.removeAttribute("aria-modal"));
    expect(await screen.findByTestId("tour-callout")).toBeVisible();
  } finally { drawer.remove(); }
});
it("every page explanation includes the shared How CTA", async () => {
  const paths = ["/forge/p/shop", "/forge/p/shop/releases", "/forge/p/shop/releases/v1", "/forge/p/shop/environments", "/forge/p/shop/environments/prod", "/forge/p/shop/secrets", "/forge/p/shop/domains", "/forge/p/shop/activity", "/forge/p/shop/workloads/prod/api", ...["dashboards","errors","traces","logs","profiles","alerts","sessions","metrics","rpc","graph"].map(view=>`/forge/demo?view=${view}`), "/forge/demo?view=traces&trace=demo"];
  const visited = new Set<string>();
  for (const path of paths) {
    cleanup(); mount(path);
    const card = await screen.findByTestId("tour-callout");
    expect(within(card).getByRole("button", {name:"How Forge builds this"})).toBeVisible();
    const url = new URL(path,"http://test");
    visited.add(demoTipKey(url.pathname, Object.fromEntries(url.searchParams))!);
  }
  expect([...visited].sort()).toEqual([...DEMO_TIP_KEYS].sort());
});
