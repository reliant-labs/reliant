/**
 * The workflows journey's content (journeys/workflows.tsx): each step's
 * required action is wired to the signal its emit site sends (emit → the
 * step's advanceOn fires), the Fix my PR seed, and the Reliant-only outro.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import yaml from "js-yaml";

const importWorkflow = vi.hoisted(() => vi.fn());
vi.mock("../../../hooks/workflow-queries", () => ({ importWorkflow }));
vi.mock("../../../hooks/trigger-queries", () => ({ useTriggers: () => ({ data: [] }) }));

import { watchAdvance, conditionsOf, type AdvanceEnv } from "../advanceOn";
import { tourEvents } from "../tourEvents";
import { setTourShell } from "../tourShell";
import { activeJourneySteps } from "../journeys/registry";
import { workflowsJourney, LOOP_CONDITION } from "../journeys/workflows";
import { layoutActions } from "../journeys/layoutActions";
import {
  FIX_MY_PR_START,
  FIX_MY_PR_TEMPLATE,
  fixMyPrName,
  fixMyPrYaml,
  seedFixMyPr,
} from "../journeys/fixMyPr";
import { enableTourNote, isWorkflowsTour } from "../journeys/workflowsTour";
import { useTourStore } from "../../../store/tourStore";
import type { JourneyStep } from "../types";

function fakeEnv(initial = { pathname: "/", search: {} as Record<string, unknown> }) {
  let location = { ...initial };
  let context: Record<string, unknown> = {};
  const locationListeners = new Set<() => void>();
  const contextListeners = new Set<() => void>();
  const env: AdvanceEnv = {
    getPathname: () => location.pathname,
    getSearch: () => location.search,
    subscribeLocation: (fn) => {
      locationListeners.add(fn);
      return () => locationListeners.delete(fn);
    },
    checklistHas: () => false,
    subscribeChecklist: () => () => undefined,
    getContext: () => context,
    subscribeContext: (fn) => {
      contextListeners.add(fn);
      return () => contextListeners.delete(fn);
    },
    document,
  };
  return {
    env,
    go(pathname: string) {
      location = { pathname, search: {} };
      locationListeners.forEach((fn) => fn());
    },
    setContext(next: Record<string, unknown>) {
      context = next;
      contextListeners.forEach((fn) => fn());
    },
  };
}

const step = (id: string): JourneyStep => {
  const found = [...workflowsJourney.steps, ...(workflowsJourney.outro ?? [])].find((s) => s.id === id);
  if (!found) throw new Error(`no step ${id}`);
  return found;
};

/** Start watching a step's advanceOn, as the engine does when the step opens. */
function open(id: string, at = "/workflow/fix-my-pr") {
  const fake = fakeEnv({ pathname: at, search: {} });
  const onFire = vi.fn();
  const s = step(id);
  const watcher = watchAdvance(conditionsOf(s), { target: s.target }, fake.env, onFire);
  return { ...fake, onFire, watcher };
}

let disposers: (() => void)[] = [];
afterEach(() => {
  disposers.forEach((d) => d());
  disposers = [];
  setTourShell(null);
});
function track<T extends { watcher: { dispose: () => void } }>(t: T): T {
  disposers.push(t.watcher.dispose);
  return t;
}

describe("workflows journey: the steps", () => {
  it("has the spec's nine steps, in order, then the outro", () => {
    expect(workflowsJourney.steps.map((s) => s.id)).toEqual([
      "wf-new",
      "wf-edge",
      "wf-loop",
      "wf-branch",
      "wf-trigger",
      "wf-enable",
      "wf-machines",
      "wf-automations",
      "wf-params",
    ]);
    expect(workflowsJourney.version).toBeGreaterThanOrEqual(2);
    expect(workflowsJourney.reshowAfterVersionBump).not.toBe(false);
  });

  it("never targets the retired Automations tab", () => {
    const routes = workflowsJourney.steps.map((s) => s.route?.path ?? "");
    expect(routes.some((p) => p.startsWith("/workflows/automations"))).toBe(false);
    expect(step("wf-automations").target).toBe("[data-tour='workflows-show-on']");
  });

  it("anchors Activate by data-tour, not by its label", () => {
    expect(step("wf-enable").target).toBe("[data-tour='activate-trigger']");
  });

  it("copy has no em dashes", () => {
    const text = JSON.stringify(workflowsJourney, (_k, v) => (typeof v === "function" ? undefined : v));
    expect(text).not.toContain("—");
  });
});

describe("workflows journey: advanceOn wiring (emit → advance)", () => {
  it("W1 wf-new: the guided draft exists AND the builder opened on it", () => {
    const t = track(open("wf-new", "/workflow/new"));
    tourEvents.emit("workflow:draftCreated", { slug: "fix-my-pr", draftId: "d1", start: "other" });
    t.go("/workflow/fix-my-pr");
    expect(t.onFire).not.toHaveBeenCalled(); // not the guided start
    t.go("/workflow/new");
    tourEvents.emit("workflow:draftCreated", { slug: "fix-my-pr", draftId: "d1", start: FIX_MY_PR_START });
    expect(t.onFire).not.toHaveBeenCalled(); // still on /workflow/new
    t.go("/workflow/fix-my-pr");
    expect(t.onFire).toHaveBeenCalledTimes(1);
    expect(step("wf-new").autoAdvance).toBe(true);
  });

  it("W2 wf-edge: only review → fix_until_green", () => {
    const t = track(open("wf-edge"));
    tourEvents.emit("workflow:edgeConnected", { sourceId: "review", targetId: "merge" });
    expect(t.onFire).not.toHaveBeenCalled();
    tourEvents.emit("workflow:edgeConnected", { sourceId: "review", targetId: "fix_until_green" });
    expect(t.onFire).toHaveBeenCalledTimes(1);
  });

  it("W3 wf-loop: selecting the loop, or expanding it", () => {
    const a = track(open("wf-loop"));
    tourEvents.emit("workflow:nodeSelected", { nodeId: "review", type: "workflow" });
    expect(a.onFire).not.toHaveBeenCalled();
    tourEvents.emit("workflow:nodeSelected", { nodeId: "fix_until_green", type: "loop" });
    expect(a.onFire).toHaveBeenCalledTimes(1);

    const b = track(open("wf-loop"));
    tourEvents.emit("workflow:loopExpanded", { nodeId: "fix_until_green" });
    expect(b.onFire).toHaveBeenCalledTimes(1);
    expect(step("wf-loop").autoAdvance).toBeFalsy(); // they read the panel
  });

  it("W4 wf-branch: an Approval, anywhere", () => {
    const t = track(open("wf-branch"));
    tourEvents.emit("workflow:nodeAdded", { nodeId: "agent", type: "workflow", anchor: "splice", spliceTarget: "merge" });
    expect(t.onFire).not.toHaveBeenCalled();
    tourEvents.emit("workflow:nodeAdded", { nodeId: "approval", type: "approval", anchor: "end" });
    expect(t.onFire).toHaveBeenCalledTimes(1);
  });

  it("W5 wf-trigger: a Schedule declared, then a save that includes it", () => {
    const t = track(open("wf-trigger"));
    tourEvents.emit("workflow:saved", { slug: "fix-my-pr", triggerSources: [] });
    tourEvents.emit("workflow:triggerDeclared", { name: "hook", source: "webhook" });
    expect(t.onFire).not.toHaveBeenCalled();
    tourEvents.emit("workflow:triggerDeclared", { name: "schedule", source: "schedule" });
    expect(t.onFire).not.toHaveBeenCalled(); // not saved yet
    tourEvents.emit("workflow:saved", { slug: "fix-my-pr", triggerSources: ["schedule"] });
    expect(t.onFire).toHaveBeenCalledTimes(1);
  });

  it("W5's skip records it, so W6 runs Next-only", async () => {
    const setContext = vi.fn();
    await step("wf-trigger").skip?.run?.({ context: {}, setContext });
    expect(setContext).toHaveBeenCalledWith({ scheduleSkipped: true });
    const skipped = fakeEnv({ pathname: "/workflow/fix-my-pr", search: {} });
    skipped.setContext({ scheduleSkipped: true });
    const watcher = watchAdvance(conditionsOf(step("wf-enable")), {}, skipped.env, vi.fn());
    expect(watcher.initiallySatisfied).toBe(true); // Next shows at once
    watcher.dispose();
  });

  it("W6 wf-enable: the dialog opened, then a machine chosen or the dialog closed; never Enable", () => {
    const closed = track(open("wf-enable"));
    tourEvents.emit("automation:dialogOpened", { workflowRef: "fix-my-pr", declaredTrigger: "schedule" });
    expect(closed.onFire).not.toHaveBeenCalled();
    tourEvents.emit("automation:dialogClosed", { workflowRef: "fix-my-pr", declaredTrigger: "schedule" });
    expect(closed.onFire).toHaveBeenCalledTimes(1);

    const chosen = track(open("wf-enable"));
    tourEvents.emit("automation:machineChosen", { machine: "none" });
    expect(chosen.onFire).not.toHaveBeenCalled(); // the dialog never opened
    tourEvents.emit("automation:dialogOpened", { workflowRef: "fix-my-pr" });
    tourEvents.emit("automation:machineChosen", { machine: "daemon-1" });
    expect(chosen.onFire).toHaveBeenCalledTimes(1);
  });

  it("W7 wf-machines: reaching Machines", () => {
    const t = track(open("wf-machines", "/workflows/library"));
    expect(t.watcher.initiallySatisfied).toBe(false);
    t.go("/settings/machines");
    expect(t.onFire).toHaveBeenCalledTimes(1);
    const already = track(open("wf-machines", "/settings/machines"));
    expect(already.watcher.initiallySatisfied).toBe(true);
  });

  it("W8 and W9 are looks: Next, no required action", () => {
    expect(step("wf-automations").advanceOn).toBeUndefined();
    expect(step("wf-params").advanceOn).toBeUndefined();
    expect(step("wf-params").target).toBe("[data-tour='workflow-inputs']");
  });

  it("the builder steps open the draft W1 recorded", () => {
    const route = step("wf-params").route!;
    expect(typeof route.open === "function" && route.open({ workflowSlug: "fix-my-pr-2" })).toEqual({
      to: "/workflow/$workflowName",
      params: { workflowName: "fix-my-pr-2" },
    });
    expect(typeof route.open === "function" && route.open({})).toBeNull();
  });
});

describe("workflows journey: the outro", () => {
  it("shows only in the Reliant shell, and collapses the panels when done", () => {
    setTourShell("reliant");
    expect(activeJourneySteps("workflows").at(-1)?.id).toBe("wf-outro-layout");
    expect(step("wf-outro-layout").onComplete).toBe(layoutActions.collapseAll);
    setTourShell("forge");
    expect(activeJourneySteps("workflows").map((s) => s.id)).not.toContain("wf-outro-layout");
  });

  it("points at the chat and the panel buttons, and says files and terminal are on the right", () => {
    const outro = step("wf-outro-layout");
    expect(outro.targets?.map((t) => t.selector)).toEqual([
      "[data-onboarding='chat-input']",
      "[aria-label='Toggle Chat Sidebar']",
      "[aria-label='Toggle File Browser']",
      "[aria-label='Toggle Terminal']",
    ]);
    expect(String(outro.description)).toMatch(/files and the terminal open on the right/);
  });
});

describe("the Fix my PR seed", () => {
  beforeEach(() => {
    importWorkflow.mockReset();
    useTourStore.setState({ contexts: {} } as never);
  });

  it("is the spec's deliberately incomplete graph", () => {
    const wf = yaml.load(FIX_MY_PR_TEMPLATE) as {
      name: string;
      title: string;
      inputs: Record<string, unknown>;
      entry: string[];
      nodes: { id: string; type: string; while?: string; passthrough?: string[] }[];
      edges: { from: string; default?: string; cases?: { to: string; condition: string }[] }[];
    };
    expect(wf.name).toBe("fix-my-pr");
    expect(wf.title).toBe("Fix my PR");
    expect(Object.keys(wf.inputs)).toEqual(["pr", "test_command"]);
    expect(wf.entry).toEqual(["review"]);
    expect(wf.nodes.map((n) => n.id)).toEqual(["review", "fix_until_green", "merge", "report"]);
    const loop = wf.nodes.find((n) => n.id === "fix_until_green")!;
    expect(loop).toMatchObject({ type: "loop", while: LOOP_CONDITION, passthrough: ["test_command"] });
    // Left for the user: review -> fix_until_green (W2) and the Approval (W4).
    expect(wf.edges.some((e) => e.from === "review")).toBe(false);
    expect(wf.nodes.some((n) => n.type === "approval")).toBe(false);
    // The switch: a case into merge and a default into report.
    expect(wf.edges).toEqual([
      expect.objectContaining({ from: "fix_until_green", default: "report", cases: [expect.objectContaining({ to: "merge" })] }),
    ]);
    expect((wf as { triggers?: unknown }).triggers).toBeUndefined();
  });

  it("takes the slug and title it is imported under", () => {
    const wf = yaml.load(fixMyPrYaml("fix-my-pr-3", "Fix: my \"PR\"")) as { name: string; title: string };
    expect(wf).toMatchObject({ name: "fix-my-pr-3", title: 'Fix: my "PR"' });
    expect(fixMyPrName(0)).toEqual({ slug: "fix-my-pr", title: "Fix my PR" });
    expect(fixMyPrName(1)).toEqual({ slug: "fix-my-pr-2", title: "Fix my PR 2" });
  });

  it("derives the slug from the title the user typed, like New workflow's other starts", () => {
    expect(fixMyPrName(0, "  Ship_it: my PR!  ")).toEqual({ slug: "ship-it-my-pr", title: "Ship_it: my PR!" });
    expect(fixMyPrName(2, "Ship it")).toEqual({ slug: "ship-it-3", title: "Ship it 3" });
    expect(fixMyPrName(0, "!!!")).toEqual({ slug: "fix-my-pr", title: "!!!" });
    expect(fixMyPrName(0, "   ")).toEqual({ slug: "fix-my-pr", title: "Fix my PR" });
  });

  it("imports a renamed draft under the slug of its new name", async () => {
    importWorkflow.mockResolvedValueOnce({ success: true, slug: "ship-it", id: "d9", status: "draft" });
    const seeded = await seedFixMyPr("project-1", "Ship it");
    expect(importWorkflow).toHaveBeenCalledWith("project-1", expect.stringMatching(/^name: ship-it$/m));
    expect(importWorkflow).toHaveBeenCalledWith("project-1", expect.stringMatching(/^title: "Ship it"$/m));
    expect(seeded).toEqual({ slug: "ship-it", draftId: "d9", title: "Ship it" });
  });

  it("imports under the first free slug, records it, and emits workflow:draftCreated", async () => {
    importWorkflow
      .mockResolvedValueOnce({ success: false, conflict: true, message: "exists" })
      .mockResolvedValueOnce({ success: true, slug: "fix-my-pr-2", id: "draft-2", status: "draft" });
    const seen = vi.fn();
    const off = tourEvents.on("workflow:draftCreated", seen);
    const seeded = await seedFixMyPr("project-1");
    off();
    expect(importWorkflow).toHaveBeenNthCalledWith(1, "project-1", expect.stringMatching(/^name: fix-my-pr$/m));
    expect(importWorkflow).toHaveBeenNthCalledWith(2, "project-1", expect.stringMatching(/^name: fix-my-pr-2$/m));
    expect(importWorkflow).toHaveBeenNthCalledWith(2, "project-1", expect.stringMatching(/^title: "Fix my PR 2"$/m));
    expect(seeded).toEqual({ slug: "fix-my-pr-2", draftId: "draft-2", title: "Fix my PR 2" });
    expect(seen).toHaveBeenCalledWith({ slug: "fix-my-pr-2", draftId: "draft-2", start: FIX_MY_PR_START });
    expect(useTourStore.getState().contexts.workflows).toMatchObject({ workflowSlug: "fix-my-pr-2", draftId: "draft-2" });
  });

  it("surfaces a failed import", async () => {
    importWorkflow.mockResolvedValueOnce({ success: false, conflict: false, message: "invalid YAML" });
    await expect(seedFixMyPr("project-1")).rejects.toThrow("invalid YAML");
  });

  it("W1's Create it for me seeds it, and stays on the step when that fails", async () => {
    const { useProjectStore } = await import("../../../store/projectStore");
    useProjectStore.setState({ currentProject: { id: "project-1" } } as never);
    importWorkflow.mockResolvedValueOnce({ success: true, slug: "fix-my-pr", id: "d1" });
    const run = step("wf-new").skip!.run!;
    await run({ context: {}, setContext: vi.fn() });
    expect(useTourStore.getState().contexts.workflows).toMatchObject({ workflowSlug: "fix-my-pr" });
    importWorkflow.mockRejectedValueOnce(new Error("offline"));
    await expect(run({ context: {}, setContext: vi.fn() })).rejects.toThrow("offline");
  });
});

describe("workflowsTour helpers", () => {
  it("recognise the workflows journey's ?tour=, and W6's dialog note", () => {
    expect(isWorkflowsTour("workflows:wf-new")).toBe(true);
    expect(isWorkflowsTour("wf-new")).toBe(true);
    expect(isWorkflowsTour("forge:fg-console")).toBe(false);
    expect(isWorkflowsTour(undefined)).toBe(false);
    expect(enableTourNote("workflows:wf-enable")).toMatch(/up to you/);
    expect(enableTourNote("workflows:wf-trigger")).toBeUndefined();
  });
});
