/**
 * The workflows journey's content (journeys/workflows.tsx): it SHOWS a
 * finished example rather than asking the user to build one. Covered here:
 * the step wiring, that no step requires a build action, the example
 * template and its seeding, the Make your own ending, the Reliant-only
 * outro, and the phone deck.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import React from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import yaml from "js-yaml";

const importWorkflow = vi.hoisted(() => vi.fn());
const fetchWorkflow = vi.hoisted(() => vi.fn());
vi.mock("../../../hooks/workflow-queries", () => ({ importWorkflow, fetchWorkflow }));

import { watchAdvance, conditionsOf, type AdvanceEnv } from "../advanceOn";
import { tourEvents } from "../tourEvents";
import { setTourShell } from "../tourShell";
// The workflows journey's content is registered by the Reliant shells (journeys/catalog.ts).
import "../journeys/catalog";
import { activeJourneySteps } from "../journeys/registry";
import { workflowsJourney, _makeOwnRequested } from "../journeys/workflows";
import { LESSONS, LOOP_CONDITION } from "../journeys/workflowsLessons";
import { FOCUS_TARGET } from "../journeys/workflowsCanvas";
import { WorkflowsPhoneDeck } from "../journeys/workflowsPhone";
import { layoutActions } from "../journeys/layoutActions";
import {
  EXAMPLE_SLUG,
  EXAMPLE_PROJECT,
  FIX_MY_PR_START,
  FIX_MY_PR_TEMPLATE,
  ensureFixMyPrExample,
  fixMyPrName,
  fixMyPrYaml,
  seedFixMyPr,
} from "../journeys/fixMyPr";
import { useTourStore } from "../../../store/tourStore";
import type { JourneyStep } from "../types";

const STEPS = ["wf-example", "wf-edge", "wf-loop", "wf-branch", "wf-trigger", "wf-machine", "wf-inputs"];

/** Signals that only fire when the user BUILDS something (creates, adds, declares, saves, enables). */
const BUILD_SIGNALS = [
  "workflow:draftCreated",
  "workflow:nodeAdded",
  "workflow:triggerDeclared",
  "workflow:saved",
  "automation:dialogOpened",
  "automation:machineChosen",
];

function fakeEnv(pathname = "/workflow/fix-my-pr") {
  const env: AdvanceEnv = {
    getPathname: () => pathname,
    getSearch: () => ({}),
    subscribeLocation: () => () => undefined,
    checklistHas: () => false,
    subscribeChecklist: () => () => undefined,
    getContext: () => ({}),
    subscribeContext: () => () => undefined,
    document,
  };
  return env;
}

const all = (): JourneyStep[] => [...workflowsJourney.steps, ...(workflowsJourney.outro ?? [])];
const step = (id: string): JourneyStep => {
  const found = all().find((s) => s.id === id);
  if (!found) throw new Error(`no step ${id}`);
  return found;
};

afterEach(() => setTourShell(null));

describe("workflows journey: the steps", () => {
  it("is version 3, seven canvas lessons then the outro: eight in Reliant", () => {
    expect(workflowsJourney.version).toBe(3);
    expect(workflowsJourney.steps.map((s) => s.id)).toEqual(STEPS);
    setTourShell("reliant");
    expect(activeJourneySteps("workflows")).toHaveLength(8);
    expect(activeJourneySteps("workflows").at(-1)?.id).toBe("wf-outro-layout");
  });

  it("uses the in-callout controls", () => {
    expect(workflowsJourney.controls).toBe("callout");
  });

  it("has a lesson for each step, with the same title", () => {
    expect(LESSONS.map((l) => l.id)).toEqual(STEPS);
    for (const lesson of LESSONS) expect(step(lesson.id).title).toBe(lesson.title);
  });

  it("explains every canvas idea beside the steps it is about: the focus box, card to the side", () => {
    for (const id of ["wf-example", "wf-edge", "wf-loop", "wf-branch", "wf-trigger", "wf-machine"]) {
      expect(step(id).target, id).toBe(FOCUS_TARGET);
      expect(step(id).tooltipPosition, id).toBe("right");
    }
    expect(step("wf-inputs").target).toBe("[data-tour='workflow-inputs']");
  });

  it("requires no build action anywhere: Next is always there", () => {
    for (const s of all()) {
      const conditions = conditionsOf(s);
      if (conditions.length === 0) continue;
      // An action is only ever an optional shortcut forward.
      expect(s.showNext, `${s.id} hides Next behind an action`).toBe(true);
      for (const c of conditions) {
        if (c.kind === "event") expect(BUILD_SIGNALS, s.id).not.toContain(c.event);
        expect(["click", "route", "checklist", "element", "store"], s.id).not.toContain(c.kind);
      }
      expect(s.skip, s.id).toBeUndefined();
    }
  });

  it("the edge step's Try it yourself: drawing review → fix_until_green moves on; nothing else does", () => {
    const s = step("wf-edge");
    const onFire = vi.fn();
    const watcher = watchAdvance(conditionsOf(s), { target: s.target }, fakeEnv(), onFire);
    tourEvents.emit("workflow:edgeConnected", { sourceId: "review", targetId: "merge" });
    expect(onFire).not.toHaveBeenCalled();
    tourEvents.emit("workflow:edgeConnected", { sourceId: "review", targetId: "fix_until_green" });
    expect(onFire).toHaveBeenCalledTimes(1);
    watcher.dispose();
    expect(s.autoAdvance).toBe(true);
  });

  it("the builder steps open the example the first step recorded", () => {
    const route = step("wf-loop").route!;
    expect(typeof route.open === "function" && route.open({ [EXAMPLE_SLUG]: "fix-my-pr-2" })).toEqual({
      to: "/workflow/$workflowName",
      params: { workflowName: "fix-my-pr-2" },
    });
    // Never the unfinished draft the previous version of the tour made.
    expect(typeof route.open === "function" && route.open({ workflowSlug: "fix-my-pr" })).toBeNull();
    // The first step opens the example itself.
    expect(step("wf-example").route).toBeUndefined();
  });

  it("says the loop's exit condition", () => {
    const loop = LESSONS.find((l) => l.id === "wf-loop")!;
    const { container } = render(<>{loop.body}</>);
    expect(container.textContent).toContain(LOOP_CONDITION);
  });

  it("copy has no em dashes", () => {
    for (const lesson of LESSONS) {
      const { container, unmount } = render(<>{lesson.body}</>);
      expect(container.textContent, lesson.id).not.toMatch(/—/);
      unmount();
    }
  });
});

describe("workflows journey: the end", () => {
  it("Finish lands on the chat; Make your own lands on New workflow, once", () => {
    expect(workflowsJourney.finish?.("reliant")).toBeNull();
    expect(_makeOwnRequested()).toBe(false);
  });

  it("the outro shows only in Reliant, offers Make your own, and collapses the panels when done", () => {
    setTourShell("reliant");
    const outro = step("wf-outro-layout");
    expect(outro.onComplete).toBe(layoutActions.collapseAll);
    expect(outro.targets?.map((t) => t.selector)).toEqual([
      "[data-onboarding='chat-input']",
      "[aria-label='Toggle Chat Sidebar']",
      "[aria-label='Toggle File Browser']",
      "[aria-label='Toggle Terminal']",
    ]);
    setTourShell("forge");
    expect(activeJourneySteps("workflows").map((s) => s.id)).not.toContain("wf-outro-layout");
  });
});

describe("the Fix my PR example", () => {
  beforeEach(() => {
    importWorkflow.mockReset();
    fetchWorkflow.mockReset();
    useTourStore.setState({ contexts: {} } as never);
  });

  it("is complete and wired: review → loop → switch → approval → merge, or report; a weekday schedule", () => {
    const wf = yaml.load(FIX_MY_PR_TEMPLATE) as {
      name: string;
      title: string;
      inputs: Record<string, unknown>;
      entry: string[];
      nodes: { id: string; type: string; while?: string; passthrough?: string[]; condition?: string }[];
      edges: { from: string; default?: string; cases?: { to: string; condition: string }[] }[];
      triggers: { name: string; schedule: { cron: string[]; timezone: string } }[];
    };
    expect(wf).toMatchObject({ name: "fix-my-pr", title: "Fix my PR", entry: ["review"] });
    expect(Object.keys(wf.inputs)).toEqual(["pr", "test_command"]);
    expect(wf.nodes.map((n) => [n.id, n.type])).toEqual([
      ["review", "workflow"],
      ["fix_until_green", "loop"],
      ["approve_merge", "approval"],
      ["merge", "workflow"],
      ["report", "workflow"],
    ]);
    expect(wf.nodes[1]).toMatchObject({ while: LOOP_CONDITION, passthrough: ["test_command"] });
    expect(wf.edges).toEqual([
      { from: "review", default: "fix_until_green" },
      expect.objectContaining({ from: "fix_until_green", default: "report", cases: [expect.objectContaining({ to: "approve_merge" })] }),
      { from: "approve_merge", default: "merge" },
    ]);
    expect(wf.triggers).toEqual([
      expect.objectContaining({ name: "weekday-mornings", schedule: { cron: ["0 9 * * 1-5"], timezone: "UTC" } }),
    ]);
  });

  it("takes the slug, title and timezone it is imported under", () => {
    const wf = yaml.load(fixMyPrYaml("fix-my-pr-3", 'Fix: my "PR"', "Europe/London")) as {
      name: string;
      title: string;
      triggers: { schedule: { timezone: string } }[];
    };
    expect(wf).toMatchObject({ name: "fix-my-pr-3", title: 'Fix: my "PR"' });
    expect(wf.triggers[0].schedule.timezone).toBe("Europe/London");
    // Anything that isn't a zone name stays UTC.
    const odd = yaml.load(fixMyPrYaml("x", "X", "not a zone\nevil: 1")) as { triggers: { schedule: { timezone: string } }[] };
    expect(odd.triggers[0].schedule.timezone).toBe("UTC");
    expect(fixMyPrName(1)).toEqual({ slug: "fix-my-pr-2", title: "Fix my PR 2" });
  });

  it("imports under the first free slug and emits workflow:draftCreated", async () => {
    importWorkflow
      .mockResolvedValueOnce({ success: false, conflict: true, message: "exists" })
      .mockResolvedValueOnce({ success: true, slug: "fix-my-pr-2", id: "draft-2", status: "draft" });
    const seen = vi.fn();
    const off = tourEvents.on("workflow:draftCreated", seen);
    const seeded = await seedFixMyPr("project-1");
    off();
    expect(importWorkflow).toHaveBeenNthCalledWith(2, "project-1", expect.stringMatching(/^name: fix-my-pr-2$/m));
    expect(seeded).toEqual({ slug: "fix-my-pr-2", draftId: "draft-2", title: "Fix my PR 2" });
    expect(seen).toHaveBeenCalledWith({ slug: "fix-my-pr-2", draftId: "draft-2", start: FIX_MY_PR_START });
  });

  it("is added once per project: reused while it exists, added again when it's gone or the project changed", async () => {
    importWorkflow.mockResolvedValue({ success: true, slug: "fix-my-pr", id: "d1" });
    expect(await ensureFixMyPrExample("project-1")).toBe("fix-my-pr");
    expect(importWorkflow).toHaveBeenCalledTimes(1);
    expect(useTourStore.getState().contexts.workflows).toMatchObject({ [EXAMPLE_SLUG]: "fix-my-pr", [EXAMPLE_PROJECT]: "project-1" });

    fetchWorkflow.mockResolvedValueOnce({ workflow: { name: "fix-my-pr" } });
    expect(await ensureFixMyPrExample("project-1")).toBe("fix-my-pr");
    expect(importWorkflow).toHaveBeenCalledTimes(1);

    fetchWorkflow.mockRejectedValueOnce(new Error("not found"));
    await ensureFixMyPrExample("project-1");
    expect(importWorkflow).toHaveBeenCalledTimes(2);

    await ensureFixMyPrExample("project-2");
    expect(importWorkflow).toHaveBeenCalledTimes(3);
  });

  it("surfaces a failed import", async () => {
    importWorkflow.mockResolvedValueOnce({ success: false, conflict: false, message: "invalid YAML" });
    await expect(ensureFixMyPrExample("project-1")).rejects.toThrow("invalid YAML");
  });
});

describe("the phone deck (Journey.phone)", () => {
  it("is the journey's phone presentation", () => {
    expect(workflowsJourney.phone).toBe(WorkflowsPhoneDeck);
  });

  it("shows every lesson with a still diagram; Next and Back walk it, Done finishes, Exit ends", () => {
    const onFinish = vi.fn();
    const onExit = vi.fn();
    render(<WorkflowsPhoneDeck onFinish={onFinish} onExit={onExit} />);
    for (const lesson of LESSONS) expect(screen.getByTestId(`wf-phone-card-${lesson.id}`)).toBeInTheDocument();
    expect(screen.getAllByTestId("wf-diagram")).toHaveLength(LESSONS.length);
    expect(document.querySelector("[data-testid='wf-phone-deck'] animate, [data-testid='wf-phone-deck'] animateMotion")).toBeNull();

    expect(screen.getByTestId("wf-phone-count")).toHaveTextContent("Step 1 of 7");
    expect(screen.queryByTestId("wf-phone-back")).toBeNull();
    for (let i = 1; i < LESSONS.length; i++) fireEvent.click(screen.getByTestId("wf-phone-next"));
    expect(screen.getByTestId("wf-phone-count")).toHaveTextContent("Step 7 of 7");
    expect(screen.getByTestId("wf-phone-next")).toHaveTextContent("Done");
    expect(screen.queryByTestId("wf-phone-exit")).toBeNull();
    fireEvent.click(screen.getByTestId("wf-phone-back"));
    expect(screen.getByTestId("wf-phone-count")).toHaveTextContent("Step 6 of 7");
    fireEvent.click(screen.getByTestId("wf-phone-exit"));
    expect(onExit).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByTestId("wf-phone-next"));
    fireEvent.click(screen.getByTestId("wf-phone-next"));
    expect(onFinish).toHaveBeenCalledTimes(1);
  });
});

void React;
