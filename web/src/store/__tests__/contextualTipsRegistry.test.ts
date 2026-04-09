import { describe, expect, it } from "vitest";
import {
  CONTEXTUAL_TIPS_COOLDOWN_MS,
  DEFAULT_CONTEXTUAL_TIP_STATE,
  getNextEligibleContextualTip,
  type ContextualTipId,
  type ContextualTipStateRecord,
  type ContextualTipTriggerContext,
} from "../../components/Onboarding/contextualTipsRegistry";

function createBaseContext(): ContextualTipTriggerContext {
  return {
    onboardingComplete: true,
    isWizardActive: false,
    activeChatId: "chat-1",
    chats: [
      { id: "chat-1" } as never,
      { id: "chat-2" } as never,
      { id: "chat-3" } as never,
    ],
    activeMessages: [
      { thread: "thread-1" } as never,
      { thread: "thread-1" } as never,
    ],
    activeThreads: [
      {
        thread: "thread-1",
        spawned_by_node_id: "spawn-node",
      } as never,
    ],
    hasNonMainWorktree: false,
    threadInteractionEngaged: false,
    threadForceYieldEngaged: false,
    branchingEngaged: false,
    paramsEngaged: false,
    now: new Date("2025-01-01T12:00:00.000Z").getTime(),
    lastTipShownAt: null,
  };
}

function freshState(): Record<ContextualTipId, ContextualTipStateRecord> {
  return JSON.parse(JSON.stringify(DEFAULT_CONTEXTUAL_TIP_STATE));
}

describe("contextualTipsRegistry", () => {
  it("selects spawned-thread-intro when a spawned thread has two messages", () => {
    const nextTip = getNextEligibleContextualTip(
      createBaseContext(),
      freshState(),
    );

    expect(nextTip?.id).toBe("spawned-thread-intro");
  });

  it("selects worktree tip when chat threshold is reached without worktrees and no thread tip is eligible", () => {
    const state = freshState();
    state["spawned-thread-intro"].dismissedAt = "2025-01-01T11:00:00.000Z";

    const nextTip = getNextEligibleContextualTip(
      {
        ...createBaseContext(),
        activeMessages: [],
        activeThreads: [],
      },
      state,
    );

    expect(nextTip?.id).toBe("worktree-after-nth-chat");
  });

  it("selects spawned-thread-interact after intro has been shown", () => {
    const state = freshState();
    state["spawned-thread-intro"].shownCount = 1;
    state["spawned-thread-intro"].lastShownAt = "2024-12-31T00:00:00.000Z";

    const nextTip = getNextEligibleContextualTip(createBaseContext(), state);

    expect(nextTip?.id).toBe("spawned-thread-interact");
  });

  // ─── Failure / null-state safeguards ─────────────────────────────────

  it("returns null when onboarding is not complete", () => {
    const context = { ...createBaseContext(), onboardingComplete: false };
    const nextTip = getNextEligibleContextualTip(context, freshState());

    expect(nextTip).toBeNull();
  });

  it("returns null when wizard is active", () => {
    const context = { ...createBaseContext(), isWizardActive: true };
    const nextTip = getNextEligibleContextualTip(context, freshState());

    expect(nextTip).toBeNull();
  });

  it("returns null when all tips are dismissed", () => {
    const state = freshState();
    for (const key of Object.keys(state) as ContextualTipId[]) {
      state[key].dismissedAt = "2025-01-01T00:00:00.000Z";
    }

    const nextTip = getNextEligibleContextualTip(createBaseContext(), state);

    expect(nextTip).toBeNull();
  });

  it("returns null when global cooldown is active", () => {
    // Last tip was shown 1 minute ago — within the 10-minute global cooldown
    const oneMinuteAgo = new Date(
      new Date("2025-01-01T12:00:00.000Z").getTime() - 60_000,
    ).toISOString();

    const context = { ...createBaseContext(), lastTipShownAt: oneMinuteAgo };
    const nextTip = getNextEligibleContextualTip(context, freshState());

    expect(nextTip).toBeNull();
  });

  it("returns a tip when global cooldown has expired", () => {
    // Last tip was shown well beyond the cooldown window
    const longAgo = new Date(
      new Date("2025-01-01T12:00:00.000Z").getTime() - CONTEXTUAL_TIPS_COOLDOWN_MS - 1,
    ).toISOString();

    const context = { ...createBaseContext(), lastTipShownAt: longAgo };
    const nextTip = getNextEligibleContextualTip(context, freshState());

    expect(nextTip).not.toBeNull();
  });

  it("returns null with default state when there are no spawned threads and not enough chats", () => {
    // Default state + no threads + only 1 chat = nothing eligible
    const context: ContextualTipTriggerContext = {
      ...createBaseContext(),
      chats: [{ id: "chat-1" } as never],
      activeMessages: [],
      activeThreads: [],
    };
    const nextTip = getNextEligibleContextualTip(context, freshState());

    expect(nextTip).toBeNull();
  });
});
