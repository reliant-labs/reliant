/**
 * Tour step transitions must not block on settings RPCs.
 *
 * THE BUG THIS CLOSES (2026-08-26, packaged desktop): "switching between step
 * 3-4 of the onboarding tour was crazy slow." Each transition awaited
 * `saveTourState()`, which is THREE settings RPCs (completed flag, completed
 * set, skipped set), before the next step could paint.
 *
 * Measured, from the client's own in-flight snapshot at 21:45:58.622:
 *
 *   GetDefaultPreset timed out after 10000ms — 4 in flight,
 *     [GetDefaultPreset:10s, UpdateSetting:1s, UpdateSetting:1s, UpdateSetting:1s]
 *
 * Three UpdateSetting calls — one save — sitting at 1s apiece against a
 * backend stalled ~19s on mcp.ensure_loaded. Going back and forth re-paid it
 * per transition.
 *
 * The fix is optimistic + coalesced persistence: in-memory state (what the UI
 * renders from) updates synchronously, and the write is a debounced background
 * detail. Rapid transitions collapse to ONE save rather than N×3 RPCs.
 */
/* eslint-disable @typescript-eslint/no-explicit-any */
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";

const persistence = vi.hoisted(() => ({
  upsertStringSetting: vi.fn(async () => undefined),
}));

vi.mock("../../lib/settingsPersistence", () => ({
  safeGetSetting: vi.fn(async () => null),
  upsertStringSetting: persistence.upsertStringSetting,
  deleteSettingIfExists: vi.fn(async () => undefined),
}));

vi.mock("../../lib/analytics", () => ({ trackEvent: vi.fn() }));

vi.mock("../../api/fileSystem", () => ({ getFileTree: vi.fn(async () => []) }));

vi.mock("../onboardingChecklistStore", () => ({
  useOnboardingChecklistStore: {
    getState: () => ({
      markComplete: vi.fn(async () => undefined),
      markWelcomeShown: vi.fn(async () => undefined),
    }),
    setState: vi.fn(),
  },
}));

import { useTourStore, __resetTourSaveScheduler } from "../tourStore";

describe("tourStore — non-blocking step persistence", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    __resetTourSaveScheduler();
    useTourStore.setState({
      completedSteps: new Set(),
      skippedSteps: new Set(),
      hasCompletedOnboarding: false,
    } as any);
  });

  afterEach(() => {
    __resetTourSaveScheduler();
    vi.useRealTimers();
  });

  it("completeStep resolves without waiting on any settings RPC", async () => {
    // The UI awaits this before advancing. If it awaits the write, the user
    // pays backend latency per Next click — the reported slowness.
    await useTourStore.getState().completeStep("workflow-intro");

    expect(persistence.upsertStringSetting).not.toHaveBeenCalled();
    // ...but the state the UI renders from is already updated.
    expect(
      (useTourStore.getState() as any).completedSteps.has("workflow-intro"),
    ).toBe(true);
  });

  it("coalesces a burst of transitions into a single save", async () => {
    // "went back and forth a few times" — six transitions used to mean
    // six saves × 3 RPCs = 18 round-trips.
    const store = useTourStore.getState();
    await store.completeStep("chat-and-sidebars");
    await store.completeStep("workspaces");
    await store.completeStep("workflow-intro");

    expect(persistence.upsertStringSetting).not.toHaveBeenCalled();

    await vi.runAllTimersAsync();

    // One save == exactly three keys, written once each.
    expect(persistence.upsertStringSetting).toHaveBeenCalledTimes(3);
  });

  it("eventually persists the accumulated step state", async () => {
    await useTourStore.getState().completeStep("workflow-intro");
    await useTourStore.getState().skipStep("workspaces");

    await vi.runAllTimersAsync();

    const written = new Map(
      persistence.upsertStringSetting.mock.calls.map(
        ([key, value]: any) => [key, value] as const,
      ),
    );
    const completed = [...written.entries()].find(([k]) =>
      k.includes("completed_steps"),
    );
    const skipped = [...written.entries()].find(([k]) =>
      k.includes("skipped_steps"),
    );
    expect(completed?.[1]).toContain("workflow-intro");
    expect(skipped?.[1]).toContain("workspaces");
  });

  it("an explicit saveTourState supersedes the queued write", async () => {
    // markTourCompleted / resetTourProgress await a real save. A trailing
    // debounce firing afterwards would spend three more RPCs saying nothing.
    await useTourStore.getState().completeStep("workflow-intro");
    await useTourStore.getState().saveTourState();

    expect(persistence.upsertStringSetting).toHaveBeenCalledTimes(3);

    await vi.runAllTimersAsync();

    expect(persistence.upsertStringSetting).toHaveBeenCalledTimes(3);
  });
});
