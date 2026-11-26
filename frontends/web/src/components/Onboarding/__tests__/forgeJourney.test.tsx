/**
 * The forge journey's content (journeys/forge.tsx, journeys-spec.md §3): its
 * sixteen steps on the sample app in the signed-in shell, eleven on the
 * app hosts signed out (on `/forge/demo`, which leaves for the demo host), the demo host's own short cut of ten
 * (DEMO_TOUR=short) or every non-shell step (full), the compact controls, each
 * step's required action against the URL or signal the spec names
 * (watchAdvance, against a fake router), and the copy that differs per shell.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

import { conditionsOf, watchAdvance, type AdvanceEnv } from "../advanceOn";
import { setRuntimeConfig } from "@/test/runtimeConfig";
import { resetShellForTests } from "@/lib/shell";

import { DEMO_INSIDE_SPAN_IDS, DEMO_INVENTORY_SPAN_ID, DEMO_ISSUE_ID, DEMO_TRACE_ID, forgeJourney } from "../journeys/forge";
import { activeJourneySteps, presentStep } from "../journeys/registry";
import { tourEvents } from "../tourEvents";
import { setDemoTourHooks, setReliantTourHooks, setTourShell } from "../tourShell";
import { setSampleTourActive, useSampleTour } from "../../Forge/Sample/sampleTour";
import type { JourneyStep, RouteTarget } from "../types";

const step = (id: string): JourneyStep => {
  const found = forgeJourney.steps.find((candidate) => candidate.id === id);
  if (!found) throw new Error(`no step ${id}`);
  return found;
};

function fakeRouter(search: Record<string, unknown>, pathname = "/forge/demo") {
  let location = { pathname, search };
  const listeners = new Set<() => void>();
  const env: AdvanceEnv = {
    getPathname: () => location.pathname,
    getSearch: () => location.search,
    subscribeLocation: (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    checklistHas: () => false,
    subscribeChecklist: () => () => undefined,
    getContext: () => ({}),
    subscribeContext: () => () => undefined,
    document,
  };
  return {
    env,
    go(next: Record<string, unknown>, nextPathname = "/forge/demo") {
      location = { pathname: nextPathname, search: next };
      listeners.forEach((fn) => fn());
    },
  };
}

/** Watch a step's advanceOn from `entry`; returns the spy and a way to move. */
function watchStep(id: string, entry: Record<string, unknown>, pathname?: string) {
  const router = fakeRouter(entry, pathname);
  const onFire = vi.fn();
  const watcher = watchAdvance(conditionsOf(step(id)), { target: step(id).target }, router.env, onFire);
  return { onFire, go: router.go, watcher };
}

const openSearch = (id: string) => (step(id).route?.open as RouteTarget).search ?? {};

/** The public demo host, as its boot leaves it (demoShellBoot.ts): the demo shell, and the demo's tour hooks on its own app. */
function onDemoHost() {
  resetShellForTests("demo");
  setDemoTourHooks({ app: "shop" });
}

/** Someone signed in: the tour walks the sample app inside the shell (catalog.ts installs this in the app). */
function signIn() {
  setReliantTourHooks({ landOnChat: async () => undefined, afterTour: async () => undefined, inAppShell: () => true });
}

/** The steps about the shell's own nav and pages: nowhere else. */
const SHELL_ONLY = ["fg-nav", "fg-deployments", "fg-tests", "fg-security", "fg-configure"];

afterEach(() => {
  setDemoTourHooks(null);
  setSampleTourActive(false);
  setReliantTourHooks(null);
  setTourShell(null);
  resetShellForTests();
  setRuntimeConfig({});
});

const ELEVEN = [
  "fg-intro",
  "fg-console",
  "fg-instrumented",
  "fg-trace",
  "fg-hops",
  "fg-logs",
  "fg-profile",
  "fg-issue",
  "fg-oncall",
  "fg-ask",
  "fg-done",
];
const SIXTEEN = [
  "fg-intro",
  "fg-nav",
  "fg-instrumented",
  "fg-deployments",
  "fg-trace",
  "fg-hops",
  "fg-logs",
  "fg-issue",
  "fg-oncall",
  "fg-ask",
  "fg-analytics",
  "fg-data",
  "fg-tests",
  "fg-security",
  "fg-configure",
  "fg-done",
];
const active = () => activeJourneySteps("forge").map((s) => s.id);

describe("the forge journey", () => {
  it("is eleven steps signed out on either app host, on /forge/demo, with the compact controls (v4)", () => {
    expect(forgeJourney.version).toBe(4);
    expect(forgeJourney.controls).toBe("callout");
    resetShellForTests("reliant");
    expect(active()).toEqual(ELEVEN);
    resetShellForTests("forge");
    expect(active()).toEqual(ELEVEN);
    // It runs in both shells: the demo reads nothing of the user's.
    expect(forgeJourney.shells).toBeUndefined();
  });

  it("is a short, optional path on the demo host: what forge is, one request across services, its logs and error, the handoff, who uses the app, how it was built", () => {
    onDemoHost();
    expect(active()).toEqual(["fg-intro", "fg-instrumented", "fg-trace", "fg-hops", "fg-logs", "fg-issue", "fg-ask", "fg-analytics", "fg-built", "fg-done"]);
    expect(active().length).toBeLessThanOrEqual(10);
  });

  it("keeps fg-instrumented in every cut; the short demo stays at ten by leaving out fg-agent, not fg-instrumented", () => {
    // Every cut the engine can run, counted from activeJourneySteps (what the callout's "Step X of Y" shows).
    const cuts: [string, () => void, number][] = [
      ["reliant", () => resetShellForTests("reliant"), 11],
      ["forge", () => resetShellForTests("forge"), 11],
      ["reliant signed in", () => (resetShellForTests("reliant"), signIn()), 16],
      ["forge signed in", () => (resetShellForTests("forge"), signIn()), 16],
      ["demo short", () => onDemoHost(), 10],
      ["demo full", () => (onDemoHost(), setRuntimeConfig({ DEMO_TOUR: "full" })), 17],
    ];
    expect(step("fg-instrumented").isAvailable).toBeUndefined();
    for (const [name, enter, total] of cuts) {
      setRuntimeConfig({});
      setReliantTourHooks(null);
      setDemoTourHooks(null);
      enter();
      expect(active(), name).toContain("fg-instrumented");
      expect(active(), name).toHaveLength(total);
      expect(new Set(active()).size, name).toBe(total);
    }
    onDemoHost();
    setRuntimeConfig({});
    expect(active()).not.toContain("fg-agent");
    expect(step("fg-agent").isAvailable?.()).toBe(false);
  });

  it("is every step but the shell's on the demo host with DEMO_TOUR=full", () => {
    onDemoHost();
    setRuntimeConfig({ DEMO_TOUR: "full" });
    // The demo's bundle never installs the Reliant tour hooks (journeys/catalog.ts), so the shell's steps never run there.
    expect(active()).toEqual(forgeJourney.steps.map((s) => s.id).filter((id) => !SHELL_ONLY.includes(id)));
    expect(active()).toEqual([
      "fg-intro",
      "fg-console",
      "fg-instrumented",
      "fg-trace",
      "fg-hops",
      "fg-inside",
      "fg-logs",
      "fg-profile",
      "fg-graph",
      "fg-issue",
      "fg-oncall",
      "fg-agent",
      "fg-ask",
      "fg-analytics",
      "fg-data",
      "fg-built",
      "fg-done",
    ]);
  });

  it("opens with two sentences: what Forge is, and what the tour shows", () => {
    onDemoHost();
    render(<>{presentStep(step("fg-intro")).description}</>);
    // Two sentences: what Forge is, and what the tour shows.
    const intro = screen.getByText(/open-source toolkit.*Claude Code, Codex or Cursor/);
    expect(intro).toHaveTextContent(/follow one failing request/);
    expect(intro.textContent!.trim().split(/(?<=[.?!])\s+/)).toHaveLength(2);
    expect(screen.queryByTestId("install-forge-link-tour_intro")).toBeNull();
    expect(conditionsOf(step("fg-intro"))).toEqual([]);
  });

  it("distinguishes sample monitoring data from the agent's work on a deployed app", () => {
    render(<>{presentStep(step("fg-done")).description}</>);
    expect(screen.getByText(/tour uses sample data/)).toBeInTheDocument();
    expect(screen.getByText(/Ask your agent to add a service or background worker/)).toHaveTextContent(
      /the agent implements the app's custom behavior and runs the checks/
    );
    expect(screen.getByText(/Once the app is deployed, the agent can inspect/)).toBeInTheDocument();
  });

  it("lives on the demo console, signed out, every step but the cards and the shell's", () => {
    for (const s of forgeJourney.steps.filter((candidate) => ![...SHELL_ONLY, "fg-done", "fg-agent"].includes(candidate.id))) {
      expect(s.route?.path, s.id).toBe("/forge/demo");
      expect((s.route?.open as RouteTarget).to, s.id).toBe("/forge/demo");
    }
    expect(step("fg-done").route).toBeUndefined();
    expect(step("fg-done").target).toBeUndefined();
  });

  it("shows How this app was built at fg-built as a wide modal card with no spotlight, and closes the page section at every step", () => {
    const built = step("fg-built");
    // No target: the engine draws a centred card, no highlight cutout; the card holds the player.
    expect(built.target).toBeUndefined();
    expect(built.targets).toBeUndefined();
    expect(built.card).toEqual({ wide: true, modal: true });
    expect(conditionsOf(built)).toEqual([]);
    // The shell's own steps open console pages, which have no `section`.
    for (const s of forgeJourney.steps.filter((candidate) => candidate.route && !SHELL_ONLY.includes(candidate.id))) {
      const search = openSearch(s.id);
      expect("section" in search && search.section === undefined, s.id).toBe(true);
    }
    // The demo host only, and the last step before the end card.
    resetShellForTests("reliant");
    expect(built.isAvailable?.()).toBe(false);
    onDemoHost();
    expect(built.isAvailable?.()).toBe(true);
    expect(active().slice(-2)).toEqual(["fg-built", "fg-done"]);
  });

  it("finishes on the person's apps (Projects), on either brand", () => {
    expect(forgeJourney.finish?.("reliant")).toEqual({ to: "/projects", search: { show: "apps" } });
    expect(forgeJourney.finish?.("forge")).toEqual({ to: "/projects", search: { show: "apps" } });
  });

  it("stays on the console when it finishes on the demo host: there is nowhere to go", () => {
    onDemoHost();
    expect(forgeJourney.finish?.("forge")).toBeNull();
  });

  it("asks only for Next on the explaining steps", () => {
    for (const id of ["fg-intro", "fg-console", "fg-built", "fg-instrumented", "fg-inside", "fg-graph", "fg-oncall", "fg-agent", "fg-analytics", "fg-data", "fg-done", ...SHELL_ONLY]) {
      expect(conditionsOf(step(id)), id).toEqual([]);
    }
  });

  describe("signed in, in the shell", () => {
    it("is sixteen steps on either app host: the app's sections in the nav, then one request, then the rest of the nav", () => {
      for (const host of ["reliant", "forge"] as const) {
        resetShellForTests(host);
        signIn();
        expect(active(), host).toEqual(SIXTEEN);
      }
    });

    it("walks the sample app's console pages, never a person's own app", () => {
      resetShellForTests("reliant");
      signIn();
      for (const id of ["fg-intro", "fg-nav", "fg-instrumented"]) {
        expect(step(id).route?.path, id).toBe("/forge/p/sample_shop");
        expect((step(id).route?.open as RouteTarget).to, id).toBe("/forge/p/sample_shop");
      }
      for (const [id, page] of [["fg-trace", "traces"], ["fg-hops", "traces"], ["fg-logs", "traces"], ["fg-issue", "errors"], ["fg-ask", "errors"], ["fg-oncall", "alerts"]] as const) {
        expect(step(id).route?.path, id).toBe("/forge/p/sample_shop/observability");
        expect((step(id).route?.open as RouteTarget).to, id).toBe(`/forge/p/sample_shop/observability/${page}`);
      }
      for (const [id, path] of [
        ["fg-deployments", "/forge/p/sample_shop/releases"],
        ["fg-analytics", "/forge/p/sample_shop/analytics"],
        ["fg-data", "/forge/p/sample_shop/data"],
        ["fg-tests", "/forge/p/sample_shop/tests"],
        ["fg-security", "/forge/p/sample_shop/security/vulnerabilities"],
        ["fg-configure", "/forge/p/sample_shop/secrets"],
      ] as const) {
        expect(step(id).route?.path, id).toBe(path);
        expect((step(id).route?.open as RouteTarget).to, id).toBe(path);
      }
    });

    it("spotlights the shell nav's own anchors: the main nav, then each section's group", () => {
      expect(step("fg-nav").target).toBe("[data-tour='forge-console-nav']");
      expect(step("fg-deployments").target).toBe("[data-tour='nav-deployments']");
      expect(step("fg-tests").target).toBe("[data-tour='nav-tests']");
      expect(step("fg-security").target).toBe("[data-tour='nav-security']");
      expect(step("fg-configure").target).toBe("[data-tour='nav-configure']");
      expect(step("fg-analytics").target).toBe("[data-tour='nav-analytics']");
      expect(step("fg-data").target).toBe("[data-tour='nav-data']");
      for (const id of [...SHELL_ONLY, "fg-analytics", "fg-data"]) expect(step(id).tooltipPosition, id).toBe("right");
    });

    it("returns to where it started when it ends, only in the shell", () => {
      resetShellForTests("reliant");
      expect(forgeJourney.returnToStart?.("reliant")).toBe(false);
      signIn();
      expect(forgeJourney.returnToStart?.("reliant")).toBe(true);
      // Signed out on an app host, or on the demo host (which has no Reliant hooks): nothing to return to.
      setReliantTourHooks(null);
      onDemoHost();
      expect(forgeJourney.returnToStart?.("forge")).toBe(false);
    });

    it("lists the sample in the switcher while it is live in the shell, and nowhere else", () => {
      resetShellForTests("reliant");
      forgeJourney.onLiveChange?.(true);
      expect(useSampleTour.getState().active, "signed out").toBe(false);
      signIn();
      forgeJourney.onLiveChange?.(true);
      expect(useSampleTour.getState().active).toBe(true);
      forgeJourney.onLiveChange?.(false);
      expect(useSampleTour.getState().active).toBe(false);
    });

    it("says what an agent does on the deployment, analytics, data and test steps, with the shell's CLI", () => {
      setTourShell("forge");
      for (const [id, command] of [
        ["fg-deployments", /forge env deploy prod <release>/],
        ["fg-analytics", /forge obs users prod/],
        ["fg-data", /forge db query prod/],
        ["fg-tests", /forge lint/],
      ] as const) {
        const { unmount } = render(<>{presentStep(step(id)).description}</>);
        expect(screen.getByText(command), id).toBeInTheDocument();
        expect(screen.queryByText(/reliant forge/), id).toBeNull();
        unmount();
      }
    });
  });

  describe("on the demo host", () => {
    afterEach(() => resetShellForTests());

    it("walks the sample app's own console pages", () => {
      onDemoHost();
      for (const id of ["fg-intro", "fg-instrumented", "fg-built"]) {
        expect(step(id).route?.path, id).toBe("/forge/p/shop");
        expect((step(id).route?.open as RouteTarget).to, id).toBe("/forge/p/shop");
      }
      for (const [id, page] of [["fg-trace", "traces"], ["fg-hops", "traces"], ["fg-logs", "traces"], ["fg-issue", "errors"], ["fg-ask", "errors"], ["fg-graph", "graph"], ["fg-oncall", "alerts"]] as const) {
        // An Observability page's prefix: the trace steps move on to the logs and the profiles pages.
        expect(step(id).route?.path, id).toBe("/forge/p/shop/observability");
        expect((step(id).route?.open as RouteTarget).to, id).toBe(`/forge/p/shop/observability/${page}`);
      }
      expect(openSearch("fg-logs")).toMatchObject({ trace: DEMO_TRACE_ID });
      expect(openSearch("fg-logs")).not.toHaveProperty("view");
      expect(openSearch("fg-ask")).toMatchObject({ issue: DEMO_ISSUE_ID });
      for (const [id, page] of [["fg-analytics", "analytics"], ["fg-data", "data"]] as const) {
        expect(step(id).route?.path, id).toBe(`/forge/p/shop/${page}`);
        expect((step(id).route?.open as RouteTarget).to, id).toBe(`/forge/p/shop/${page}`);
      }
    });

    it("scopes every console step to prod, the story's environment, but fg-data to staging, whose database asks no reason", () => {
      onDemoHost();
      setRuntimeConfig({ DEMO_TOUR: "full" });
      // The demo host remembers a page's scope (envScope.ts): naming prod on each step undoes fg-data's staging, or a visitor's dev.
      for (const s of activeJourneySteps("forge").filter((candidate) => candidate.route && candidate.id !== "fg-data")) {
        expect(openSearch(s.id), s.id).toMatchObject({ env: "prod" });
      }
      expect(openSearch("fg-data")).toMatchObject({ env: "staging" });
      // In the shell the sample's scope is never remembered, so its steps name none; signed out on an app host they
      // are the demo console's, which has no scope of its own.
      setDemoTourHooks(null);
      resetShellForTests("reliant");
      expect(openSearch("fg-trace")).not.toHaveProperty("env");
      signIn();
      expect(openSearch("fg-trace")).not.toHaveProperty("env");
      expect(openSearch("fg-analytics")).not.toHaveProperty("env");
      expect(openSearch("fg-data")).toMatchObject({ env: "staging" });
    });

    it("leaves fg-data and fg-inside to the full tour, and gives the short tour fg-analytics", () => {
      onDemoHost();
      expect(step("fg-analytics").isAvailable?.()).toBe(true);
      expect(step("fg-data").isAvailable?.()).toBe(false);
      expect(step("fg-inside").isAvailable?.()).toBe(false);
      setRuntimeConfig({ DEMO_TOUR: "full" });
      for (const id of ["fg-analytics", "fg-data", "fg-inside"]) expect(step(id).isAvailable?.(), id).toBe(true);
      // Signed out on an app host: no app's console to walk.
      setDemoTourHooks(null);
      resetShellForTests("reliant");
      for (const id of ["fg-analytics", "fg-data"]) expect(step(id).isAvailable?.(), id).toBe(false);
    });

    it("fg-logs and fg-profile are done on the console's logs and profiles pages", () => {
      onDemoHost();
      const logs = watchStep("fg-logs", openSearch("fg-logs"), "/forge/p/shop/observability/traces");
      logs.go({ trace: DEMO_TRACE_ID }, "/forge/p/shop/observability/traces");
      expect(logs.onFire).not.toHaveBeenCalled();
      logs.go({ trace: DEMO_TRACE_ID }, "/forge/p/shop/observability/logs");
      expect(logs.onFire).toHaveBeenCalledTimes(1);
      logs.watcher.dispose();
      const profile = watchStep("fg-profile", openSearch("fg-profile"), "/forge/p/shop/observability/traces");
      profile.go({ trace: DEMO_TRACE_ID, service: "api" }, "/forge/p/shop/observability/profiles");
      expect(profile.onFire).toHaveBeenCalledTimes(1);
      profile.watcher.dispose();
    });
  });

  describe("required actions", () => {
    it("fg-trace: the toured trace opened (?trace=)", () => {
      const { onFire, go, watcher } = watchStep("fg-trace", openSearch("fg-trace"));
      expect(watcher.initiallySatisfied).toBe(false);
      go({ view: "traces", trace: "0".repeat(32) });
      expect(onFire).not.toHaveBeenCalled();
      go({ view: "traces", trace: DEMO_TRACE_ID });
      expect(onFire).toHaveBeenCalledTimes(1);
      // It moves on once it's done.
      expect(step("fg-trace").autoAdvance).toBe(true);
      watcher.dispose();
    });

    it("fg-hops: inventory's server span selected (?span=e1), on the toured trace", () => {
      expect(openSearch("fg-hops")).toMatchObject({ view: "traces", trace: DEMO_TRACE_ID });
      const { onFire, go, watcher } = watchStep("fg-hops", openSearch("fg-hops"));
      expect(watcher.initiallySatisfied).toBe(false);
      go({ view: "traces", trace: DEMO_TRACE_ID, span: "a1" });
      expect(onFire).not.toHaveBeenCalled();
      go({ view: "traces", trace: DEMO_TRACE_ID, span: DEMO_INVENTORY_SPAN_ID });
      expect(onFire).toHaveBeenCalledTimes(1);
      // It moves on when clicked, so the step's copy explains the hop BEFORE the click.
      expect(step("fg-hops").autoAdvance).toBe(true);
      expect(step("fg-hops").target).toBe(`[data-testid='trace-span-row'][data-span-id='${DEMO_INVENTORY_SPAN_ID}']`);
      watcher.dispose();
    });

    it("fg-logs: that trace's logs (?view=logs&trace=)", () => {
      const { onFire, go, watcher } = watchStep("fg-logs", openSearch("fg-logs"));
      expect(watcher.initiallySatisfied).toBe(false);
      go({ view: "logs" });
      go({ view: "traces", trace: DEMO_TRACE_ID });
      expect(onFire).not.toHaveBeenCalled();
      go({ view: "logs", trace: DEMO_TRACE_ID });
      expect(onFire).toHaveBeenCalledTimes(1);
      // Held: the next step is on another page, and the logs it opens are the point.
      expect(step("fg-logs").autoAdvance).toBe(false);
      watcher.dispose();
    });

    it("fg-profile: the flamegraph (?view=profiles)", () => {
      const { onFire, go, watcher } = watchStep("fg-profile", openSearch("fg-profile"));
      expect(watcher.initiallySatisfied).toBe(false);
      go({ view: "profiles", trace: DEMO_TRACE_ID });
      expect(onFire).toHaveBeenCalledTimes(1);
      watcher.dispose();
    });

    it("fg-issue: the issue opened (?issue=)", () => {
      const { onFire, go, watcher } = watchStep("fg-issue", openSearch("fg-issue"));
      expect(watcher.initiallySatisfied).toBe(false);
      go({ view: "errors", issue: "iss-other" });
      expect(onFire).not.toHaveBeenCalled();
      go({ view: "errors", issue: DEMO_ISSUE_ID });
      expect(onFire).toHaveBeenCalledTimes(1);
      watcher.dispose();
    });

    it("fg-ask: the Ask-an-agent surface opened (askAgent:opened)", () => {
      const { onFire, watcher } = watchStep("fg-ask", openSearch("fg-ask"));
      expect(onFire).not.toHaveBeenCalled();
      tourEvents.emit("askAgent:copied", { what: "prompt" });
      expect(onFire).not.toHaveBeenCalled();
      tourEvents.emit("askAgent:opened", { subjectKind: "issue", mode: "handoff" });
      expect(onFire).toHaveBeenCalledTimes(1);
      // Held: the sheet it opens is the point, and its phase says so.
      expect(step("fg-ask").autoAdvance).toBe(false);
      expect(String(step("fg-ask").phases?.[1].description)).toMatch(/Reliant workflow/);
      watcher.dispose();
    });

    it("each step's entry starts the action over: the list, not an item already open", () => {
      for (const id of ["fg-trace", "fg-issue"]) {
        const search = openSearch(id);
        expect("trace" in search && search.trace === undefined, id).toBe(true);
        expect("issue" in search && search.issue === undefined, id).toBe(true);
      }
    });
  });

  it("opens each step where the one before it left off", () => {
    expect(openSearch("fg-logs")).toMatchObject({ view: "traces", trace: DEMO_TRACE_ID });
    expect(openSearch("fg-profile")).toMatchObject({ view: "traces", trace: DEMO_TRACE_ID });
    expect(openSearch("fg-ask")).toMatchObject({ view: "errors", issue: DEMO_ISSUE_ID });
  });

  it("spotlights the anchors the demo renders", () => {
    // The shell's nav, on the demo host too (it runs in the app's shell): one anchor, no second frame's.
    expect(step("fg-console").target).toBe("[data-tour='forge-console-nav']");
    // The console's own row, or the led Traces page's (the demo's console).
    expect(step("fg-trace").target).toBe(`[data-testid='obs-trace-row'][data-trace-id='${DEMO_TRACE_ID}'], [data-testid='lead-trace-row'][data-trace-id='${DEMO_TRACE_ID}']`);
    expect(step("fg-issue").target).toContain(`[data-testid='lead-featured-issue'][data-issue-id='${DEMO_ISSUE_ID}']`);
    expect(step("fg-instrumented").target).toBe("[data-testid='obs-overview'], [data-testid='overview-services']");
    expect(step("fg-ask").phases?.map((p) => p.target)).toEqual(["[data-testid='ask-reliant-actions']", "[data-testid='ask-agent-sheet']"]);
    expect(step("fg-inside").target).toBe(`[data-testid='trace-span-row'][data-span-id='${DEMO_INSIDE_SPAN_IDS.method}']`);
    expect(step("fg-graph").target).toBe("[data-testid='graph-view']");
    expect(openSearch("fg-graph")).toMatchObject({ view: "graph", trace: undefined });
  });

  describe("per shell", () => {
    it("spells the CLI the shell's way on the end card", () => {
      setTourShell("reliant");
      const { unmount } = render(<>{presentStep(step("fg-done")).description}</>);
      expect(screen.getByText("reliant forge obs status prod")).toBeInTheDocument();
      unmount();
      setTourShell("forge");
      render(<>{presentStep(step("fg-done")).description}</>);
      expect(screen.getByText("forge obs status prod")).toBeInTheDocument();
      expect(screen.getByText("forge obs traces prod --errors")).toBeInTheDocument();
      expect(screen.queryByText(/reliant forge/)).toBeNull();
    });

    it("tells the agent story with the shell's CLI, and ends the demo's tour on Deploy yours free, Install and Sign up", () => {
      setTourShell("forge");
      const agent = render(<>{presentStep(step("fg-agent")).description}</>);
      expect(screen.getByText(`forge obs trace ${DEMO_TRACE_ID} prod`)).toBeInTheDocument();
      expect(screen.getByText("claude mcp add forge -- forge agent mcp")).toBeInTheDocument();
      agent.unmount();
      onDemoHost();
      render(<>{presentStep(step("fg-done")).description}</>);
      // The public demo ends on its call to action, not a command list.
      expect(screen.queryByText("forge scaffold service inventory")).toBeNull();
      expect(screen.getByTestId("demo-cta-end_card")).toBeInTheDocument();
      expect(screen.getByTestId("demo-cta-deploy")).toHaveTextContent("Deploy yours free");
      expect(screen.getByTestId("demo-cta-signup")).toHaveTextContent("Sign up free");
      expect(screen.getByTestId("demo-cta-install")).toHaveTextContent("Install Forge");
    });

    it("says on each feature step what an agent does with it, with the shell's CLI", () => {
      setTourShell("forge");
      for (const [id, command] of [
        ["fg-hops", /forge obs trace 4bf92f35… prod/],
        ["fg-logs", /forge obs logs prod --trace <id>/],
        ["fg-issue", /forge obs issue <id> prod/],
      ] as const) {
        const { unmount } = render(<>{presentStep(step(id)).description}</>);
        expect(screen.getByText(command), id).toBeInTheDocument();
        expect(screen.queryByText(/reliant forge/), id).toBeNull();
        unmount();
      }
    });

    it("says Autopilot runs in Reliant, in the forge shell", () => {
      setTourShell("forge");
      render(<>{presentStep(step("fg-oncall")).description}</>);
      expect(screen.getByText(/Nothing deploys without configuration and permission/)).toBeInTheDocument();
      expect(screen.getByRole("link", { name: "Set it up in Reliant" })).toHaveAttribute(
        "href",
        "https://app.reliantlabs.io/workflows/library/builtin%3A%2F%2Fautopilot-oncall",
      );
    });
  });
});
