/**
 * The forge journey's content (journeys/forge.tsx, journeys-spec.md §3): its
 * nine steps, each step's required action against the URL or signal the spec
 * names (watchAdvance, against a fake router), where Skip lands, and the copy
 * that differs in the forge shell.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

import { conditionsOf, watchAdvance, type AdvanceEnv } from "../advanceOn";
import { DEMO_ISSUE_ID, DEMO_TRACE_ID, forgeJourney } from "../journeys/forge";
import { presentStep } from "../journeys/registry";
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
});

describe("the forge journey", () => {
  it("is the spec's nine steps, at a new version", () => {
    expect(forgeJourney.version).toBe(2);
    expect(forgeJourney.steps.map((s) => s.id)).toEqual([
      "fg-console",
      "fg-instrumented",
      "fg-trace",
      "fg-logs",
      "fg-profile",
      "fg-issue",
      "fg-oncall",
      "fg-ask",
      "fg-done",
    ]);
    // It runs in both shells: the demo reads nothing of the user's.
    expect(forgeJourney.shells).toBeUndefined();
  });

  it("lives on the demo console, every step but the end card", () => {
    for (const s of forgeJourney.steps.slice(0, -1)) {
      expect(s.route?.path, s.id).toBe("/forge/demo");
      expect((s.route?.open as RouteTarget).to, s.id).toBe("/forge/demo");
    }
    expect(step("fg-done").route).toBeUndefined();
    expect(step("fg-done").target).toBeUndefined();
  });

  it("finishes on forge's Apps in Reliant and the project picker in the forge shell", () => {
    expect(forgeJourney.finish?.("reliant")).toEqual({ to: "/forge" });
    expect(forgeJourney.finish?.("forge")).toEqual({ to: "/apps" });
  });

  it("asks only for Next on the explaining steps", () => {
    for (const id of ["fg-console", "fg-instrumented", "fg-oncall", "fg-done"]) expect(conditionsOf(step(id)), id).toEqual([]);
  });

  describe("required actions", () => {
    it("fg-trace: the toured trace opened (?trace=)", () => {
      const { onFire, go, watcher } = watchStep("fg-trace", openSearch("fg-trace"));
      expect(watcher.initiallySatisfied).toBe(false);
      go({ view: "traces", trace: "0".repeat(32) });
      expect(onFire).not.toHaveBeenCalled();
      go({ view: "traces", trace: DEMO_TRACE_ID });
      expect(onFire).toHaveBeenCalledTimes(1);
      expect(step("fg-trace").autoAdvance).toBeFalsy();
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
      expect(step("fg-logs").autoAdvance).toBeFalsy();
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
      expect(step("fg-ask").afterAction).toBe("Copy something to try it.");
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

  it("lands Skip where the action would have: the next step opens with it done", () => {
    // Skipping fg-trace opens fg-logs on the trace; skipping fg-issue / fg-oncall reaches fg-ask on the issue.
    expect(openSearch("fg-logs")).toMatchObject({ view: "traces", trace: DEMO_TRACE_ID });
    expect(openSearch("fg-profile")).toMatchObject({ view: "traces", trace: DEMO_TRACE_ID });
    expect(openSearch("fg-ask")).toMatchObject({ view: "errors", issue: DEMO_ISSUE_ID });
  });

  it("spotlights the anchors the demo renders", () => {
    expect(step("fg-console").target).toBe("[data-tour='forge-console'] #sidebar-layout-nav");
    expect(step("fg-trace").phases?.[0].target).toBe(`[data-testid='obs-trace-row'][data-trace-id='${DEMO_TRACE_ID}']`);
    expect(step("fg-ask").phases?.map((p) => p.target)).toEqual(["[data-testid='ask-reliant-actions']", "[data-testid='ask-agent-sheet']"]);
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

    it("says Autopilot runs in Reliant, in the forge shell", () => {
      setTourShell("forge");
      render(<>{presentStep(step("fg-oncall")).description}</>);
      expect(screen.getByText(/Autopilot runs in Reliant/)).toBeInTheDocument();
      expect(screen.getByRole("link", { name: "Set it up in Reliant" })).toHaveAttribute(
        "href",
        "https://app.reliantlabs.io/workflows/library/builtin%3A%2F%2Fautopilot-oncall",
      );
    });
  });
});
