// Copyright (c) 2025 Reliant Labs
import { afterEach, beforeEach, expect, it } from "vitest";
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
  expect(screen.getByTestId("tour-step-count")).toHaveTextContent("Step 1 of 1");
  expect(screen.getByTestId("tour-exit")).toHaveTextContent("Dismiss");
  expect(screen.queryByText("Take the tour")).toBeNull();
  expect(screen.queryByTestId("onboarding-nav-bar")).toBeNull();
  expect((router.state.location.search as {tour?:string}).tour).toBeUndefined();
});
it("dismissal holds through same-page filters and rerenders, ? replays without navigation", async () => {
  const {go,router} = renderDemo("/forge/p/shop/observability/logs");
  await waitFor(() => fireEvent.click(screen.getByTestId("tour-exit")));
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
  fireEvent.click(screen.getByTestId("tour-exit"));
  await go("/forge/p/shop/releases");
  expect(await screen.findByTestId("tour-callout")).toHaveTextContent(DEMO_TIPS.releases.body);
  fireEvent.click(screen.getByTestId("tour-exit"));
  await go("/forge/p/shop/observability/logs");
  expect(await screen.findByTestId("tour-callout")).toHaveTextContent(DEMO_TIPS.logs.body);
  fireEvent.click(screen.getByTestId("tour-exit"));
  await go("/forge/p/shop");
  expect(await screen.findByTestId("tour-callout")).toHaveTextContent(DEMO_TIPS.overview.body);
});
it("refresh starts again, even after dismissing the previous mount", async () => {
  renderDemo("/forge/p/shop/observability/errors");
  await waitFor(() => fireEvent.click(screen.getByTestId("tour-exit"))); cleanup();
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
    fireEvent.click(screen.getByTestId("tour-exit"));
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
it("copy stays concise and permission-aware",()=>{
  for(const key of DEMO_TIP_KEYS){expect(DEMO_TIPS[key].body.length,key).toBeLessThan(480);expect(DEMO_TIPS[key].body).not.toMatch(/NATS|transport/);}
  expect(DEMO_TIPS.alerts.body).toMatch(/nothing deploys without configuration and permission/);
});
