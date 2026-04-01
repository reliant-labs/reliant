import type { ActiveThreadUpdate } from "../../types/streaming";
import type { Message } from "../../types/chat";
import type { Chat } from "../../api/client";

export type ContextualTipId =
  | "spawned-thread-intro"
  | "spawned-thread-interact"
  | "thread-model"
  | "chat-branching"
  | "worktree-after-nth-chat";

export interface ContextualTipStateRecord {
  shownCount: number;
  lastShownAt: string | null;
  dismissedAt: string | null;
  engagedAt: string | null;
}

export interface ContextualTipTriggerContext {
  onboardingComplete: boolean;
  isWizardActive: boolean;
  activeChatId: string | null;
  chats: Chat[];
  activeMessages: Message[];
  activeThreads: ActiveThreadUpdate[];
  hasNonMainWorktree: boolean;
  threadInteractionEngaged: boolean;
  threadForceYieldEngaged: boolean;
  branchingEngaged: boolean;
  paramsEngaged: boolean;
  now: number;
  lastTipShownAt: string | null;
}

export interface ContextualTipDefinition {
  id: ContextualTipId;
  title: string;
  body: string;
  targetSelector: string;
  priority: number;
  cooldownMs: number;
  shouldShow: (
    context: ContextualTipTriggerContext,
    state: Record<ContextualTipId, ContextualTipStateRecord>,
  ) => boolean;
}

export const CONTEXTUAL_TIPS_SETTINGS_KEYS = {
  DISABLED: "contextual_tips.disabled",
  STATE: "contextual_tips.state",
} as const;

export const CONTEXTUAL_TIPS_LOCAL_STORAGE_KEYS = {
  DISABLED: "reliant.contextualTips.disabled",
  STATE: "reliant.contextualTips.state",
} as const;

export const DEFAULT_CONTEXTUAL_TIP_STATE: Record<ContextualTipId, ContextualTipStateRecord> = {
  "spawned-thread-intro": {
    shownCount: 0,
    lastShownAt: null,
    dismissedAt: null,
    engagedAt: null,
  },
  "spawned-thread-interact": {
    shownCount: 0,
    lastShownAt: null,
    dismissedAt: null,
    engagedAt: null,
  },
  "thread-model": {
    shownCount: 0,
    lastShownAt: null,
    dismissedAt: null,
    engagedAt: null,
  },
  "chat-branching": {
    shownCount: 0,
    lastShownAt: null,
    dismissedAt: null,
    engagedAt: null,
  },
  "worktree-after-nth-chat": {
    shownCount: 0,
    lastShownAt: null,
    dismissedAt: null,
    engagedAt: null,
  },
};

export const CONTEXTUAL_TIPS_COOLDOWN_MS = 1000 * 60 * 10;
const THREAD_TIP_COOLDOWN_MS = 1000 * 60 * 30;
const NTH_CHAT_WORKTREE_THRESHOLD = 3;

function parseTimestamp(value: string | null): number | null {
  if (!value) return null;
  const parsed = new Date(value).getTime();
  return Number.isNaN(parsed) ? null : parsed;
}

function isTipDismissed(record: ContextualTipStateRecord): boolean {
  return Boolean(record.dismissedAt || record.engagedAt);
}

function isWithinCooldown(
  lastShownAt: string | null,
  cooldownMs: number,
  now: number,
): boolean {
  const timestamp = parseTimestamp(lastShownAt);
  if (timestamp == null) return false;
  return now - timestamp < cooldownMs;
}

function hasSpawnedThreadWithMessages(
  context: ContextualTipTriggerContext,
  minimumMessages: number,
): boolean {
  if (!context.activeChatId) return false;

  const spawnedThreadIds = new Set(
    context.activeThreads
      .filter((thread) => Boolean(thread.spawned_by_node_id) && thread.thread)
      .map((thread) => thread.thread),
  );

  if (spawnedThreadIds.size === 0) {
    return false;
  }

  const messageCountsByThread = new Map<string, number>();
  for (const message of context.activeMessages) {
    const threadId = message.thread || context.activeChatId;
    if (!spawnedThreadIds.has(threadId)) continue;
    messageCountsByThread.set(threadId, (messageCountsByThread.get(threadId) || 0) + 1);
  }

  return Array.from(messageCountsByThread.values()).some(
    (count) => count >= minimumMessages,
  );
}

function hasSeenThreadTip(
  state: Record<ContextualTipId, ContextualTipStateRecord>,
): boolean {
  return state["spawned-thread-intro"].shownCount > 0 || isTipDismissed(state["spawned-thread-intro"]);
}

function hasResolvedThreadInteract(
  context: ContextualTipTriggerContext,
  state: Record<ContextualTipId, ContextualTipStateRecord>,
): boolean {
  return (
    context.threadInteractionEngaged ||
    context.threadForceYieldEngaged ||
    Boolean(state["spawned-thread-interact"].engagedAt)
  );
}

export const CONTEXTUAL_TIP_DEFINITIONS: ContextualTipDefinition[] = [
  {
    id: "spawned-thread-intro",
    title: "A new thread was created",
    body: "This is a spawned sub-conversation running alongside the main chat. Open it to inspect what happened or continue work there.",
    targetSelector: "[data-contextual-tip='spawned-thread-item']",
    priority: 100,
    cooldownMs: THREAD_TIP_COOLDOWN_MS,
    shouldShow: (context, state) => {
      const record = state["spawned-thread-intro"];
      if (!context.onboardingComplete || context.isWizardActive) return false;
      if (isTipDismissed(record)) return false;
      if (record.shownCount > 0) return false;
      if (context.threadInteractionEngaged) return false;
      if (!hasSpawnedThreadWithMessages(context, 2)) return false;
      if (isWithinCooldown(context.lastTipShownAt, CONTEXTUAL_TIPS_COOLDOWN_MS, context.now)) return false;
      return !isWithinCooldown(record.lastShownAt, THREAD_TIP_COOLDOWN_MS, context.now);
    },
  },
  {
    id: "spawned-thread-interact",
    title: "You can open or pause this thread",
    body: "Spawned threads are interactive. Click into one to chat directly, or use the hand button to force-yield it when you want control back.",
    targetSelector: "[data-contextual-tip='spawned-thread-force-yield']",
    priority: 90,
    cooldownMs: THREAD_TIP_COOLDOWN_MS,
    shouldShow: (context, state) => {
      const record = state["spawned-thread-interact"];
      if (!context.onboardingComplete || context.isWizardActive) return false;
      if (isTipDismissed(record)) return false;
      if (!hasSeenThreadTip(state)) return false;
      if (hasResolvedThreadInteract(context, state)) return false;
      if (!hasSpawnedThreadWithMessages(context, 2)) return false;
      if (isWithinCooldown(context.lastTipShownAt, CONTEXTUAL_TIPS_COOLDOWN_MS, context.now)) return false;
      return !isWithinCooldown(record.lastShownAt, THREAD_TIP_COOLDOWN_MS, context.now);
    },
  },
  {
    id: "thread-model",
    title: "Threads are independent agents within a chat",
    body: "Threads can be spawned dynamically by an agent, or created deterministically within a workflow. You can interact with each thread independently, send messages to guide that agent, or even branch a thread into a top-level chat.",
    targetSelector: "[data-contextual-tip='spawned-thread-item']",
    priority: 75,
    cooldownMs: THREAD_TIP_COOLDOWN_MS,
    shouldShow: (context, state) => {
      const record = state["thread-model"];
      if (!context.onboardingComplete || context.isWizardActive) return false;
      if (isTipDismissed(record)) return false;
      if (!hasSeenThreadTip(state)) return false;
      if (state["spawned-thread-interact"].shownCount === 0 && !isTipDismissed(state["spawned-thread-interact"])) {
        return false;
      }
      if (isWithinCooldown(context.lastTipShownAt, CONTEXTUAL_TIPS_COOLDOWN_MS, context.now)) return false;
      return !isWithinCooldown(record.lastShownAt, THREAD_TIP_COOLDOWN_MS, context.now);
    },
  },
  {
    id: "chat-branching",
    title: "Branch any message into a new conversation",
    body: "Click the branch icon on any message to create a new chat that picks up from that point. It's a great way to explore alternate approaches without losing your original conversation.",
    targetSelector: "[data-contextual-tip='branch-button']",
    priority: 70,
    cooldownMs: THREAD_TIP_COOLDOWN_MS,
    shouldShow: (context, state) => {
      const record = state["chat-branching"];
      if (!context.onboardingComplete || context.isWizardActive) return false;
      if (isTipDismissed(record)) return false;
      if (context.branchingEngaged) return false;
      if (!hasSeenThreadTip(state)) return false;
      if (context.activeMessages.length < 4) return false;
      if (isWithinCooldown(context.lastTipShownAt, CONTEXTUAL_TIPS_COOLDOWN_MS, context.now)) return false;
      return !isWithinCooldown(record.lastShownAt, THREAD_TIP_COOLDOWN_MS, context.now);
    },
  },
  {
    id: "worktree-after-nth-chat",
    title: "Try a workspace for isolated work",
    body: "You’ve started a few chats already. Workspaces let you branch off into isolated git contexts so feature work and experiments stay cleanly separated.",
    targetSelector: "[data-onboarding='workspace-buttons'], [data-onboarding='workspace-indicator']",
    priority: 80,
    cooldownMs: CONTEXTUAL_TIPS_COOLDOWN_MS,
    shouldShow: (context, state) => {
      const record = state["worktree-after-nth-chat"];
      if (!context.onboardingComplete || context.isWizardActive) return false;
      if (isTipDismissed(record)) return false;
      if (context.hasNonMainWorktree) return false;
      if (context.chats.length < NTH_CHAT_WORKTREE_THRESHOLD) return false;
      if (isWithinCooldown(context.lastTipShownAt, CONTEXTUAL_TIPS_COOLDOWN_MS, context.now)) return false;
      return !isWithinCooldown(record.lastShownAt, CONTEXTUAL_TIPS_COOLDOWN_MS, context.now);
    },
  },
].sort((left, right) => right.priority - left.priority);

export function getNextEligibleContextualTip(
  context: ContextualTipTriggerContext,
  state: Record<ContextualTipId, ContextualTipStateRecord>,
): ContextualTipDefinition | null {
  for (const tip of CONTEXTUAL_TIP_DEFINITIONS) {
    if (tip.shouldShow(context, state)) {
      return tip;
    }
  }
  return null;
}