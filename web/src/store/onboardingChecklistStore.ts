/**
 * Onboarding Checklist Store
 *
 * Achievement-based onboarding that tracks real user actions.
 * The guided-tour wizard lives in tourStore.ts; the two stores coordinate
 * for the "take-product-tour" achievement.
 */

import { create } from "zustand";
import { api } from "../api/client";
import { mcpGrpc } from "../api/mcp-grpc";
import { logger } from "../lib/logger";
import {
  readSetting,
  upsertStringSetting,
} from "../lib/settingsPersistence";
import {
  CHECKLIST_ITEMS,
  CHECKLIST_SETTINGS_KEYS,
  REQUIRED_ITEMS,
} from "../components/Onboarding/constants";
import type {
  ChecklistItemId,
  ChecklistPanelState,
} from "../components/Onboarding/types";
import { useWorktreeStore } from "./worktreeStore";
import { useProjectStore } from "./projectStore";
import { useGlobalDataStore } from "./globalDataStore";
import { getEventBus } from "../lib/events";
import { chatKeys, getCachedChatList } from "../hooks/chat-queries";
import { queryClient } from "../lib/query-client";

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
   * Bring a dismissed guide back — the one sanctioned way to undo a
   * dismissal. Used by "Restart Onboarding Guide" and the post-onboarding
   * hand-off. Unlike a raw setState this persists, so the revival survives a
   * reload instead of silently reverting to the stored `dismissed`.
   */
  revive: () => Promise<void>;

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
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function serializeItems(items: Set<ChecklistItemId>): string {
  return JSON.stringify(Array.from(items));
}

function deserializeItems(json: string): Set<ChecklistItemId> {
  try {
    const parsed = JSON.parse(json) as ChecklistItemId[];
    return new Set(parsed);
  } catch {
    return new Set();
  }
}

// ─── Local mirror ─────────────────────────────────────────────────────────────
// The settings table is the system of record, but it is reachable only over an
// RPC that can be slow, unauthenticated, or down at exactly the moment we have
// to decide whether to show the guide. Every write is mirrored to localStorage
// synchronously so a reload can answer "did they already dismiss this?" without
// the network. On conflict the two are merged rather than overwritten — see
// resolvePanelState.

const LOCAL_KEYS = {
  PANEL_STATE: "reliant.checklist.panelState",
  COMPLETED_ITEMS: "reliant.checklist.completedItems",
  WELCOME_SHOWN: "reliant.checklist.welcomeShown",
} as const;

function readLocal(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeLocal(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // Private-mode / quota. The backend write is still in flight.
  }
}

/**
 * Dismissal is monotonic and sticky: if either side says the user dismissed
 * the guide, it stays dismissed. Anything else is a display preference, where
 * a successful remote read wins (it may carry another device's choice) and a
 * failed one falls back to whatever this device last saw.
 *
 * Reviving a dismissed guide is possible only through revive(), which clears
 * both records at once.
 */
function resolvePanelState(
  remote: ChecklistPanelState | null,
  local: ChecklistPanelState | null,
): ChecklistPanelState {
  if (remote === "dismissed" || local === "dismissed") return "dismissed";
  return remote ?? local ?? "collapsed";
}

function isPanelState(value: string | null): value is ChecklistPanelState {
  return value === "collapsed" || value === "expanded" || value === "dismissed";
}

// ─── Store ────────────────────────────────────────────────────────────────────

export const useOnboardingChecklistStore = create<OnboardingChecklistState>(
  (set, get) => ({
    completedItems: new Set(),
    welcomeShown: false,
    panelState: "collapsed",
    isInitialized: false,
    isLoading: false,

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

      const [completedRead, panelRead, welcomeRead] = await Promise.all([
        readSetting(CHECKLIST_SETTINGS_KEYS.COMPLETED_ITEMS),
        readSetting(CHECKLIST_SETTINGS_KEYS.PANEL_STATE),
        readSetting(CHECKLIST_SETTINGS_KEYS.WELCOME_SHOWN),
      ]);

      const remotePanel =
        panelRead.status === "found" && isPanelState(panelRead.value)
          ? panelRead.value
          : null;
      const localPanelRaw = readLocal(LOCAL_KEYS.PANEL_STATE);
      const localPanel = isPanelState(localPanelRaw) ? localPanelRaw : null;
      const panelState = resolvePanelState(remotePanel, localPanel);

      // Union the two records. Completions are monotonic — an item once earned
      // is never un-earned — so merging is always safe, and it stops a failed
      // read from resetting the user's progress to zero.
      const completedItems = new Set<ChecklistItemId>([
        ...(completedRead.status === "found"
          ? deserializeItems(completedRead.value)
          : []),
        ...deserializeItems(readLocal(LOCAL_KEYS.COMPLETED_ITEMS) ?? "[]"),
      ]);

      const welcomeShown =
        (welcomeRead.status === "found" && welcomeRead.value === "true") ||
        readLocal(LOCAL_KEYS.WELCOME_SHOWN) === "true";

      // Re-mirror the resolved answer so the next boot is decidable offline and
      // so a dismissal made on another device is honored on this one.
      writeLocal(LOCAL_KEYS.PANEL_STATE, panelState);
      if (completedItems.size > 0) {
        writeLocal(LOCAL_KEYS.COMPLETED_ITEMS, serializeItems(completedItems));
      }

      // Heal the backend when this device knows something it doesn't. Without
      // this a dismissal recorded during an outage would never reach the
      // server, and every other device would keep showing the guide.
      if (remotePanel !== panelState) {
        void upsertStringSetting(
          CHECKLIST_SETTINGS_KEYS.PANEL_STATE,
          panelState,
        ).catch(() => {
          /* best-effort; the local mirror already holds the truth */
        });
      }

      set({
        completedItems,
        panelState,
        welcomeShown,
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

      const serialized = serializeItems(newItems);
      writeLocal(LOCAL_KEYS.COMPLETED_ITEMS, serialized);
      await upsertStringSetting(
        CHECKLIST_SETTINGS_KEYS.COMPLETED_ITEMS,
        serialized,
      );

      logger.info("[ChecklistStore] Marked complete", { itemId });
    },

    markWelcomeShown: async () => {
      set({ welcomeShown: true });
      writeLocal(LOCAL_KEYS.WELCOME_SHOWN, "true");
      try {
        await upsertStringSetting(CHECKLIST_SETTINGS_KEYS.WELCOME_SHOWN, "true");
      } catch (e) {
        logger.warn("[ChecklistStore] Failed to persist welcomeShown to DB", e);
      }
    },

    setPanelState: async (panelState: ChecklistPanelState) => {
      set({ panelState });
      // Mirror first, and synchronously. If the RPC below is slow or fails, a
      // reload still has to remember that the user closed this.
      writeLocal(LOCAL_KEYS.PANEL_STATE, panelState);
      try {
        await upsertStringSetting(
          CHECKLIST_SETTINGS_KEYS.PANEL_STATE,
          panelState,
        );
      } catch (e) {
        logger.warn("[ChecklistStore] Failed to persist panelState to DB", e);
      }
    },

    revive: async () => {
      set({ panelState: "expanded" });
      writeLocal(LOCAL_KEYS.PANEL_STATE, "expanded");
      try {
        await upsertStringSetting(
          CHECKLIST_SETTINGS_KEYS.PANEL_STATE,
          "expanded",
        );
      } catch (e) {
        logger.warn("[ChecklistStore] Failed to persist revive to DB", e);
      }
    },

    detectCompletedItems: async () => {
      const { completedItems } = get();
      const newItems = new Set(completedItems);
      let changed = false;

      const projectId = useProjectStore.getState().currentProject?.id;

      // 1. API key
      if (!newItems.has("add-api-key")) {
        try {
          const providers = await api.settings.getProviders();
          if (providers.some((p) => p.hasApiKey || p.configured)) {
            newItems.add("add-api-key");
            changed = true;
          }
        } catch {
          // Fail open
        }
      }

      // 2. Start a chat
      if (!newItems.has("start-chat")) {
        const chats = getCachedChatList(projectId);
        if (chats.length > 0) {
          newItems.add("start-chat");
          changed = true;
        }
      }

      // 3. Use a custom workflow
      if (!newItems.has("use-custom-workflow")) {
        const chats = getCachedChatList(projectId);
        const isDefaultWorkflow = (name?: string) =>
          !name || name === "" || name === "agent" || name === "builtin://agent";
        const usedCustomWorkflow = chats.some(
          (chat) => !isDefaultWorkflow(chat.workflowName),
        );
        if (usedCustomWorkflow) {
          newItems.add("use-custom-workflow");
          changed = true;
        }
      }

      // 4. Create a workflow
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

      // 5. Take product tour — read from tour store
      if (!newItems.has("take-product-tour")) {
        const { useTourStore } = await import("./tourStore");
        if (useTourStore.getState().hasCompletedOnboarding) {
          newItems.add("take-product-tour");
          changed = true;
        }
      }

      // 6. Create a workspace
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

      // 8. Create a preset
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

      // 9. Read docs — manually marked only

      if (changed) {
        set({ completedItems: newItems });
        const serialized = serializeItems(newItems);
        writeLocal(LOCAL_KEYS.COMPLETED_ITEMS, serialized);
        await upsertStringSetting(
          CHECKLIST_SETTINGS_KEYS.COMPLETED_ITEMS,
          serialized,
        );
        logger.info("[ChecklistStore] Auto-detected completions", {
          items: Array.from(newItems),
        });
      }
    },

    subscribeToStoreChanges: () => {
      const unsubscribers: (() => void)[] = [];

      // Chats live in the React Query cache (the single source of truth), so
      // subscribe to the query cache and re-read the current project's list on
      // each change instead of subscribing to the Zustand store.
      const currentChatList = () =>
        getCachedChatList(useProjectStore.getState().currentProject?.id);
      let prevChatCount = currentChatList().length;
      unsubscribers.push(
        queryClient.getQueryCache().subscribe((event) => {
          // Only react to chat-list cache mutations.
          if (
            event.query.queryKey[0] !== chatKeys.all[0] ||
            event.query.queryKey[1] !== "list"
          ) {
            return;
          }
          const chats = currentChatList();
          if (chats.length > prevChatCount) {
            get().markComplete("start-chat");
          }
          prevChatCount = chats.length;

          if (!get().completedItems.has("use-custom-workflow")) {
            const isDefault = (name?: string) =>
              !name || name === "" || name === "agent" || name === "builtin://agent";
            const usedCustom = chats.some(
              (chat) => !isDefault(chat.workflowName),
            );
            if (usedCustom) {
              get().markComplete("use-custom-workflow");
            }
          }
        }),
      );

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

      try {
        const unsubscribeApiKey = getEventBus().on("api-key:saved", () => {
          get().markComplete("add-api-key");
        });
        unsubscribers.push(unsubscribeApiKey);
      } catch {
        /* bus not ready yet */
      }

      return () => {
        unsubscribers.forEach((unsub) => unsub());
      };
    },

    reset: () => {
      set({
        completedItems: new Set(),
        welcomeShown: false,
        panelState: "expanded",
        isInitialized: false,
        isLoading: false,
      });
      // Clear the local mirror too, or loadState() would immediately resolve
      // the cleared state back to whatever this device last recorded.
      for (const key of Object.values(LOCAL_KEYS)) {
        try {
          localStorage.removeItem(key);
        } catch {
          /* private mode */
        }
      }
    },
  }),
);