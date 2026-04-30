/**
 * Onboarding Checklist Store
 *
 * Achievement-based onboarding that tracks real user actions,
 * combined with the guided-tour wizard state.
 *
 * Replaces the passive spotlight tour with a unified store that manages
 * both the checklist (auto-detected milestones) and the step-by-step
 * tour wizard.
 */

import { create } from "zustand";
import { api } from "../api/client";
import { mcpGrpc } from "../api/mcp-grpc";
import { logger } from "../lib/logger";
import {
  safeGetSetting,
  upsertStringSetting,
  deleteSettingIfExists,
} from "../lib/settingsPersistence";
import {
  CHECKLIST_ITEMS,
  CHECKLIST_SETTINGS_KEYS,
  TOUR_SETTINGS_KEYS,
  ONBOARDING_STEPS,
  REQUIRED_ITEMS,
  getNextStepId,
  getPreviousStepId,
  stepRequiresWorkflowMode,
  stepRequiresWorkflowBuilder,
  stepRequiresChatMode,
  stepRequiresSettingsMode,
} from "../components/Onboarding/constants";
import type {
  ChecklistItemId,
  ChecklistPanelState,
  OnboardingStepId,
} from "../components/Onboarding/types";
import { useChatStore } from "./chatStore";
import { useWorktreeStore } from "./worktreeStore";
import { useProjectStore } from "./projectStore";
import { useGlobalDataStore } from "./globalDataStore";
import { useViewerStore } from "./viewerStore";
import { useChatParamsStore } from "./chatParamsStore";
import { useAttachmentStore } from "./attachmentStore";
import { useWorkspaceStateStore } from "./workspaceStateStore";

let suppressNextChatLaunch = false;

export function suppressNextOnboardingChatLaunch(): void {
  suppressNextChatLaunch = true;
}

// ─── Store Interface ──────────────────────────────────────────────────────────

interface OnboardingChecklistState {
  /** Items the user has completed */
  completedItems: Set<ChecklistItemId>;

  /** Whether the welcome modal has been shown */
  welcomeShown: boolean;

  /** Current state of the floating checklist panel */
  panelState: ChecklistPanelState;

  /** Whether the initial state has been loaded from DB */
  isInitialized: boolean;

  /** Loading guard */
  isLoading: boolean;

  // ─── Tour State ──────────────────────────────────────────────────────────
  /** Whether the guided tour is currently active */
  isWizardActive: boolean;
  /** Current tour step ID */
  currentStepId: OnboardingStepId | null;
  /** Set of completed tour step IDs */
  completedSteps: Set<OnboardingStepId>;
  /** Set of skipped tour step IDs */
  skippedSteps: Set<OnboardingStepId>;
  /** Whether the tour has been completed at least once */
  hasCompletedOnboarding: boolean;
  /** Whether the project has existing source code (for completion step) */
  projectHasCode: boolean | null;

  // ─── Derived ────────────────────────────────────────────────────────────

  /** Number of required items completed */
  requiredCompleted: () => number;

  /** Total number of required items */
  requiredTotal: () => number;

  /** Total items completed (required + bonus) */
  totalCompleted: () => number;

  /** Whether all required items are done */
  allRequiredComplete: () => boolean;

  /** Completion percentage (all items) */
  completionPercentage: () => number;

  // ─── Checklist Actions ─────────────────────────────────────────────────

  /** Load persisted state from DB */
  loadState: () => Promise<void>;

  /** Mark a single item as completed and persist */
  markComplete: (itemId: ChecklistItemId) => Promise<void>;

  /** Mark welcome modal as shown and persist */
  markWelcomeShown: () => Promise<void>;

  /** Update panel state (collapsed/expanded/dismissed) and persist */
  setPanelState: (state: ChecklistPanelState) => Promise<void>;

  /**
   * Detect which items are already completed by checking current app state.
   * Called on init and periodically to catch actions the user took outside
   * the checklist flow.
   */
  detectCompletedItems: () => Promise<void>;

  /**
   * Subscribe to Zustand store changes for real-time auto-detection.
   * Returns an unsubscribe function.
   */
  subscribeToStoreChanges: () => () => void;

  /** Reset store to initial state (for testing / restart) */
  reset: () => void;

  // ─── Tour Actions ────────────────────────────────────────────────────────
  startWizard: () => Promise<void>;
  resumeWizard: () => Promise<void>;
  closeWizard: () => void;
  goToStep: (stepId: OnboardingStepId) => void;
  completeStep: (stepId: OnboardingStepId) => Promise<void>;
  skipStep: (stepId: OnboardingStepId) => Promise<void>;
  skipAll: () => Promise<void>;
  nextStep: () => void;
  previousStep: () => void;
  restartWizard: () => Promise<void>;
  saveTourState: () => Promise<void>;
  detectProjectCode: () => Promise<void>;
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

/** Serialize a Set<ChecklistItemId> to JSON string */
function serializeItems(items: Set<ChecklistItemId>): string {
  return JSON.stringify(Array.from(items));
}

/** Deserialize a JSON string to Set<ChecklistItemId> */
function deserializeItems(json: string): Set<ChecklistItemId> {
  try {
    const parsed = JSON.parse(json) as ChecklistItemId[];
    return new Set(parsed);
  } catch {
    return new Set();
  }
}

/** After tour ends, show API key modal if none configured (same path as setup guide). */
async function promptApiKeyIfNeededAfterOnboarding(): Promise<void> {
  const { useApiKeySetupStore } = await import("./apiKeySetupStore");
  // Reset hasChecked so the check actually re-runs (it may have been set during app startup)
  useApiKeySetupStore.setState({ hasChecked: false });
  await useApiKeySetupStore.getState().ensureApiKeyOrShowModal();
}

function getLaunchPrompt(projectHasCode: boolean | null): string {
  const projectId = useProjectStore.getState().currentProject?.id;
  if (projectId) {
    const draft = useWorkspaceStateStore.getState().getNewChatDraft(projectId).trim();
    if (draft) return draft;
  }

  return projectHasCode !== false
    ? "Search for refactoring opportunities in this codebase"
    : "Write me a Python hello world HTTP server";
}

function selectedPresetsFromTemp(value: unknown): Record<string, string> | undefined {
  if (!value || typeof value !== "object") return undefined;
  const selectedPresets: Record<string, string> = {};
  for (const [target, preset] of Object.entries(value as Record<string, string | null>)) {
    if (preset) selectedPresets[target] = preset;
  }
  return Object.keys(selectedPresets).length > 0 ? selectedPresets : undefined;
}

async function launchConfiguredOnboardingChat(projectHasCode: boolean | null): Promise<boolean> {
  const worktreeStore = useWorktreeStore.getState();
  const worktreeId =
    worktreeStore.currentWorktree?.id ??
    worktreeStore.worktrees.find((worktree) => worktree.is_main && !worktree.deleted_at)?.id;
  if (!worktreeId) return false;

  const tempParams = useChatParamsStore.getState().tempNewChatParams;
  if (Object.keys(tempParams).length === 0) return false;

  const {
    __selectedWorkflow,
    __selectedPresets,
    ...workflowParams
  } = tempParams;
  const workflow = typeof __selectedWorkflow === "string" ? __selectedWorkflow : undefined;
  const selectedPresets = selectedPresetsFromTemp(__selectedPresets);
  const prompt = getLaunchPrompt(projectHasCode);

  const chat = await useChatStore
    .getState()
    .createChat(
      worktreeId,
      prompt,
      undefined,
      Object.keys(workflowParams).length > 0 ? workflowParams : undefined,
      workflow,
      selectedPresets,
    );

  useChatParamsStore.getState().transferTempToChat(chat.id);
  useChatStore.getState().selectChat(chat);
  useAttachmentStore.getState().clearAttachments("temp");
  return true;
}

async function finishTourAndLaunchChat(projectHasCode: boolean | null): Promise<void> {
  if (suppressNextChatLaunch) {
    suppressNextChatLaunch = false;
    return;
  }

  try {
    const launched = await launchConfiguredOnboardingChat(projectHasCode);
    if (!launched) {
      useChatStore.getState().clearCurrentChat();
    }
  } catch (error) {
    logger.error("[ChecklistStore] Failed to launch onboarding chat", error);
    useChatStore.getState().clearCurrentChat();
  }
}
/** Detect if the project has source code files. */
async function detectHasCode(): Promise<boolean> {
  try {
    const { getFileTree } = await import("../api/fileSystem");
    const files = await getFileTree("/", false);
    const ignoreDirs = new Set([".git", ".reliant", "node_modules", ".vscode", ".idea"]);
    const codeFiles = files.filter((f) => !ignoreDirs.has(f.name));
    return codeFiles.length > 1;
  } catch {
    return true;
  }
}

// ─── Store ────────────────────────────────────────────────────────────────────

export const useOnboardingChecklistStore = create<OnboardingChecklistState>(
  (set, get) => ({
    completedItems: new Set(),
    welcomeShown: false,
    panelState: "expanded",
    isInitialized: false,
    isLoading: false,

    // ─── Tour State ────────────────────────────────────────────────────────
    isWizardActive: false,
    currentStepId: null,
    completedSteps: new Set(),
    skippedSteps: new Set(),
    hasCompletedOnboarding: false,
    projectHasCode: null,

    // ─── Derived ──────────────────────────────────────────────────────────

    requiredCompleted: () => {
      const { completedItems } = get();
      return REQUIRED_ITEMS.filter((item) => completedItems.has(item.id)).length;
    },

    requiredTotal: () => REQUIRED_ITEMS.length,

    totalCompleted: () => get().completedItems.size,

    allRequiredComplete: () => {
      const { completedItems } = get();
      return REQUIRED_ITEMS.every((item) => completedItems.has(item.id));
    },

    completionPercentage: () => {
      const total = CHECKLIST_ITEMS.length;
      if (total === 0) return 100;
      return Math.round((get().completedItems.size / total) * 100);
    },

    // ─── Checklist Actions ────────────────────────────────────────────────

    loadState: async () => {
      if (get().isLoading) return;
      set({ isLoading: true });

      // Load all settings independently — each call is wrapped so one
      // missing/failing key never prevents the others from loading.
      // This is critical: if e.g. COMPLETED_ITEMS doesn't exist yet
      // but onboarding.completed does, we must still read the latter.
      const [
        completedSetting,
        panelSetting,
        tourCompletedSetting,
        currentStepSetting,
        completedStepsSetting,
        skippedStepsSetting,
        welcomeSetting,
      ] = await Promise.all([
        safeGetSetting(CHECKLIST_SETTINGS_KEYS.COMPLETED_ITEMS),
        safeGetSetting(CHECKLIST_SETTINGS_KEYS.PANEL_STATE),
        safeGetSetting(TOUR_SETTINGS_KEYS.COMPLETED),
        safeGetSetting(TOUR_SETTINGS_KEYS.CURRENT_STEP),
        safeGetSetting(TOUR_SETTINGS_KEYS.COMPLETED_STEPS),
        safeGetSetting(TOUR_SETTINGS_KEYS.SKIPPED_STEPS),
        safeGetSetting(CHECKLIST_SETTINGS_KEYS.WELCOME_SHOWN),
      ]);

      const completedItems = completedSetting?.value
        ? deserializeItems(completedSetting.value)
        : new Set<ChecklistItemId>();

      const panelState =
        (panelSetting?.value as ChecklistPanelState) || "expanded";

      const hasCompletedOnboarding = tourCompletedSetting?.value === "true";

      const currentStep = currentStepSetting?.value as OnboardingStepId | undefined;

      let completedSteps = new Set<OnboardingStepId>();
      if (completedStepsSetting?.value) {
        try { completedSteps = new Set(JSON.parse(completedStepsSetting.value)); } catch { /* ignore */ }
      }

      let skippedSteps = new Set<OnboardingStepId>();
      if (skippedStepsSetting?.value) {
        try { skippedSteps = new Set(JSON.parse(skippedStepsSetting.value)); } catch { /* ignore */ }
      }

      // Welcome shown: check DB first, fallback to localStorage,
      // and also consider tour completion as implicit welcome shown
      const welcomeShown =
        welcomeSetting?.value === "true" ||
        hasCompletedOnboarding ||
        localStorage.getItem("reliant.checklist.welcomeShown") === "true";

      set({
        completedItems,
        panelState,
        welcomeShown,
        hasCompletedOnboarding,
        currentStepId: currentStep || null,
        completedSteps,
        skippedSteps,
        isInitialized: true,
        isLoading: false,
      });
    },

    markComplete: async (itemId: ChecklistItemId) => {
      const { completedItems } = get();
      if (completedItems.has(itemId)) return;

      const newItems = new Set(completedItems);
      newItems.add(itemId);
      set({ completedItems: newItems });

      await upsertStringSetting(
        CHECKLIST_SETTINGS_KEYS.COMPLETED_ITEMS,
        serializeItems(newItems),
      );

      logger.info("[ChecklistStore] Marked complete", { itemId });
    },

    markWelcomeShown: async () => {
      set({ welcomeShown: true });
      // Persist to localStorage immediately as fallback (sync, never fails)
      localStorage.setItem("reliant.checklist.welcomeShown", "true");
      // Also persist to DB (async, may fail on first load)
      try {
        await upsertStringSetting(CHECKLIST_SETTINGS_KEYS.WELCOME_SHOWN, "true");
      } catch (e) {
        logger.warn("[ChecklistStore] Failed to persist welcomeShown to DB", e);
      }
    },

    setPanelState: async (panelState: ChecklistPanelState) => {
      set({ panelState });
      await upsertStringSetting(CHECKLIST_SETTINGS_KEYS.PANEL_STATE, panelState);
    },

    detectCompletedItems: async () => {
      const { completedItems } = get();
      const newItems = new Set(completedItems);
      let changed = false;

      const projectId = useProjectStore.getState().currentProject?.id;

      // 1. API key — check if any provider is configured (hasApiKey or configured for OAuth)
      if (!newItems.has("add-api-key")) {
        try {
          const providers = await api.settings.getProviders();
          if (providers.some((p) => p.hasApiKey || p.configured)) {
            newItems.add("add-api-key");
            changed = true;
          }
        } catch {
          // Fail open — don't mark as complete if we can't check
        }
      }

      // 2. Start a chat — check if any chats exist
      if (!newItems.has("start-chat")) {
        const chats = useChatStore.getState().chats;
        if (chats.size > 0) {
          newItems.add("start-chat");
          changed = true;
        }
      }

      // 3. Use a custom workflow — check if any chat used a non-default workflow
      if (!newItems.has("use-custom-workflow")) {
        const chats = useChatStore.getState().chats;
        const isDefaultWorkflow = (name?: string) =>
          !name || name === "" || name === "agent" || name === "builtin://agent";
        const usedCustomWorkflow = Array.from(chats.values()).some(
          (chat) => !isDefaultWorkflow(chat.workflowName),
        );
        if (usedCustomWorkflow) {
          newItems.add("use-custom-workflow");
          changed = true;
        }
      }

      // 4. Create a workflow — check if any user-created workflows exist
      if (!newItems.has("create-workflow") && projectId) {
        try {
          const workflows = useGlobalDataStore.getState().workflows;
          if (workflows.some((w) => w.source === "user")) {
            newItems.add("create-workflow");
            changed = true;
          }
        } catch {
          // Fail open
        }
      }

      // 5. Take product tour — check if the tour has been completed
      if (!newItems.has("take-product-tour") && get().hasCompletedOnboarding) {
        newItems.add("take-product-tour");
        changed = true;
      }

      // 6. Create a workspace — check if any non-main worktrees exist
      if (!newItems.has("create-workspace")) {
        const worktrees = useWorktreeStore.getState().worktrees;
        const hasUserWorktree = worktrees.some(
          (w) => !w.is_main && !w.deleted_at,
        );
        if (hasUserWorktree) {
          newItems.add("create-workspace");
          changed = true;
        }
      }

      // 7. Install an MCP server
      if (!newItems.has("install-mcp") && projectId) {
        try {
          const result = await mcpGrpc.listServers(projectId);
          if (result.servers.length > 0) {
            newItems.add("install-mcp");
            changed = true;
          }
        } catch {
          // Fail open
        }
      }

      // 8. Create a preset — check if any user presets exist
      if (!newItems.has("create-preset") && projectId) {
        try {
          const presets = useGlobalDataStore.getState().presets;
          if (presets.some((p) => p.source === "user")) {
            newItems.add("create-preset");
            changed = true;
          }
        } catch {
          // Fail open
        }
      }

      // 9. Read docs — this is manually marked only (external link click)
      // No auto-detection needed.

      if (changed) {
        set({ completedItems: newItems });
        await upsertStringSetting(
          CHECKLIST_SETTINGS_KEYS.COMPLETED_ITEMS,
          serializeItems(newItems),
        );
        logger.info("[ChecklistStore] Auto-detected completions", {
          items: Array.from(newItems),
        });
      }
    },

    subscribeToStoreChanges: () => {
      const unsubscribers: (() => void)[] = [];

      // Watch chat store for new chats and workflow runs
      let prevChatCount = useChatStore.getState().chats.size;
      unsubscribers.push(
        useChatStore.subscribe((state) => {
          // Detect new chats
          if (state.chats.size > prevChatCount) {
            get().markComplete("start-chat");
          }
          prevChatCount = state.chats.size;

          // Detect custom workflow usage
          if (!get().completedItems.has("use-custom-workflow")) {
            const isDefault = (name?: string) =>
              !name || name === "" || name === "agent" || name === "builtin://agent";
            const usedCustom = Array.from(state.chats.values()).some(
              (chat) => !isDefault(chat.workflowName),
            );
            if (usedCustom) {
              get().markComplete("use-custom-workflow");
            }
          }
        }),
      );

      // Watch worktrees for new workspaces
      unsubscribers.push(
        useWorktreeStore.subscribe((state) => {
          if (get().completedItems.has("create-workspace")) return;
          const hasUserWorktree = state.worktrees.some(
            (w) => !w.is_main && !w.deleted_at,
          );
          if (hasUserWorktree) {
            get().markComplete("create-workspace");
          }
        }),
      );

      // Watch globalDataStore for user workflows and presets
      unsubscribers.push(
        useGlobalDataStore.subscribe((state) => {
          if (
            !get().completedItems.has("create-workflow") &&
            state.workflows.some((w) => w.source === "user")
          ) {
            get().markComplete("create-workflow");
          }
          if (
            !get().completedItems.has("create-preset") &&
            state.presets.some((p) => p.source === "user")
          ) {
            get().markComplete("create-preset");
          }
        }),
      );

      // Watch for API key saves (dispatched by ApiKeySetupModal and CombinedGeneralSettings)
      const handleApiKeySaved = () => {
        get().markComplete("add-api-key");
      };
      window.addEventListener("api-key-saved", handleApiKeySaved);
      unsubscribers.push(() => {
        window.removeEventListener("api-key-saved", handleApiKeySaved);
      });

      return () => {
        unsubscribers.forEach((unsub) => unsub());
      };
    },

    // ─── Tour Actions ─────────────────────────────────────────────────────

    startWizard: async () => {
      set({
        isWizardActive: true,
        currentStepId: ONBOARDING_STEPS[0].id,
        completedSteps: new Set(),
        skippedSteps: new Set(),
        hasCompletedOnboarding: false,
      });
      await get().saveTourState();
      await get().detectProjectCode();
    },

    resumeWizard: async () => {
      const state = get();
      if (state.currentStepId) {
        set({ isWizardActive: true });
      } else {
        await get().startWizard();
      }
    },

    closeWizard: () => {
      set({ isWizardActive: false });
      suppressNextChatLaunch = false;
      get().saveTourState();
    },

    goToStep: (stepId: OnboardingStepId) => {
      set({ currentStepId: stepId });
    },

    completeStep: async (stepId: OnboardingStepId) => {
      const newCompleted = new Set(get().completedSteps);
      newCompleted.add(stepId);
      set({ completedSteps: newCompleted });
      await get().saveTourState();
    },

    skipStep: async (stepId: OnboardingStepId) => {
      const newSkipped = new Set(get().skippedSteps);
      newSkipped.add(stepId);
      set({ skippedSteps: newSkipped });
      await get().saveTourState();
    },

    skipAll: async () => {
      // Navigate back to chat area if in workflow mode
      const viewerStore = useViewerStore.getState();
      if (viewerStore.isWorkflowMode) {
        viewerStore.setWorkflowMode(false);
      }

      set({
        isWizardActive: false,
        currentStepId: null,
        welcomeShown: true,
      });

      // Mark welcomeShown in localStorage as well
      localStorage.setItem("reliant.checklist.welcomeShown", "true");

      // Mark skipped_all in database
      await upsertStringSetting(TOUR_SETTINGS_KEYS.SKIPPED_ALL, "true");
      await get().saveTourState();

      void finishTourAndLaunchChat(get().projectHasCode);
      void promptApiKeyIfNeededAfterOnboarding();
    },

    nextStep: () => {
      const state = get();
      if (!state.currentStepId) return;

      const nextId = getNextStepId(state.currentStepId);
      if (nextId) {
        set({ currentStepId: nextId });
        get().saveTourState();
      } else {
        // No more steps — complete the wizard
        const viewerStore = useViewerStore.getState();
        viewerStore.setSettingsMode(false);
        viewerStore.setWorkflowMode(false);

        const completedItems = new Set(get().completedItems);
        completedItems.add("take-product-tour");

        set({
          isWizardActive: false,
          hasCompletedOnboarding: true,
          currentStepId: null,
          welcomeShown: true,
          completedItems,
        });

        // Persist welcomeShown via localStorage too
        localStorage.setItem("reliant.checklist.welcomeShown", "true");

        void upsertStringSetting(
          CHECKLIST_SETTINGS_KEYS.COMPLETED_ITEMS,
          serializeItems(completedItems),
        );
        get().saveTourState();

        void finishTourAndLaunchChat(state.projectHasCode);
        void promptApiKeyIfNeededAfterOnboarding();
      }
    },

    previousStep: () => {
      const state = get();
      if (!state.currentStepId) return;

      const prevId = getPreviousStepId(state.currentStepId);
      if (prevId) {
        const viewerStore = useViewerStore.getState();

        // Set the correct view mode BEFORE changing step so targets are visible
        if (stepRequiresWorkflowMode(prevId)) {
          viewerStore.setSettingsMode(false);
          const workflowName = stepRequiresWorkflowBuilder(prevId) ? "builtin://agent" : undefined;
          viewerStore.setWorkflowMode(true, workflowName);
        } else if (stepRequiresSettingsMode(prevId)) {
          viewerStore.setWorkflowMode(false);
          viewerStore.setSettingsMode(true);
        } else if (stepRequiresChatMode(prevId)) {
          viewerStore.setSettingsMode(false);
          viewerStore.setWorkflowMode(false);
        } else {
          // Modal step or unknown — close both modes
          viewerStore.setSettingsMode(false);
          viewerStore.setWorkflowMode(false);
        }

        set({ currentStepId: prevId });
        get().saveTourState();
      }
    },

    restartWizard: async () => {
      // Navigate to chat area (close settings and workflow modes)
      const viewerStore = useViewerStore.getState();
      viewerStore.setSettingsMode(false);
      viewerStore.setWorkflowMode(false);

      // Clear current chat to show the new-chat page
      useChatStore.getState().clearCurrentChat();

      // Clear the skipped_all flag
      try {
        await deleteSettingIfExists(TOUR_SETTINGS_KEYS.SKIPPED_ALL);
      } catch {
        // Ignore if doesn't exist
      }

      // Reset all tour state
      set({
        isWizardActive: true,
        currentStepId: ONBOARDING_STEPS[0].id,
        completedSteps: new Set(),
        skippedSteps: new Set(),
        hasCompletedOnboarding: false,
        projectHasCode: null,
        // Reset checklist state as well
        completedItems: new Set(),
        welcomeShown: false,
        panelState: "expanded",
      });

      // Clear localStorage welcomeShown
      localStorage.removeItem("reliant.checklist.welcomeShown");
      suppressNextChatLaunch = false;

      await get().saveTourState();
      await get().detectProjectCode();
    },

    saveTourState: async () => {
      const state = get();
      try {
        await upsertStringSetting(
          TOUR_SETTINGS_KEYS.COMPLETED,
          state.hasCompletedOnboarding ? "true" : "false",
        );
        if (state.currentStepId) {
          await upsertStringSetting(TOUR_SETTINGS_KEYS.CURRENT_STEP, state.currentStepId);
        }
        await upsertStringSetting(
          TOUR_SETTINGS_KEYS.COMPLETED_STEPS,
          JSON.stringify(Array.from(state.completedSteps)),
        );
        await upsertStringSetting(
          TOUR_SETTINGS_KEYS.SKIPPED_STEPS,
          JSON.stringify(Array.from(state.skippedSteps)),
        );
      } catch (error) {
        logger.error("[ChecklistStore] Failed to save tour state", error);
      }
    },

    detectProjectCode: async () => {
      const hasCode = await detectHasCode();
      set({ projectHasCode: hasCode });
    },

    reset: () => {
      set({
        completedItems: new Set(),
        welcomeShown: false,
        panelState: "expanded",
        isInitialized: false,
        isLoading: false,
        isWizardActive: false,
        currentStepId: null,
        completedSteps: new Set(),
        skippedSteps: new Set(),
        hasCompletedOnboarding: false,
        projectHasCode: null,
      });
      suppressNextChatLaunch = false;
    },
  }),
);