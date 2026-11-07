// Copyright (c) 2025 Reliant Labs
import { afterEach, beforeEach, expect, it } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from "@tanstack/react-router";
import { memoryTourStorage, setTourShell, setTourStorage } from "@/components/Onboarding/tourShell";
import { JourneyTour } from "@/components/Onboarding/JourneyTour";
import { resetShellForTests } from "@/lib/shell";
import { DemoTips } from "../tips/DemoTips";
import { DemoTourToast } from "../DemoTourToast";
import { resetDemoSessionForTests } from "../demoSession";
import { DEMO_TIPS, DEMO_TIP_KEYS, demoTipKey } from "../tips";

function renderDemo(path: string) {
  const root = createRootRoute({ component: () => <><Outlet /><DemoTips /><DemoTourToast /><JourneyTour /></> });
  const any = createRoute({ getParentRoute: () => root, path: "$", component: () => <main data-testid="page"><h1>Console page</h1><div data-testid="obs-trace-table" /><div data-testid="obs-logs-table" /><div data-testid="obs-alerts-firing" /><div data-testid="obs-errors-list" /></main>, validateSearch: (s: Record<string, unknown>) => s });
  const router = createRouter({ routeTree: root.addChildren([any]), history: createMemoryHistory({ initialEntries: [path] }) });
  render(<RouterProvider router={router} />);
  return { router, go: (to: string) => act(async () => { await router.navigate({ href: to }); }) };
}
beforeEach(() => { resetShellForTests("demo"); setTourShell("forge"); setTourStorage(memoryTourStorage()); resetDemoSessionForTests(); });
afterEach(() => { cleanup(); setTourShell(null); resetShellForTests(); });

it("arrival shows one compact card, no competing toast, auto-start or modal", async () => {
  const { router } = renderDemo("/forge/p/shop");
  expect(await screen.findByTestId("demo-tip")).toHaveTextContent("Explore a forge app");
  expect(screen.getByTestId("demo-tip-dismiss")).toHaveTextContent("Dismiss");
  expect(screen.getByTestId("demo-page-tour-start")).toHaveTextContent("Take the tour");
  expect(screen.queryByTestId("demo-tour-toast")).toBeNull();
  expect(screen.queryByTestId("tour-callout")).toBeNull();
  expect(screen.queryByRole("dialog")).toBeNull();
  expect((router.state.location.search as {tour?:string}).tour).toBeUndefined();
  expect(screen.getByTestId("demo-cta-signup")).toHaveAttribute("href", expect.stringContaining("utm_content=corner"));
});
it("Dismiss leaves the page usable and accessible ? restores the information", async () => {
  const { go } = renderDemo("/forge/p/shop/observability/traces");
  fireEvent.click(await screen.findByTestId("demo-tip-dismiss"));
  expect(screen.queryByTestId("demo-tip")).toBeNull();
  const info = screen.getByRole("button", { name: "About Traces" });
  expect(info).toHaveClass("touch-target");
  fireEvent.click(info);
  expect(screen.getByTestId("demo-tip")).toHaveTextContent("Find exactly where an error happens");
  fireEvent.click(screen.getByTestId("demo-tip-dismiss"));
  await go("/forge/p/shop/observability/errors");
  expect(await screen.findByTestId("demo-tip")).toHaveAttribute("data-tip", "errors");
  await go("/forge/p/shop/observability/traces");
  await waitFor(() => expect(screen.queryByTestId("demo-tip")).toBeNull());
  expect(screen.getByRole("button", { name: "About Traces" })).toBeInTheDocument();
});
it("secondary help is keyboard/click accessible, not hover-only", async () => {
  renderDemo("/forge/p/shop/observability/alerts"); await screen.findByTestId("demo-tip");
  const help = screen.getByText("How an agent helps"); help.focus(); expect(help).toHaveFocus();
  await userEvent.click(help);
  expect(help.closest("details")).toHaveAttribute("open");
  expect(screen.getByText(/Notification destinations/)).toBeVisible();
  await userEvent.click(help); expect(help.closest("details")).not.toHaveAttribute("open");
});
it("optional Next follows actual page navigation; Exit stays on the current page", async () => {
  const { router } = renderDemo("/forge/p/shop");
  fireEvent.click(await screen.findByTestId("demo-page-tour-start"));
  expect(await screen.findByTestId("tour-callout")).toHaveTextContent("Explore a forge app");
  expect(screen.queryByTestId("onboarding-nav-bar")).toBeNull();
  fireEvent.click(screen.getByTestId("tour-next"));
  await waitFor(() => expect(router.state.location.pathname).toBe("/forge/p/shop/observability/traces"));
  expect(await screen.findByTestId("tour-callout")).toHaveTextContent("Find exactly where an error happens");
  fireEvent.click(screen.getByTestId("tour-exit"));
  await waitFor(() => expect((router.state.location.search as {tour?:string}).tour).toBeUndefined());
  expect(router.state.location.pathname).toBe("/forge/p/shop/observability/traces");
});
it("legacy logs link explains logs, not a prerequisite action", async () => {
  renderDemo("/forge/demo?view=logs&tour=forge:fg-logs");
  expect(await screen.findByTestId("tour-callout")).toHaveTextContent(DEMO_TIPS.logs.body);
  expect(screen.getByTestId("tour-next")).not.toBeDisabled();
  expect(screen.queryByTestId("tour-open-page")).toBeNull();
});
it("the corner disappears while the phone drawer is open", async () => {
  renderDemo("/forge/p/shop"); await screen.findByTestId("demo-tip");
  const drawer = document.createElement("nav"); drawer.id = "sidebar-layout-nav"; document.body.appendChild(drawer);
  try {
    act(() => drawer.setAttribute("aria-modal", "true"));
    await waitFor(() => expect(screen.queryByTestId("demo-tip")).toBeNull());
    act(() => drawer.removeAttribute("aria-modal")); expect(await screen.findByTestId("demo-tip")).toBeInTheDocument();
  } finally { drawer.remove(); }
});
it("each explanation is brief, plain-language and has no local-stop arrays", () => {
  for (const key of DEMO_TIP_KEYS) {
    const tip = DEMO_TIPS[key];
    expect(tip.body.length, key).toBeLessThan(480);
    expect(tip.body.match(/[.!?](?:\s|$)/g)?.length ?? 0, key).toBeLessThanOrEqual(3);
    expect(`${tip.body} ${tip.help}`, key).not.toMatch(/NATS|\bConnect\s+(?:client|server|hop)|transport|RPC|headers/i);
    expect(tip.command, key).toMatch(/^forge /); expect(tip).not.toHaveProperty("callouts");
  }
  expect(DEMO_TIPS.errors.body).toMatch(/users.*release.*regression.*changed code/);
  expect(DEMO_TIPS.alerts.body).toMatch(/text, email or Slack where supported/);
  expect(DEMO_TIPS.alerts.body).toMatch(/configured Reliant workflow.*approvals/);
  expect(DEMO_TIPS.alerts.body).toMatch(/nothing deploys without configuration and permission/);
  expect(demoTipKey("/forge/demo", {view:"logs"})).toBe("logs");
});
