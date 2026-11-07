// Copyright (c) 2025 Reliant Labs

/**
 * Explore first (owner, 2026-11-06): the demo opens on the console, a toast
 * offers the guided tour, and each console view shows a short tip the first
 * time it is opened.
 *
 *  - The toast: start or dismiss; a dismissed toast stays dismissed for the
 *    session; it hides while a tour is open.
 *  - Tips: once per view per session; "Got it" closes one, "Exit tips" all;
 *    off while the tour is open or the toast still waits for an answer;
 *    every listed view has one, and the copy has
 *    an agent line with a command on every tip.
 *  - The copy never says "new in release": releases are connected, not news.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from "@tanstack/react-router";

const tracked = vi.hoisted(() => [] as { event: string; props?: Record<string, unknown> }[]);
vi.mock("@/lib/analytics", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/analytics")>()),
  trackEvent: (event: string, props?: Record<string, unknown>) => tracked.push({ event, props }),
}));

import { memoryTourStorage, setTourShell, setTourStorage } from "@/components/Onboarding/tourShell";
import { forgeJourney } from "@/components/Onboarding/journeys/forge";
import { presentStep } from "@/components/Onboarding/journeys/registry";
import { resetShellForTests } from "@/lib/shell";

import { DemoTourToast } from "../DemoTourToast";
import { demoSession, resetDemoSessionForTests } from "../demoSession";
import { FORGE_ONE_LINER, FORGE_ONE_TOOL, FORGE_WHAT_IT_DOES } from "../demoCopy";
import { DEMO_TIPS, DEMO_TIP_KEYS, DemoTips, demoTipKey, type DemoTipKey } from "../tips";

/** A page that renders the given anchors, for the page tours to find. */
function Page({ anchors = [] }: { anchors?: string[] }) {
  return (
    <div data-testid="page">
      {anchors.map((id) => (
        <div key={id} data-testid={id} />
      ))}
    </div>
  );
}

function renderDemo(path: string, anchors?: string[]) {
  const root = createRootRoute({
    component: () => (
      <>
        <Outlet />
        <DemoTips />
        <DemoTourToast />
      </>
    ),
  });
  const any = createRoute({ getParentRoute: () => root, path: "$", component: () => <Page anchors={anchors} />, validateSearch: (s: Record<string, unknown>) => s });
  const router = createRouter({ routeTree: root.addChildren([any]), history: createMemoryHistory({ initialEntries: [path] }) });
  render(<RouterProvider router={router} />);
  const go = (to: string) =>
    act(async () => {
      await router.navigate({ href: to });
    });
  return { router, go };
}

const tip = () => screen.queryByTestId("demo-tip")?.getAttribute("data-tip") ?? null;

beforeEach(() => {
  resetShellForTests("demo");
  setTourShell("forge");
  setTourStorage(memoryTourStorage());
  resetDemoSessionForTests();
  tracked.length = 0;
});

afterEach(() => {
  cleanup();
  setTourShell(null);
  resetShellForTests();
});

describe("the tour toast", () => {
  it("offers the tour on arrival, without starting it", async () => {
    const { router } = renderDemo("/forge/demo");
    expect(await screen.findByTestId("demo-tour-toast")).toHaveTextContent("Take the guided tour");
    expect((router.state.location.search as { tour?: string }).tour).toBeUndefined();
    expect(tracked.filter((t) => t.event === "demo_tour_offered")).toHaveLength(1);
  });

  it("starts the forge journey, and is gone for the session", async () => {
    const { router } = renderDemo("/forge/demo");
    fireEvent.click(await screen.findByTestId("demo-tour-toast-start"));
    await waitFor(() => expect((router.state.location.search as { tour?: string }).tour).toBe("forge:fg-intro"));
    expect(screen.queryByTestId("demo-tour-toast")).toBeNull();
    expect(demoSession.get().toastDismissed).toBe(true);
  });

  it("stays dismissed for the session once dismissed, across remounts", async () => {
    renderDemo("/forge/demo");
    fireEvent.click(await screen.findByTestId("demo-tour-toast-dismiss"));
    expect(screen.queryByTestId("demo-tour-toast")).toBeNull();
    expect(window.sessionStorage.getItem("forge-demo.session.v1")).toContain('"toastDismissed":true');
    cleanup();
    renderDemo("/forge/demo?view=traces");
    await screen.findByTestId("page");
    expect(screen.queryByTestId("demo-tour-toast")).toBeNull();
  });

  it("the close button dismisses it too", async () => {
    renderDemo("/forge/demo");
    fireEvent.click(await screen.findByTestId("demo-tour-toast-close"));
    expect(screen.queryByTestId("demo-tour-toast")).toBeNull();
    expect(demoSession.get().toastDismissed).toBe(true);
  });

  it("never shows over an open tour, and an open tour answers the offer", async () => {
    renderDemo("/forge/demo?view=traces&tour=forge:fg-trace");
    await screen.findByTestId("page");
    expect(screen.queryByTestId("demo-tour-toast")).toBeNull();
    expect(demoSession.get().toastDismissed).toBe(true);
  });
});

describe("the view tips", () => {
  // The toast asks first; these start with it answered.
  beforeEach(() => demoSession.dismissToast());

  it("names a tip for every view the owner listed, on the demo console and at the console's real paths", () => {
    const cases: [string, Record<string, unknown>, DemoTipKey | null][] = [
      ["/forge/demo", {}, "overview"],
      ["/forge/demo", { view: "traces" }, "traces"],
      ["/forge/demo", { view: "traces", trace: "abc" }, "trace"],
      ["/forge/demo", { view: "logs" }, "logs"],
      ["/forge/demo", { view: "profiles" }, "profiles"],
      ["/forge/demo", { view: "errors" }, "errors"],
      ["/forge/demo", { view: "dashboards" }, "dashboards"],
      ["/forge/demo", { view: "alerts" }, "alerts"],
      ["/forge/demo", { section: "built" }, null],
      ["/forge/p/shop", {}, "overview"],
      ["/forge/p/shop/releases", {}, "releases"],
      ["/forge/p/shop/releases/v1.8.3", {}, "release"],
      ["/forge/p/shop/environments", {}, "environments"],
      ["/forge/p/shop/environments/prod", {}, "environment"],
      ["/forge/p/shop/secrets", {}, "secrets"],
      ["/forge/p/shop/domains", {}, "domains"],
      ["/forge/p/shop/activity", {}, "activity"],
      ["/forge/p/shop/observability/traces", { trace: "abc" }, "trace"],
      ["/forge/p/shop/observability/errors", {}, "errors"],
      ["/forge/p/shop/observability/sessions", {}, "sessions"],
      ["/forge/p/shop/observability/dashboards/release/abc", {}, "dashboards"],
      ["/forge/p/shop/observability/graph", {}, null],
    ];
    for (const [path, search, key] of cases) expect(demoTipKey(path, search), `${path} ${JSON.stringify(search)}`).toBe(key);
    expect(new Set(cases.map(([, , key]) => key).filter(Boolean))).toEqual(new Set(DEMO_TIP_KEYS));
  });

  it("shows each view's tip once per session", async () => {
    const { go } = renderDemo("/forge/demo?view=traces");
    await waitFor(() => expect(tip()).toBe("traces"));
    await go("/forge/demo?view=logs");
    await waitFor(() => expect(tip()).toBe("logs"));
    // Back to a view whose tip was shown: nothing, even though it was never dismissed.
    await go("/forge/demo?view=traces");
    await waitFor(() => expect(tip()).toBeNull());
    await go("/forge/p/shop/releases");
    await waitFor(() => expect(tip()).toBe("releases"));
    cleanup();
    // A remount in the same session remembers.
    renderDemo("/forge/demo?view=logs");
    await screen.findByTestId("page");
    expect(tip()).toBeNull();
    expect(tracked.filter((t) => t.event === "demo_tip_shown").map((t) => t.props?.tip)).toEqual(["traces", "logs", "releases"]);
  });

  it("Got it closes this tip; the next view still gets its own", async () => {
    const { go } = renderDemo("/forge/demo?view=errors");
    fireEvent.click(await screen.findByTestId("demo-tip-dismiss"));
    expect(tip()).toBeNull();
    await go("/forge/p/shop/secrets");
    await waitFor(() => expect(tip()).toBe("secrets"));
  });

  it("Exit tips turns every tip off for the session", async () => {
    const { go } = renderDemo("/forge/demo?view=errors");
    fireEvent.click(await screen.findByTestId("demo-tips-exit"));
    expect(tip()).toBeNull();
    await go("/forge/p/shop/domains");
    await screen.findByTestId("page");
    expect(tip()).toBeNull();
    expect(demoSession.get().tipsDisabled).toBe(true);
  });

  it("stays out of the guided tour's way, and marks nothing shown meanwhile", async () => {
    const { go } = renderDemo("/forge/demo?view=traces&tour=forge:fg-trace");
    await screen.findByTestId("page");
    expect(tip()).toBeNull();
    expect(demoSession.hasSeenTip("traces")).toBe(false);
    await go("/forge/demo?view=traces");
    await waitFor(() => expect(tip()).toBe("traces"));
  });

  it("waits for the toast: one card at a time on arrival, then the view's tip", async () => {
    resetDemoSessionForTests();
    renderDemo("/forge/demo");
    fireEvent.click(await screen.findByTestId("demo-tour-toast-dismiss"));
    await waitFor(() => expect(tip()).toBe("overview"));
    expect(screen.getByTestId("demo-tip").parentElement!.parentElement).toBe(document.getElementById("forge-demo-corner"));
  });
});

describe("the page tours", () => {
  beforeEach(() => demoSession.dismissToast());

  const TRACE_STOPS = ["obs-trace-latency", "obs-errors-only", "obs-trace-table"];
  const callout = () => screen.queryByTestId("tour-callout");

  it("the card offers Show me around, and Sign up in its footer for this placement", async () => {
    renderDemo("/forge/demo?view=traces", TRACE_STOPS);
    await waitFor(() => expect(tip()).toBe("traces"));
    expect(screen.getByTestId("demo-page-tour-start")).toHaveTextContent("Show me around");
    const footer = screen.getByTestId("demo-tip-cta");
    const signup = footer.querySelector<HTMLAnchorElement>("[data-testid='demo-cta-signup']")!;
    expect(signup.href).toContain("utm_content=page_card");
    expect(signup.href).toContain("journey=forge");
    fireEvent.click(signup);
    expect(tracked).toContainEqual({ event: "demo_cta_clicked", props: { cta: "signup", placement: "page_card" } });
  });

  it("walks the page's callouts: Next, Back, then Done leaves the replay button", async () => {
    renderDemo("/forge/demo?view=traces", TRACE_STOPS);
    fireEvent.click(await screen.findByTestId("demo-page-tour-start"));
    await waitFor(() => expect(callout()).toHaveTextContent(DEMO_TIPS.traces.callouts[0].title));
    expect(screen.getByTestId("tour-step-count")).toHaveTextContent(/1 of 3/);
    expect(screen.queryByTestId("tour-back")).toBeNull();
    expect(tip()).toBeNull();

    fireEvent.click(screen.getByTestId("tour-next"));
    await waitFor(() => expect(callout()).toHaveTextContent(DEMO_TIPS.traces.callouts[1].title));
    fireEvent.click(screen.getByTestId("tour-back"));
    await waitFor(() => expect(callout()).toHaveTextContent(DEMO_TIPS.traces.callouts[0].title));
    fireEvent.click(screen.getByTestId("tour-next"));
    fireEvent.click(await screen.findByTestId("tour-next"));
    await waitFor(() => expect(callout()).toHaveTextContent(DEMO_TIPS.traces.callouts[2].title));
    expect(screen.getByTestId("tour-next")).toHaveTextContent("Done");
    fireEvent.click(screen.getByTestId("tour-next"));

    await waitFor(() => expect(callout()).toBeNull());
    expect(screen.getByTestId("demo-page-tour-replay")).toHaveAttribute("data-tip", "traces");
    expect(tracked.map((t) => t.event)).toEqual(expect.arrayContaining(["demo_page_tour_started", "demo_page_tour_completed"]));
  });

  it("leaves out a stop whose target isn't on the page", async () => {
    renderDemo("/forge/demo?view=traces", ["obs-trace-latency", "obs-trace-table"]);
    fireEvent.click(await screen.findByTestId("demo-page-tour-start"));
    await waitFor(() => expect(screen.getByTestId("tour-step-count")).toHaveTextContent(/1 of 2/));
  });

  it("Exit tour and Escape both end it, and the replay button starts it again", async () => {
    renderDemo("/forge/demo?view=traces", TRACE_STOPS);
    fireEvent.click(await screen.findByTestId("demo-page-tour-start"));
    fireEvent.click(await screen.findByTestId("tour-exit"));
    await waitFor(() => expect(callout()).toBeNull());
    expect(tracked).toContainEqual({ event: "demo_page_tour_exited", props: { tip: "traces", at: 1 } });

    fireEvent.click(screen.getByTestId("demo-page-tour-replay"));
    await waitFor(() => expect(callout()).toHaveTextContent(DEMO_TIPS.traces.callouts[0].title));
    fireEvent.keyDown(window, { key: "Escape" });
    await waitFor(() => expect(callout()).toBeNull());
    expect(screen.getByTestId("demo-page-tour-replay")).toBeInTheDocument();
  });

  it("a dismissed card leaves the replay button; a page seen before shows only that", async () => {
    const { go } = renderDemo("/forge/demo?view=traces", TRACE_STOPS);
    fireEvent.click(await screen.findByTestId("demo-tip-dismiss"));
    expect(screen.getByTestId("demo-page-tour-replay")).toBeInTheDocument();
    await go("/forge/demo?view=logs");
    await waitFor(() => expect(tip()).toBe("logs"));
    await go("/forge/demo?view=traces");
    await waitFor(() => expect(screen.getByTestId("demo-page-tour-replay")).toHaveAttribute("data-tip", "traces"));
    expect(tip()).toBeNull();
  });
});

describe("the page tours' copy", () => {
  it("gives every page 2–4 callouts, each on a data-testid anchor with a title and a body", () => {
    for (const key of DEMO_TIP_KEYS) {
      const { callouts } = DEMO_TIPS[key];
      expect(callouts.length, key).toBeGreaterThanOrEqual(2);
      expect(callouts.length, key).toBeLessThanOrEqual(4);
      for (const c of callouts) {
        expect(c.selector, key).toMatch(/^\[data-testid=/);
        expect(c.title, key).toBeTruthy();
        expect(c.body.length, key).toBeGreaterThan(15);
      }
    }
  });
});

describe("the demo's copy", () => {
  const forbidden = /new in (release|v\d)/i;

  it("gives every tip a title, a body, and an agent line that names a forge command", () => {
    for (const key of DEMO_TIP_KEYS) {
      const t = DEMO_TIPS[key];
      expect(t.title, key).toBeTruthy();
      expect(t.body.length, key).toBeGreaterThan(40);
      expect(t.agent.before, key).toMatch(/agent/i);
      expect(t.agent.command, key).toMatch(/^forge (obs|env|secret|domain) /);
    }
  });

  it("never says an error is 'new in release …'; it says forge ships the releases", async () => {
    const strings = [FORGE_ONE_LINER, FORGE_WHAT_IT_DOES, FORGE_ONE_TOOL, ...DEMO_TIP_KEYS.flatMap((k) => [DEMO_TIPS[k].title, DEMO_TIPS[k].body, DEMO_TIPS[k].agent.after])];
    for (const s of strings) expect(s).not.toMatch(forbidden);
    expect(DEMO_TIPS.errors.body).toMatch(/Because forge also ships your releases/);
    // Every tour step, rendered, in each shell.
    for (const shell of ["forge", "reliant"] as const) {
      setTourShell(shell);
      for (const step of forgeJourney.steps) {
        const shown = presentStep(step);
        const { container, unmount } = render(<>{shown.title}{shown.description}{step.phases?.map((p, i) => <div key={i}>{p.description}</div>)}</>);
        expect(container.textContent ?? "", `${shell} ${step.id}`).not.toMatch(forbidden);
        unmount();
      }
    }
  });
});
