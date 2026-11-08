// Copyright (c) 2025 Reliant Labs
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from "@tanstack/react-router";
import { memoryTourStorage, setTourShell, setTourStorage } from "@/components/Onboarding/tourShell";
import { JourneyTour } from "@/components/Onboarding/JourneyTour";
import { resetShellForTests } from "@/lib/shell";
import { DemoTips } from "../tips/DemoTips";
import { demoSession, resetDemoSessionForTests } from "../demoSession";
import { DEMO_TIPS, DEMO_TIP_KEYS } from "../tips";

function renderDemo(path: string) {
  const root = createRootRoute({ component: () => <><Outlet /><DemoTips /><JourneyTour /></> });
  const any = createRoute({ getParentRoute: () => root, path: "$", component: () => <main data-testid="page"><h1>Console page</h1><div data-testid="obs-log-row" /><div data-testid="obs-trace-row" /><div data-testid="obs-alerts-firing" /><div data-testid="obs-issue-iss-orders-deadline" /></main>, validateSearch: (s: Record<string, unknown>) => s });
  const router = createRouter({ routeTree: root.addChildren([any]), history: createMemoryHistory({ initialEntries: [path] }) });
  render(<RouterProvider router={router} />);
  return { router, go: (to: string) => act(async () => { await router.navigate({ href: to }); }) };
}
beforeEach(() => { resetShellForTests("demo"); setTourShell("forge"); setTourStorage(memoryTourStorage()); resetDemoSessionForTests(); });
afterEach(() => { cleanup(); setTourShell(null); resetShellForTests(); });

it("opens this page's tour automatically on direct load, regardless of session seen/dismissed state", async () => {
  demoSession.markTipSeen("logs"); demoSession.disableTips();
  const {router} = renderDemo("/forge/p/shop/observability/logs");
  expect(await screen.findByTestId("tour-callout")).toHaveTextContent(DEMO_TIPS.logs.body);
  expect(screen.queryByTestId("tour-step-count")).toBeNull();
  expect(screen.queryByTestId("tour-exit")).toBeNull();
  expect(screen.getByTestId("tour-next")).toHaveTextContent("Done");
  expect(screen.getByTestId("tour-callout").querySelector('[data-testid="demo-cta-signup"]')).toBeNull();
  expect(screen.queryByText("Take the tour")).toBeNull();
  expect(screen.queryByTestId("onboarding-nav-bar")).toBeNull();
  expect((router.state.location.search as {tour?:string}).tour).toBeUndefined();
});
it("dismissal holds through same-page filters and rerenders, ? replays without navigation", async () => {
  const {go,router} = renderDemo("/forge/p/shop/observability/logs");
  await waitFor(() => fireEvent.click(screen.getByTestId("tour-next")));
  await waitFor(() => expect(screen.queryByTestId("tour-callout")).toBeNull());
  await go("/forge/p/shop/observability/logs?severity=error&range=6h");
  expect(screen.queryByTestId("tour-callout")).toBeNull();
  fireEvent(document.body, new Event("updated"));
  expect(screen.queryByTestId("tour-callout")).toBeNull();
  fireEvent.click(screen.getByRole("button", {name:"About Logs"}));
  expect(await screen.findByTestId("tour-callout")).toHaveTextContent(DEMO_TIPS.logs.body);
  expect(router.state.location.pathname).toBe("/forge/p/shop/observability/logs");
});
it("each actual page navigation and return starts a new page-local tour", async () => {
  const {go} = renderDemo("/forge/p/shop");
  expect(await screen.findByTestId("tour-callout")).toHaveTextContent(DEMO_TIPS.overview.body);
  fireEvent.click(screen.getByTestId("tour-next"));
  await go("/forge/p/shop/releases");
  expect(await screen.findByTestId("tour-callout")).toHaveTextContent(DEMO_TIPS.releases.body);
  fireEvent.click(screen.getByTestId("tour-next"));
  await go("/forge/p/shop/observability/logs");
  expect(await screen.findByTestId("tour-callout")).toHaveTextContent(DEMO_TIPS.logs.body);
  fireEvent.click(screen.getByTestId("tour-next"));
  await go("/forge/p/shop");
  expect(await screen.findByTestId("tour-callout")).toHaveTextContent(DEMO_TIPS.overview.body);
});
it("refresh starts again, even after dismissing the previous mount", async () => {
  renderDemo("/forge/p/shop/observability/errors");
  await waitFor(() => fireEvent.click(screen.getByTestId("tour-next"))); cleanup();
  renderDemo("/forge/p/shop/observability/errors");
  expect(await screen.findByTestId("tour-callout")).toHaveTextContent(DEMO_TIPS.errors.body);
});
it("drawer hides the callout, then closing it reveals the new page without consuming dismissal", async () => {
  const {go} = renderDemo("/forge/p/shop"); await screen.findByTestId("tour-callout");
  const drawer = document.createElement("nav"); drawer.id="sidebar-layout-nav"; document.body.appendChild(drawer);
  try {
    act(()=>drawer.setAttribute("aria-modal","true"));
    await waitFor(()=>expect(screen.queryByTestId("tour-callout")).toBeNull());
    await go("/forge/p/shop/observability/alerts");
    expect(screen.queryByTestId("tour-callout")).toBeNull();
    act(()=>drawer.removeAttribute("aria-modal"));
    expect(await screen.findByTestId("tour-callout")).toHaveTextContent(DEMO_TIPS.alerts.body);
    fireEvent.click(screen.getByTestId("tour-next"));
    act(()=>drawer.setAttribute("aria-modal","true")); act(()=>drawer.removeAttribute("aria-modal"));
    expect(screen.queryByTestId("tour-callout")).toBeNull();
  }finally{drawer.remove();}
});
it("legacy logs tour link opens only logs; Done dismisses without a full-site sequence", async () => {
  const {router}=renderDemo("/forge/demo?view=logs&tour=forge:fg-logs");
  expect(await screen.findByTestId("tour-callout")).toHaveTextContent(DEMO_TIPS.logs.body);
  fireEvent.click(screen.getByTestId("tour-next"));
  await waitFor(()=>expect(screen.queryByTestId("tour-callout")).toBeNull());
  expect(router.state.location.pathname).toBe("/forge/demo");
  expect((router.state.location.search as {view:string}).view).toBe("logs");
});
it("persistent actions survive page navigation and open the existing build section", async()=>{
  const {go,router}=renderDemo("/forge/p/shop");
  expect(await screen.findByTestId("demo-screen-actions")).toHaveTextContent("How Forge builds this");
  await go("/forge/p/shop/observability/logs");
  expect(screen.getByTestId("demo-screen-actions")).toHaveTextContent("Install Forge");
  expect(screen.getByTestId("tour-callout").querySelector('[data-testid="demo-cta-signup"]')).toBeNull();
  fireEvent.click(screen.getByRole("link",{name:"How Forge builds this"}));
  await waitFor(()=>expect(router.state.location.pathname).toBe("/forge/demo"));
  expect(router.state.location.search).toMatchObject({section:"built"});
});
it("keeps a page callout below the screen-action row, which stacks over the tour", async () => {
  Object.defineProperty(window, "innerWidth", { configurable: true, value: 1440 });
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 900 });
  // The page starts under the 52px action row; its target sits at the page's top, so a
  // card centred beside it would be clamped to the viewport's top edge, under the row.
  const box = (top: number, left: number, width: number, height: number) => ({ top, left, width, height, right: left + width, bottom: top + height, x: left, y: top, toJSON: () => ({}) }) as DOMRect;
  const original = Element.prototype.getBoundingClientRect;
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
    if (this.tagName === "MAIN") return box(52, 260, 1180, 848);
    if (this.getAttribute("data-testid") === "obs-log-row") return box(60, 260, 300, 28);
    return original.call(this);
  });
  try {
    renderDemo("/forge/p/shop/observability/logs");
    // The corner card shows until the target's anchor is found; then the spotlight replaces it.
    await waitFor(() => expect(screen.getByTestId("tour-callout").getAttribute("data-side")).toBe("right"));
    expect(parseFloat(screen.getByTestId("tour-callout").style.top)).toBeGreaterThanOrEqual(52 + 16);
  } finally {
    vi.restoreAllMocks();
  }
});
it("on a window too short to fit the card below the action row, shrinks the card to stay below the row and on screen", async () => {
  Object.defineProperty(window, "innerWidth", { configurable: true, value: 1440 });
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 260 });
  // The page below the 52px row leaves 208px; the card's natural 240px plus margins
  // needs 272px, and its target sits near the page's bottom.
  const box = (top: number, left: number, width: number, height: number) => ({ top, left, width, height, right: left + width, bottom: top + height, x: left, y: top, toJSON: () => ({}) }) as DOMRect;
  const original = Element.prototype.getBoundingClientRect;
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
    if (this.tagName === "MAIN") return box(52, 260, 1180, 208);
    if (this.getAttribute("data-testid") === "obs-log-row") return box(220, 260, 300, 28);
    if (this.getAttribute("data-testid") === "tour-callout" && this instanceof HTMLElement) {
      // The card measures at its natural height unless its max-height caps it.
      const cap = parseFloat(this.style.maxHeight);
      return box(parseFloat(this.style.top) || 0, parseFloat(this.style.left) || 0, 320, Number.isNaN(cap) ? 240 : Math.min(240, cap));
    }
    return original.call(this);
  });
  try {
    renderDemo("/forge/p/shop/observability/logs");
    await waitFor(() => expect(screen.getByTestId("tour-callout").getAttribute("data-side")).toBe("right"));
    const card = screen.getByTestId("tour-callout");
    await waitFor(() => expect(card.style.maxHeight).not.toBe(""));
    const top = parseFloat(card.style.top);
    const height = Math.min(240, parseFloat(card.style.maxHeight));
    expect(top).toBeGreaterThanOrEqual(52 + 16);
    expect(top + height).toBeLessThanOrEqual(260 - 16);
    expect(card.style.overflowY).toBe("auto");
  } finally {
    vi.restoreAllMocks();
  }
});
describe("a page's anchor that mounts late (a lazy chunk)", () => {
  const lateAnchor = () => {
    const anchor = document.createElement("div");
    anchor.setAttribute("data-testid", "releases-table");
    const scrollIntoView = vi.fn();
    anchor.scrollIntoView = scrollIntoView;
    return { anchor, scrollIntoView };
  };
  it("is scrolled to when it turns up", async () => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 1440 });
    renderDemo("/forge/p/shop/releases");
    await screen.findByTestId("tour-callout");
    const { anchor, scrollIntoView } = lateAnchor();
    try {
      act(() => { document.querySelector("main")!.appendChild(anchor); });
      await waitFor(() => expect(scrollIntoView).toHaveBeenCalled());
    } finally { anchor.remove(); }
  });
  it("leaves the page where the reader put it once they have scrolled", async () => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 1440 });
    renderDemo("/forge/p/shop/releases");
    await screen.findByTestId("tour-callout");
    act(() => { window.dispatchEvent(new Event("wheel")); });
    const { anchor, scrollIntoView } = lateAnchor();
    try {
      act(() => { document.querySelector("main")!.appendChild(anchor); });
      // The spotlight takes over from the corner card, without scrolling.
      await waitFor(() => expect(screen.getByTestId("tour-callout").getAttribute("data-side")).not.toBeNull());
      expect(scrollIntoView).not.toHaveBeenCalled();
    } finally { anchor.remove(); }
  });
});
it("copy stays concise and permission-aware",()=>{
  for(const key of DEMO_TIP_KEYS){expect(DEMO_TIPS[key].body.length,key).toBeLessThan(480);expect(DEMO_TIPS[key].body).not.toMatch(/NATS|transport/);}
  expect(DEMO_TIPS.alerts.body).toMatch(/nothing deploys without configuration and permission/);
});
