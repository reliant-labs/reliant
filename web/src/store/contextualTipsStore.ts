import { create } from "zustand";
import { logger } from "../lib/logger";
import {
  CONTEXTUAL_TIPS_SETTINGS_KEYS,
  DEFAULT_CONTEXTUAL_TIP_STATE,
  type ContextualTipId,
  type ContextualTipStateRecord,
  getNextEligibleContextualTip,
} from "../components/Onboarding/contextualTipsRegistry";
import {
  safeGetSetting,
  upsertStringSetting,
} from "../lib/settingsPersistence";
import { useChatStore } from "./chatStore";
import { useThreadActivityStore } from "./threadActivityStore";
import { useWorktreeStore } from "./worktreeStore";
import { useTourStore } from "./tourStore";

function createDefaultContextualTipState(): Record<ContextualTipId, ContextualTipStateRecord> {
  return JSON.parse(JSON.stringify(DEFAULT_CONTEXTUAL_TIP_STATE)) as Record<
    ContextualTipId,
    ContextualTipStateRecord
  >;
}

interface ContextualTipsStoreState {
  isInitialized: boolean;
  isLoading: boolean;
  loadFailed: boolean;
  tipsDisabled: boolean;
  activeTipId: ContextualTipId | null;
  lastTipShownAt: string | null;
  tipState: Record<ContextualTipId, ContextualTipStateRecord>;
  featureEngagement: {
    threadInteraction: boolean;
    branching: boolean;
    params: boolean;
  };
  loadState: () => Promise<void>;
  reevaluate: () => Promise<void>;
  confirmTipShown: (tipId: ContextualTipId) => void;
  clearActiveTip: () => void;
  dismissTip: (tipId: ContextualTipId) => Promise<void>;
  disableAllTips: () => Promise<void>;
  markTipEngaged: (tipId: ContextualTipId) => Promise<void>;
  markFeatureEngaged: (feature: "threadInteraction" | "branching" | "params") => Promise<void>;
  subscribeToSources: () => () => void;
  reset: () => void;
}

async function persistState(
  tipState: Record<ContextualTipId, ContextualTipStateRecord>,
  tipsDisabled: boolean,
): Promise<void> {
  await Promise.all([
    upsertStringSetting(
      CONTEXTUAL_TIPS_SETTINGS_KEYS.STATE,
      JSON.stringify(tipState),
    ),
    upsertStringSetting(
      CONTEXTUAL_TIPS_SETTINGS_KEYS.DISABLED,
      tipsDisabled ? "true" : "false",
    ),
  ]);
}

export const useContextualTipsStore = create<ContextualTipsStoreState>((set, get) => ({
  isInitialized: false,
  isLoading: false,
  loadFailed: false,
  tipsDisabled: false,
  activeTipId: null,
  lastTipShownAt: null,
  tipState: createDefaultContextualTipState(),
  featureEngagement: {
    threadInteraction: false,
    branching: false,
    params: false,
  },

  loadState: async () => {
    if (get().isLoading || get().isInitialized) return;
    set({ isLoading: true });

    let remoteStateSetting: Awaited<ReturnType<typeof safeGetSetting>>;
    let remoteDisabledSetting: Awaited<ReturnType<typeof safeGetSetting>>;
    try {
      [remoteStateSetting, remoteDisabledSetting] = await Promise.all([
        safeGetSetting(CONTEXTUAL_TIPS_SETTINGS_KEYS.STATE),
        safeGetSetting(CONTEXTUAL_TIPS_SETTINGS_KEYS.DISABLED),
      ]);
    } catch (error) {
      logger.warn("[ContextualTipsStore] Failed to load tip state from backend", { error });
      set({ isInitialized: true, isLoading: false, loadFailed: true, activeTipId: null });
      return;
    }

    let tipState = createDefaultContextualTipState();
    if (remoteStateSetting?.value) {
      try {
        tipState = {
          ...createDefaultContextualTipState(),
          ...JSON.parse(remoteStateSetting.value),
        };
      } catch (error) {
        logger.warn("[ContextualTipsStore] Failed to parse remote tip state", { error });
      }
    }

    const tipsDisabled = remoteDisabledSetting?.value === "true";

    const lastTipShownAt = Object.values(tipState)
      .map((record) => record.lastShownAt)
      .filter((value): value is string => Boolean(value))
      .sort()
      .at(-1) ?? null;

    set({
      isInitialized: true,
      isLoading: false,
      loadFailed: false,
      tipsDisabled,
      tipState,
      lastTipShownAt,
    });
  },

  reevaluate: async () => {
    const onboardingState = useTourStore.getState();
    const state = get();
    if (!state.isInitialized || state.loadFailed || state.tipsDisabled) {
      if (state.activeTipId !== null) {
        set({ activeTipId: null });
      }
      return;
    }

    // Pessimistic: don't evaluate until dependent stores have completed
    // their initial load.  Before that, empty arrays (worktrees: [],
    // chats: Map()) are indistinguishable from "loaded but empty" and
    // tips would evaluate against incomplete data — e.g. deciding the
    // user has no worktrees when they simply haven't loaded yet.
    const chatState = useChatStore.getState();
    const worktreeState = useWorktreeStore.getState();
    if (!chatState.hasLoaded || !worktreeState.hasLoaded) {
      return;
    }

    const activeChatId = chatState.activeChatId;
    const activeMessages = activeChatId ? chatState.messages[activeChatId] || [] : [];
    const activeThreads = activeChatId
      ? useThreadActivityStore.getState().threads[activeChatId] || []
      : [];

    const nextTip = getNextEligibleContextualTip(
      {
        onboardingComplete: onboardingState.hasCompletedOnboarding,
        isWizardActive: onboardingState.isWizardActive,
        activeChatId,
        chats: Array.from(chatState.chats.values()),
        activeMessages,
        activeThreads,
        hasNonMainWorktree: worktreeState
          .worktrees.some((worktree) => !worktree.is_main && !worktree.deleted_at),
        threadInteractionEngaged: state.featureEngagement.threadInteraction || Boolean(state.tipState["spawned-thread-intro"].engagedAt),
        branchingEngaged: state.featureEngagement.branching || Boolean(state.tipState["chat-branching"].engagedAt),
        paramsEngaged: state.featureEngagement.params,
        now: Date.now(),
        lastTipShownAt: state.lastTipShownAt,
      },
      state.tipState,
    );

    if (!nextTip) {
      if (state.activeTipId !== null) {
        set({ activeTipId: null });
      }
      return;
    }

    if (state.activeTipId === nextTip.id) {
      return;
    }

    set({ activeTipId: nextTip.id });
  },

  confirmTipShown: (tipId) => {
    const state = get();
    if (state.activeTipId !== tipId) return;

    const record = state.tipState[tipId];
    if (!record) return;

    const shownAt = new Date().toISOString();
    const nextTipState = {
      ...state.tipState,
      [tipId]: {
        ...record,
        shownCount: record.shownCount + 1,
        lastShownAt: shownAt,
      },
    };

    set({
      tipState: nextTipState,
      lastTipShownAt: shownAt,
    });

    void persistState(nextTipState, state.tipsDisabled);
  },

  clearActiveTip: () => {
    if (get().activeTipId !== null) {
      set({ activeTipId: null });
    }
  },

  dismissTip: async (tipId) => {
    const state = get();
    const nextTipState = {
      ...state.tipState,
      [tipId]: {
        ...state.tipState[tipId],
        dismissedAt: new Date().toISOString(),
      },
    };

    set({
      tipState: nextTipState,
      activeTipId: state.activeTipId === tipId ? null : state.activeTipId,
    });

    await persistState(nextTipState, state.tipsDisabled);
    await get().reevaluate();
  },

  disableAllTips: async () => {
    const state = get();
    set({ tipsDisabled: true, activeTipId: null });
    await persistState(state.tipState, true);
  },

  markTipEngaged: async (tipId) => {
    const state = get();
    const nextTipState = {
      ...state.tipState,
      [tipId]: {
        ...state.tipState[tipId],
        engagedAt: new Date().toISOString(),
      },
    };

    set({
      tipState: nextTipState,
      activeTipId: state.activeTipId === tipId ? null : state.activeTipId,
    });

    await persistState(nextTipState, state.tipsDisabled);
    await get().reevaluate();
  },

  markFeatureEngaged: async (feature) => {
    set((state) => ({
      featureEngagement: {
        ...state.featureEngagement,
        [feature]: true,
      },
    }));
    await get().reevaluate();
  },

  subscribeToSources: () => {
    // Debounce reevaluate calls from store subscriptions to avoid
    // evaluating tips against transient intermediate states (e.g. during
    // chat navigation where activeChatId updates after the chats map).
    // Without this, tips can briefly flash and get their shownCount
    // incorrectly incremented before the state settles.
    let debounceTimer: ReturnType<typeof setTimeout> | null = null;
    const debouncedReevaluate = () => {
      if (debounceTimer) clearTimeout(debounceTimer);
      debounceTimer = setTimeout(() => {
        debounceTimer = null;
        void get().reevaluate();
      }, 150);
    };

    const unsubscribeChat = useChatStore.subscribe(debouncedReevaluate);
    const unsubscribeThreads = useThreadActivityStore.subscribe(debouncedReevaluate);
    const unsubscribeWorktrees = useWorktreeStore.subscribe(debouncedReevaluate);
    const unsubscribeOnboarding = useTourStore.subscribe(debouncedReevaluate);

    const handleThreadInteracted = () => {
      void get().markFeatureEngaged("threadInteraction");
      void get().markTipEngaged("spawned-thread-intro");
      void get().markTipEngaged("spawned-thread-interact");
    };
    const handleParamsOpened = () => {
      void get().markFeatureEngaged("params");
    };

    window.addEventListener("contextual-tip-thread-interacted", handleThreadInteracted);
    window.addEventListener("contextual-tip-params-opened", handleParamsOpened);

    return () => {
      if (debounceTimer) clearTimeout(debounceTimer);
      unsubscribeChat();
      unsubscribeThreads();
      unsubscribeWorktrees();
      unsubscribeOnboarding();
      window.removeEventListener("contextual-tip-thread-interacted", handleThreadInteracted);
      window.removeEventListener("contextual-tip-params-opened", handleParamsOpened);
    };
  },

  reset: () => {
    set({
      isInitialized: false,
      isLoading: false,
      loadFailed: false,
      tipsDisabled: false,
      activeTipId: null,
      lastTipShownAt: null,
      tipState: createDefaultContextualTipState(),
      featureEngagement: {
        threadInteraction: false,
        branching: false,
        params: false,
      },
    });
  },
}));