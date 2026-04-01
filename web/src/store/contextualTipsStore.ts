import { create } from "zustand";
import { logger } from "../lib/logger";
import {
  CONTEXTUAL_TIPS_LOCAL_STORAGE_KEYS,
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
import { useOnboardingChecklistStore } from "./onboardingChecklistStore";

function createDefaultContextualTipState(): Record<ContextualTipId, ContextualTipStateRecord> {
  return JSON.parse(JSON.stringify(DEFAULT_CONTEXTUAL_TIP_STATE)) as Record<
    ContextualTipId,
    ContextualTipStateRecord
  >;
}

interface ContextualTipsStoreState {
  isInitialized: boolean;
  isLoading: boolean;
  tipsDisabled: boolean;
  activeTipId: ContextualTipId | null;
  lastTipShownAt: string | null;
  tipState: Record<ContextualTipId, ContextualTipStateRecord>;
  featureEngagement: {
    threadInteraction: boolean;
    threadForceYield: boolean;
    branching: boolean;
    params: boolean;
  };
  loadState: () => Promise<void>;
  reevaluate: () => Promise<void>;
  dismissTip: (tipId: ContextualTipId) => Promise<void>;
  disableAllTips: () => Promise<void>;
  markTipEngaged: (tipId: ContextualTipId) => Promise<void>;
  markFeatureEngaged: (feature: "threadInteraction" | "threadForceYield" | "branching" | "params") => Promise<void>;
  subscribeToSources: () => () => void;
  reset: () => void;
}

function getStoredLocalState(): Record<ContextualTipId, ContextualTipStateRecord> {
  try {
    const raw = localStorage.getItem(CONTEXTUAL_TIPS_LOCAL_STORAGE_KEYS.STATE);
    if (!raw) return createDefaultContextualTipState();
    return {
      ...createDefaultContextualTipState(),
      ...JSON.parse(raw),
    };
  } catch {
    return createDefaultContextualTipState();
  }
}

function persistLocalState(
  tipState: Record<ContextualTipId, ContextualTipStateRecord>,
  tipsDisabled: boolean,
): void {
  localStorage.setItem(
    CONTEXTUAL_TIPS_LOCAL_STORAGE_KEYS.STATE,
    JSON.stringify(tipState),
  );
  localStorage.setItem(
    CONTEXTUAL_TIPS_LOCAL_STORAGE_KEYS.DISABLED,
    tipsDisabled ? "true" : "false",
  );
}

async function persistRemoteState(
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
  tipsDisabled: false,
  activeTipId: null,
  lastTipShownAt: null,
  tipState: createDefaultContextualTipState(),
  featureEngagement: {
    threadInteraction: false,
    threadForceYield: false,
    branching: false,
    params: false,
  },

  loadState: async () => {
    if (get().isLoading || get().isInitialized) return;
    set({ isLoading: true });

    const [remoteStateSetting, remoteDisabledSetting] = await Promise.all([
      safeGetSetting(CONTEXTUAL_TIPS_SETTINGS_KEYS.STATE),
      safeGetSetting(CONTEXTUAL_TIPS_SETTINGS_KEYS.DISABLED),
    ]);

    let tipState = getStoredLocalState();
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

    const tipsDisabled =
      remoteDisabledSetting?.value === "true" ||
      localStorage.getItem(CONTEXTUAL_TIPS_LOCAL_STORAGE_KEYS.DISABLED) === "true";

    const lastTipShownAt = Object.values(tipState)
      .map((record) => record.lastShownAt)
      .filter((value): value is string => Boolean(value))
      .sort()
      .at(-1) ?? null;

    persistLocalState(tipState, tipsDisabled);

    set({
      isInitialized: true,
      isLoading: false,
      tipsDisabled,
      tipState,
      lastTipShownAt,
    });
  },

  reevaluate: async () => {
    const onboardingState = useOnboardingChecklistStore.getState();
    const state = get();
    if (!state.isInitialized || state.tipsDisabled) {
      if (state.activeTipId !== null) {
        set({ activeTipId: null });
      }
      return;
    }

    const chatState = useChatStore.getState();
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
        hasNonMainWorktree: useWorktreeStore
          .getState()
          .worktrees.some((worktree) => !worktree.is_main && !worktree.deleted_at),
        threadInteractionEngaged: state.featureEngagement.threadInteraction || Boolean(state.tipState["spawned-thread-intro"].engagedAt),
        threadForceYieldEngaged: state.featureEngagement.threadForceYield,
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

    const shownAt = new Date().toISOString();
    const nextTipState = {
      ...state.tipState,
      [nextTip.id]: {
        ...state.tipState[nextTip.id],
        shownCount: state.tipState[nextTip.id].shownCount + 1,
        lastShownAt: shownAt,
      },
    };

    set({
      activeTipId: nextTip.id,
      tipState: nextTipState,
      lastTipShownAt: shownAt,
    });

    persistLocalState(nextTipState, state.tipsDisabled);
    void persistRemoteState(nextTipState, state.tipsDisabled);
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

    persistLocalState(nextTipState, state.tipsDisabled);
    await persistRemoteState(nextTipState, state.tipsDisabled);
    await get().reevaluate();
  },

  disableAllTips: async () => {
    const state = get();
    set({ tipsDisabled: true, activeTipId: null });
    persistLocalState(state.tipState, true);
    await persistRemoteState(state.tipState, true);
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

    persistLocalState(nextTipState, state.tipsDisabled);
    await persistRemoteState(nextTipState, state.tipsDisabled);
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
    const unsubscribeChat = useChatStore.subscribe(() => {
      void get().reevaluate();
    });
    const unsubscribeThreads = useThreadActivityStore.subscribe(() => {
      void get().reevaluate();
    });
    const unsubscribeWorktrees = useWorktreeStore.subscribe(() => {
      void get().reevaluate();
    });
    const unsubscribeOnboarding = useOnboardingChecklistStore.subscribe(() => {
      void get().reevaluate();
    });

    const handleThreadInteracted = () => {
      void get().markFeatureEngaged("threadInteraction");
      void get().markTipEngaged("spawned-thread-intro");
      void get().markTipEngaged("spawned-thread-interact");
    };
    const handleThreadForceYield = () => {
      void get().markFeatureEngaged("threadForceYield");
      void get().markTipEngaged("spawned-thread-interact");
    };
    const handleParamsOpened = () => {
      void get().markFeatureEngaged("params");
    };

    window.addEventListener("contextual-tip-thread-interacted", handleThreadInteracted);
    window.addEventListener("contextual-tip-thread-force-yield", handleThreadForceYield);
    window.addEventListener("contextual-tip-params-opened", handleParamsOpened);

    return () => {
      unsubscribeChat();
      unsubscribeThreads();
      unsubscribeWorktrees();
      unsubscribeOnboarding();
      window.removeEventListener("contextual-tip-thread-interacted", handleThreadInteracted);
      window.removeEventListener("contextual-tip-thread-force-yield", handleThreadForceYield);
      window.removeEventListener("contextual-tip-params-opened", handleParamsOpened);
    };
  },

  reset: () => {
    localStorage.removeItem(CONTEXTUAL_TIPS_LOCAL_STORAGE_KEYS.STATE);
    localStorage.removeItem(CONTEXTUAL_TIPS_LOCAL_STORAGE_KEYS.DISABLED);
    set({
      isInitialized: false,
      isLoading: false,
      tipsDisabled: false,
      activeTipId: null,
      lastTipShownAt: null,
      tipState: createDefaultContextualTipState(),
      featureEngagement: {
        threadInteraction: false,
        threadForceYield: false,
        branching: false,
        params: false,
      },
    });
  },
}));