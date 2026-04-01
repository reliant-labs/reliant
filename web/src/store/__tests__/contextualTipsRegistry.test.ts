import { describe, expect, it } from "vitest";
import { describe, expect, it } from "vitest";
import {
  DEFAULT_CONTEXTUAL_TIP_STATE,
  getNextEligibleContextualTip,
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

describe("contextualTipsRegistry", () => {
  it("selects spawned-thread-intro when a spawned thread has two messages", () => {
    const nextTip = getNextEligibleContextualTip(
      createBaseContext(),
      JSON.parse(JSON.stringify(DEFAULT_CONTEXTUAL_TIP_STATE)),
    );

    expect(nextTip?.id).toBe("spawned-thread-intro");
  });

  it("selects worktree tip when chat threshold is reached without worktrees and no thread tip is eligible", () => {
    const state = JSON.parse(JSON.stringify(DEFAULT_CONTEXTUAL_TIP_STATE));
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
    const state = JSON.parse(JSON.stringify(DEFAULT_CONTEXTUAL_TIP_STATE));
    state["spawned-thread-intro"].shownCount = 1;
    state["spawned-thread-intro"].lastShownAt = "2024-12-31T00:00:00.000Z";

    const nextTip = getNextEligibleContextualTip(createBaseContext(), state);

    expect(nextTip?.id).toBe("spawned-thread-interact");
  });
});