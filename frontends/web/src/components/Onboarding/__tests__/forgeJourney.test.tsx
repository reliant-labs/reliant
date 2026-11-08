/**
 * The forge journey's content (journeys/forge.tsx, journeys-spec.md §3): its
 * eleven steps in Reliant and the forge shell, the demo host's own short cut
 * of ten (DEMO_TOUR=short) or every step (full), the compact controls, each
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
import { setTourShell } from "../tourShell";
import type { JourneyStep, RouteTarget } from "../types";

const step = (id: string): JourneyStep => {
  const found = forgeJourney.steps.find((candidate) => candidate.id === id);
  if (!found) throw new Error(`no step ${id}`);
  return found;
};

function fakeRouter(search: Record<string, unknown>) {
  let location = { pathname: "/forge/demo", search };
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
    go(next: Record<string, unknown>) {
      location = { pathname: "/forge/demo", search: next };
      listeners.forEach((fn) => fn());
    },
  };
}

/** Watch a step's advanceOn from `entry`; returns the spy and a way to move. */
function watchStep(id: string, entry: Record<string, unknown>) {
  const router = fakeRouter(entry);
  const onFire = vi.fn();
  const watcher = watchAdvance(conditionsOf(step(id)), { target: step(id).target }, router.env, onFire);
  return { onFire, go: router.go, watcher };
}

const openSearch = (id: string) => (step(id).route?.open as RouteTarget).search ?? {};

afterEach(() => {
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
const active = () => activeJourneySteps("forge").map((s) => s.id);

describe("the forge journey", () => {
  it("is eleven steps in Reliant and the forge shell, with the compact controls (v3)", () => {
    expect(forgeJourney.version).toBe(3);
    expect(forgeJourney.controls).toBe("callout");
    resetShellForTests("reliant");
    expect(active()).toEqual(ELEVEN);
    resetShellForTests("forge");
    expect(active()).toEqual(ELEVEN);
    // It runs in both shells: the demo reads nothing of the user's.
    expect(forgeJourney.shells).toBeUndefined();
  });

  it("is a short, optional path on the demo host: what forge is, one request across services, its logs and error, the handoff, how it was built", () => {
    resetShellForTests("demo");
    expect(active()).toEqual(["fg-intro", "fg-instrumented", "fg-trace", "fg-hops", "fg-inside", "fg-logs", "fg-issue", "fg-ask", "fg-built", "fg-done"]);
    expect(active().length).toBeLessThanOrEqual(10);
  });

  it("keeps fg-instrumented in every cut; the short demo stays at ten by leaving out fg-agent, not fg-instrumented", () => {
    // Every cut the engine can run, counted from activeJourneySteps (what the callout's "Step X of Y" shows).
    const cuts: [string, () => void, number][] = [
      ["reliant", () => resetShellForTests("reliant"), 11],
      ["forge", () => resetShellForTests("forge"), 11],
      ["demo short", () => resetShellForTests("demo"), 10],
      ["demo full", () => (resetShellForTests("demo"), setRuntimeConfig({ DEMO_TOUR: "full" })), 15],
    ];
    expect(step("fg-instrumented").isAvailable).toBeUndefined();
    for (const [name, enter, total] of cuts) {
      setRuntimeConfig({});
      enter();
      expect(active(), name).toContain("fg-instrumented");
      expect(active(), name).toHaveLength(total);
      expect(new Set(active()).size, name).toBe(total);
    }
    resetShellForTests("demo");
    setRuntimeConfig({});
    expect(active()).not.toContain("fg-agent");
    expect(step("fg-agent").isAvailable?.()).toBe(false);
  });

  it("is every step on the demo host with DEMO_TOUR=full", () => {
    resetShellForTests("demo");
    setRuntimeConfig({ DEMO_TOUR: "full" });
    expect(active()).toEqual(forgeJourney.steps.map((s) => s.id));
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
      "fg-built",
      "fg-done",
    ]);
  });

  it("explains the agent's work, Forge's foundation and the user's ownership", () => {
    resetShellForTests("demo");
    render(<>{presentStep(step("fg-intro")).description}</>);
    expect(screen.getByText(/open-source toolkit.*Claude Code, Codex or Cursor/)).toBeInTheDocument();
    expect(
      screen.getByText(
        "Tell your agent what to build. Your agent defines the data and API and writes your app's custom behavior. Forge generates supporting code and production setup, with checks to guide the work. Its tools let the agent build, deploy and monitor the app; you own the code and decide what ships."
      )
    ).toBeInTheDocument();
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

  it("lives on the demo console, every step but the cards", () => {
    for (const s of forgeJourney.steps.filter((candidate) => !["fg-done", "fg-agent"].includes(candidate.id))) {
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
    for (const s of forgeJourney.steps.filter((candidate) => candidate.route)) {
      const search = openSearch(s.id);
      expect("section" in search && search.section === undefined, s.id).toBe(true);
    }
    // The demo host only, and the last step before the end card.
    resetShellForTests("reliant");
    expect(built.isAvailable?.()).toBe(false);
    resetShellForTests("demo");
    expect(built.isAvailable?.()).toBe(true);
    expect(active().slice(-2)).toEqual(["fg-built", "fg-done"]);
  });

  it("finishes on forge's Apps in Reliant and the project picker in the forge shell", () => {
    expect(forgeJourney.finish?.("reliant")).toEqual({ to: "/forge" });
    expect(forgeJourney.finish?.("forge")).toEqual({ to: "/apps" });
  });

  it("stays on the console when it finishes on the demo host: there is nowhere to go", () => {
    resetShellForTests("demo");
    expect(forgeJourney.finish?.("forge")).toBeNull();
  });

  it("asks only for Next on the explaining steps", () => {
    for (const id of ["fg-intro", "fg-console", "fg-built", "fg-instrumented", "fg-inside", "fg-graph", "fg-oncall", "fg-agent", "fg-done"]) expect(conditionsOf(step(id)), id).toEqual([]);
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
    expect(step("fg-console").target).toBe("[data-tour='forge-console'] #sidebar-layout-nav");
    expect(step("fg-trace").target).toBe(`[data-testid='obs-trace-row'][data-trace-id='${DEMO_TRACE_ID}']`);
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

    it("tells the agent story with the shell's CLI, and offers Install and Sign up on the demo's end card", () => {
      setTourShell("forge");
      const agent = render(<>{presentStep(step("fg-agent")).description}</>);
      expect(screen.getByText(`forge obs trace ${DEMO_TRACE_ID} prod`)).toBeInTheDocument();
      expect(screen.getByText("claude mcp add forge -- forge mcp")).toBeInTheDocument();
      agent.unmount();
      resetShellForTests("demo");
      render(<>{presentStep(step("fg-done")).description}</>);
      expect(screen.getByText("forge scaffold service inventory")).toBeInTheDocument();
      expect(screen.queryByTestId("demo-cta-end_card")).toBeNull();
      expect(screen.queryByTestId("demo-cta-signup")).toBeNull();
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
