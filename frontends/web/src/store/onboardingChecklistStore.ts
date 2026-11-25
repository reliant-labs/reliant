/**
 * Onboarding Checklist Store
 *
 * The Setup Guide's state: which items are done, and whether the guide is
 * open, collapsed or dismissed. Every item completes on a real signal (the
 * list and its signals: Onboarding/checklistItems.ts), read from the query
 * cache the rest of the app already fills, never from a click on the guide.
 * The guided-tour wizard lives in tourStore.ts; the two stores coordinate
 * for the "take-product-tour" achievement.
 *
 * Retired ids (the chat-only guide's) are migrated as they load
 * (`migrateChecklistIds`), and `checklist_step_completed` fires once per item
 * when its signal is first met, with the item's stable id.
 *
 * "add-api-key" specifically: like every other item here it is STICKY —
 * once earned it stays in `completedItems` even if the user later removes
 * their only key — consistent with the rest of the checklist (e.g.
 * "add-repo" doesn't un-complete when the project is deleted) and
 * with what `completedItems` already persists to the settings table. What
 * changed is not stickiness but WHERE the "earned" signal comes from: it is
 * now derived by reading `providerStatusesKey()` — the same React Query
 * cache `useProviderStatuses()` (hooks/settings-queries.ts) exposes to the
 * rest of the app — rather than by trusting a UI flow to announce itself via
 * the "api-key:saved" event. See `subscribeToStoreChanges` below for why a
 * forgotten emit site can no longer leave this permanently unchecked.
 */

import { create } from "zustand";
import { logger } from "../lib/logger";
import {
  readSetting,
  upsertStringSetting,
} from "../lib/settingsPersistence";
import { CHECKLIST_SETTINGS_KEYS } from "../components/Onboarding/constants";
import {
  activeChecklistItems,
  isChecklistItemId,
  LEGACY_GUIDE_FINISHED,
  migrateChecklistIds,
} from "../components/Onboarding/checklistItems";
import { trackEvent } from "../lib/analytics";
import type {
  ChecklistItemId,
  ChecklistPanelState,
} from "../components/Onboarding/types";
import { tourEvents } from "../components/Onboarding/tourEvents";
import { useProjectStore } from "./projectStore";
import { getEventBus } from "../lib/events";
import { chatKeys, getCachedChatList } from "../hooks/chat-queries";
import {
  fetchProviderStatuses,
  hasAnyConfiguredProvider,
  invalidateProviderStatuses,
  providerStatusesKey,
} from "../hooks/settings-queries";
import { hashKey, partialMatchKey } from "@tanstack/react-query";
import { getCachedWorkflows, workflowListFromProto } from "../hooks/workflow-queries";
import { workflowServiceKeys } from "../hooks/workflow-service-hooks_gen";
import type { ListWorkflowsResponse } from "../gen/reliant/v1/workflow_pb";
import type { GetProviderStatusesResponse } from "../gen/reliant/v1/settings_pb";
import { queryClient } from "../lib/query-client";
import { useAuthStore } from "./authStore";
import { getOrgScope, whenOrgScopeReady } from "../lib/orgScope";
import { cachedDaemonList, DAEMON_LIST_QUERY_KEY, fetchDaemonList } from "../hooks/daemon-registry-queries";
import { hasUsableDaemonForOnboarding } from "../components/OnboardingFlow/usableDaemon";
import type { ListDaemonsResponse } from "../gen/reliant/v1/daemon_registry_pb";
import { projectServiceKeys } from "../hooks/project-service-hooks_gen";
import type { ListProjectsWithAppsResponse } from "../gen/reliant/v1/project_pb";
import { catalogFromResponse, catalogHasDeployedApp as catalogHasApp, catalogHasOwnRepo } from "../services/projects/catalog";
import { organizationServiceKeys, organizationServiceQueries } from "../hooks/controlplane/organization-service-hooks_gen";
import type { ListInvitationsResponse, ListMembersResponse } from "../gen/controlplane/services/organization/v1/organization_pb";
import { orgMembershipAvailable, seatsShowATeam, toMemberPage } from "../services/controlPlane/members";
import type { JourneyRecord } from "./tourStore";

/**
 * Whether an outbound RPC from this store can carry a credential.
 *
 * This store is driven by OnboardingWizard, which is mounted at the ROUTER
 * ROOT — so it is alive on every route, including the signed-out sign-in
 * screen. Its `if (!user) return null` guard stops it RENDERING, but effects
 * still run, so `detectCompletedItems()` and the provider poll below both
 * fired against a session that did not exist. Both swallow their failures
 * ("fail open"), so nothing surfaced and nothing stopped: that is where the
 * 401s against GetProviderStatuses on /auth actually came from.
 *
 * Read imperatively rather than through the hook — these are store actions and
 * timer callbacks, not React renders, and the answer must be the session as it
 * stands at the moment of the call.
 */
function hasSession(): boolean {
  return !!useAuthStore.getState().session;
}

// ─── Store Interface ──────────────────────────────────────────────────────────

interface OnboardingChecklistState {
  /** Items the user has completed */
  completedItems: Set<ChecklistItemId>;

  /** Whether the welcome modal has been shown */
  welcomeShown: boolean;

  /** The floating Setup Guide: open, collapsed to its pill, or dismissed */
  panelState: ChecklistPanelState;

  /** Whether the initial state has been loaded from DB */
  isInitialized: boolean;

  /** Loading guard */
  isLoading: boolean;

  // ─── Derived ────────────────────────────────────────────────────────────

  /** How many of the items that apply here are done */
  totalCompleted: () => number;

  /** How many items apply here (activeChecklistItems) */
  totalItems: () => number;

  /** Whether every item that applies here is done: the guide retires */
  allComplete: () => boolean;

  /** Completion percentage of the items that apply here */
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

/** A stored list's raw ids: current ones and retired ones (migrateChecklistIds reads both). */
function deserializeIds(json: string): string[] {
  try {
    const parsed: unknown = JSON.parse(json);
    return Array.isArray(parsed) ? parsed.filter((id): id is string => typeof id === "string") : [];
  } catch {
    return [];
  }
}

/** One `checklist_step_completed` per item, when its signal is first met (the id is stable across releases). */
function trackCompleted(itemId: ChecklistItemId, done: Set<ChecklistItemId>): void {
  const item = activeChecklistItems().find((i) => i.id === itemId);
  if (!item) return;
  trackEvent("checklist_step_completed", {
    stepId: item.id,
    stepName: item.title,
    stepsCompleted: activeChecklistItems().filter((i) => done.has(i.id)).length,
    totalSteps: activeChecklistItems().length,
  });
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

// ─── Signals ──────────────────────────────────────────────────────────────────
// Each item's completion, read from what the app already caches.

/** A chat that ran a workflow other than the default agent. */
function chatsRanAWorkflow(chats: ReadonlyArray<{ workflowName?: string }>): boolean {
  const isDefault = (name?: string) => !name || name === "agent" || name === "builtin://agent";
  return chats.some((chat) => !isDefault(chat.workflowName));
}

/** Every project catalog (ListProjectsWithApps answer) in the cache, whichever org's. */
function cachedCatalogs(): ListProjectsWithAppsResponse[] {
  return queryClient
    .getQueriesData<ListProjectsWithAppsResponse>({ queryKey: projectServiceKeys.listProjectsWithApps.all })
    .flatMap(([, data]) => (data?.projects ? [data] : []));
}

function catalogHasRepo(response: ListProjectsWithAppsResponse): boolean {
  return catalogHasOwnRepo(catalogFromResponse(response));
}

function catalogHasDeployedApp(response: ListProjectsWithAppsResponse): boolean {
  return catalogHasApp(catalogFromResponse(response));
}

/** A tour journey finished with Finish, at its current version or an older one. Exit tour and a dismissed picker are not. */
function journeyCompleted(record: JourneyRecord): boolean {
  return record.current === "completed" || record.completedOlder;
}

// Bumped by reset() (an identity change: authStore): a load or detection
// started for the previous user drops its answer instead of installing that
// user's progress, and re-mirroring it, for the next one.
let generation = 0;

// ─── Store ────────────────────────────────────────────────────────────────────

export const useOnboardingChecklistStore = create<OnboardingChecklistState>(
  (set, get) => ({
    completedItems: new Set(),
    welcomeShown: false,
    panelState: "collapsed",
    isInitialized: false,
    isLoading: false,

    // ─── Derived ──────────────────────────────────────────────────────────

    totalCompleted: () => {
      const { completedItems } = get();
      return activeChecklistItems().filter((item) => completedItems.has(item.id)).length;
    },

    totalItems: () => activeChecklistItems().length,

    allComplete: () => {
      const { completedItems } = get();
      return activeChecklistItems().every((item) => completedItems.has(item.id));
    },

    completionPercentage: () => {
      const total = get().totalItems();
      if (total === 0) return 100;
      return Math.round((get().totalCompleted() / total) * 100);
    },

    // ─── Checklist Actions ────────────────────────────────────────────────

    loadState: async () => {
      if (get().isLoading) return;
      set({ isLoading: true });
      const startedIn = generation;

      const [completedRead, panelRead, welcomeRead] = await Promise.all([
        readSetting(CHECKLIST_SETTINGS_KEYS.COMPLETED_ITEMS),
        readSetting(CHECKLIST_SETTINGS_KEYS.PANEL_STATE),
        readSetting(CHECKLIST_SETTINGS_KEYS.WELCOME_SHOWN),
      ]);
      if (startedIn !== generation) return;

      const remotePanel =
        panelRead.status === "found" && isPanelState(panelRead.value)
          ? panelRead.value
          : null;
      const localPanelRaw = readLocal(LOCAL_KEYS.PANEL_STATE);
      const localPanel = isPanelState(localPanelRaw) ? localPanelRaw : null;

      // Union the two records. Completions are monotonic — an item once earned
      // is never un-earned — so merging is always safe, and it stops a failed
      // read from resetting the user's progress to zero. Retired ids become
      // the ids that replaced them (checklistItems.ts LEGACY_CHECKLIST_ITEMS).
      const remoteIds = completedRead.status === "found" ? deserializeIds(completedRead.value) : null;
      const localIds = deserializeIds(readLocal(LOCAL_KEYS.COMPLETED_ITEMS) ?? "[]");
      const storedIds = [...(remoteIds ?? []), ...localIds];
      const completedItems = migrateChecklistIds(storedIds);
      const migrated = storedIds.some((id) => !isChecklistItemId(id));

      // Someone who finished the old, chat-only guide saw it retire. The new
      // one has items they never had, but it does not reappear on every page
      // unasked: it stays put away, and Help › Show setup guide brings it back.
      // Read from the system of record when it answered: once migrated it holds
      // no old ids, so a stale mirror on another device cannot put a guide the
      // person revived away again.
      const finishedOldGuide = (ids: string[]) => LEGACY_GUIDE_FINISHED.every((id) => ids.includes(id));
      let panelState = resolvePanelState(remotePanel, localPanel);
      if (panelState !== "dismissed" && finishedOldGuide(remoteIds ?? localIds)) {
        panelState = "dismissed";
      }

      const welcomeShown =
        (welcomeRead.status === "found" && welcomeRead.value === "true") ||
        readLocal(LOCAL_KEYS.WELCOME_SHOWN) === "true";

      // Re-mirror the resolved answer so the next boot is decidable offline and
      // so a dismissal made on another device is honored on this one.
      writeLocal(LOCAL_KEYS.PANEL_STATE, panelState);
      if (completedItems.size > 0 || migrated) {
        writeLocal(LOCAL_KEYS.COMPLETED_ITEMS, serializeItems(completedItems));
      }
      // The settings row is rewritten in current ids once, so the next load
      // (on any device) has nothing to migrate.
      if (migrated) {
        void upsertStringSetting(CHECKLIST_SETTINGS_KEYS.COMPLETED_ITEMS, serializeItems(completedItems)).catch(() => {
          /* best-effort; the next load migrates again */
        });
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
      trackCompleted(itemId, newItems);
      // A journey step can wait on this (advanceOn: { kind: "event",
      // event: "checklist:itemCompleted" }); free when none is listening.
      tourEvents.emit("checklist:itemCompleted", { item: itemId });

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
      const startedIn = generation;
      const { completedItems } = get();
      const newItems = new Set(completedItems);
      const found = (id: ChecklistItemId, met: boolean) => {
        if (met && !newItems.has(id)) newItems.add(id);
      };

      const projectId = useProjectStore.getState().currentProject?.id;

      // A machine: the daemon list the shell polls (one shared entry). Read it
      // once if nothing has yet; a failed read leaves the item for later.
      if (!newItems.has("connect-machine") && hasSession()) {
        try {
          const daemons = cachedDaemonList(queryClient) ?? (await fetchDaemonList(queryClient));
          found("connect-machine", hasUsableDaemonForOnboarding(daemons));
        } catch {
          // Fail open
        }
      }

      // Your AI — read through the shared React Query cache (providerStatusesKey())
      // rather than calling the API directly, so this and every other consumer of
      // "is a provider configured?" agree on one in-flight request and one cached answer.
      if (!newItems.has("add-api-key") && hasSession()) {
        try {
          const { providers } = await fetchProviderStatuses(10_000);
          found("add-api-key", hasAnyConfiguredProvider(providers));
        } catch {
          // Fail open
        }
      }

      const chats = getCachedChatList(projectId);
      found("start-chat", chats.length > 0);

      // A repository and a deployed app: whatever project catalog the shell read.
      for (const response of cachedCatalogs()) {
        found("add-repo", catalogHasRepo(response));
        found("deploy-app", catalogHasDeployedApp(response));
      }

      // A workflow: a chat ran one, or the person has one of their own.
      found("run-workflow", chatsRanAWorkflow(chats) || getCachedWorkflows().some((w) => w.source === "user"));

      // A teammate: the tab's org's seats, read once the org is known, while it
      // is outstanding (the Members page refreshes the cache entries the
      // subscription below watches).
      if (!newItems.has("invite-teammate") && orgMembershipAvailable && hasSession()) {
        try {
          const scope = getOrgScope();
          const { orgId } = scope.ready ? scope : await whenOrgScopeReady();
          if (!orgId) throw new Error("no org");
          // The org's seat count: a one-member ListMembers page, under the
          // roster's keys (ORG_MEMBER_READS), so an org switch drops it.
          const page = await queryClient.fetchQuery(
            organizationServiceQueries.listMembers({ orgId, pageSize: 1 }, { staleTime: 60_000 }),
          );
          found("invite-teammate", seatsShowATeam(toMemberPage(page).seats));
        } catch {
          // Fail open (a self-hosted server, a member who may not read the roster)
        }
      }

      // The tour: a journey completed (Finish), at any version.
      const { useTourStore } = await import("./tourStore");
      found("take-product-tour", Object.values(useTourStore.getState().journeys ?? {}).some(journeyCompleted));

      if (startedIn !== generation) return;
      // Other detectors and live completion signals may have committed while
      // these reads were pending. Achievements are monotonic: merge with the
      // current set, never replace it with the pre-await snapshot.
      const currentItems = get().completedItems;
      const added = [...newItems].filter((id) => !currentItems.has(id));
      for (const id of currentItems) newItems.add(id);
      if (added.length > 0) {
        set({ completedItems: newItems });
        for (const id of added) {
          trackCompleted(id, newItems);
          tourEvents.emit("checklist:itemCompleted", { item: id });
        }
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
      const done = (id: ChecklistItemId) => get().completedItems.has(id);
      const cache = queryClient.getQueryCache();

      // Chats live in the React Query cache (the single source of truth), so
      // subscribe to the query cache and re-read the current project's list on
      // each change instead of subscribing to the Zustand store.
      unsubscribers.push(
        cache.subscribe((event) => {
          // Only react to chat-list cache mutations.
          if (
            event.query.queryKey[0] !== chatKeys.all[0] ||
            event.query.queryKey[1] !== "list"
          ) {
            return;
          }
          const chats = getCachedChatList(useProjectStore.getState().currentProject?.id);
          if (!done("start-chat") && chats.length > 0) void get().markComplete("start-chat");
          if (!done("run-workflow") && chatsRanAWorkflow(chats)) void get().markComplete("run-workflow");
        }),
      );

      // "run-workflow" also derives from any workflow list the app reads (the
      // ListWorkflows entries under workflowServiceKeys): a workflow of your own.
      unsubscribers.push(
        cache.subscribe((event) => {
          if (done("run-workflow")) return;
          if (!partialMatchKey(event.query.queryKey, workflowServiceKeys.all)) return;
          const data = event.query.state.data as Partial<ListWorkflowsResponse> | undefined;
          if (!Array.isArray(data?.workflows)) return;
          const { workflows } = workflowListFromProto(data as ListWorkflowsResponse);
          if (workflows.some((w) => w.source === "user")) {
            void get().markComplete("run-workflow");
          }
        }),
      );

      // "connect-machine": the one daemon list (DAEMON_LIST_QUERY_KEY), as it is polled.
      const daemonListHash = hashKey(DAEMON_LIST_QUERY_KEY);
      unsubscribers.push(
        cache.subscribe((event) => {
          if (done("connect-machine")) return;
          if (event.query.queryHash !== daemonListHash) return;
          const daemons = (event.query.state.data as ListDaemonsResponse | undefined)?.daemons;
          if (daemons && hasUsableDaemonForOnboarding(daemons)) void get().markComplete("connect-machine");
        }),
      );

      // "add-repo" and "deploy-app": the project catalog (ListProjectsWithApps), whichever org's.
      unsubscribers.push(
        cache.subscribe((event) => {
          if (done("add-repo") && done("deploy-app")) return;
          if (!partialMatchKey(event.query.queryKey, projectServiceKeys.listProjectsWithApps.all)) return;
          const response = event.query.state.data as ListProjectsWithAppsResponse | undefined;
          if (!response?.projects) return;
          if (!done("add-repo") && catalogHasRepo(response)) void get().markComplete("add-repo");
          if (!done("deploy-app") && catalogHasDeployedApp(response)) void get().markComplete("deploy-app");
        }),
      );

      // "invite-teammate": the org's roster (its seat count) or its invitations,
      // as the Members page and the read above leave them.
      unsubscribers.push(
        cache.subscribe((event) => {
          if (done("invite-teammate")) return;
          const key = event.query.queryKey;
          const watched =
            partialMatchKey(key, organizationServiceKeys.listMembers.all) ||
            partialMatchKey(key, organizationServiceKeys.listInvitations.all);
          if (!watched) return;
          // A plain read holds the response; the roster's paged read, its pages.
          const data = event.query.state.data as
            | ListMembersResponse
            | ListInvitationsResponse
            | { pages: (ListMembersResponse | ListInvitationsResponse)[] }
            | undefined;
          const first = data && "pages" in data ? data.pages[0] : data;
          if (!first) return;
          const team =
            "invitations" in first
              ? first.invitations.some((invitation) => invitation.status === "pending")
              : seatsShowATeam(toMemberPage(first).seats);
          if (team) void get().markComplete("invite-teammate");
        }),
      );

      // "add-api-key" derives from the provider-status query cache
      // (providerStatusesKey()), not from any one save flow announcing
      // itself. Whenever that cache is written — by a background refetch, a
      // focus refetch, or the invalidation nudge below — re-read it and mark
      // complete if a provider is now configured. This is what makes the
      // completion correct even when nothing ever emits "api-key:saved": the
      // checklist agrees with whatever the settings page or a fresh
      // getProviders() poll would show, on its own schedule.
      const providerStatusesHash = hashKey(providerStatusesKey());
      unsubscribers.push(
        queryClient.getQueryCache().subscribe((event) => {
          if (get().completedItems.has("add-api-key")) return;
          if (event.query.queryHash !== providerStatusesHash) return;
          const providers = (event.query.state.data as GetProviderStatusesResponse | undefined)?.providers;
          if (providers && hasAnyConfiguredProvider(providers)) {
            get().markComplete("add-api-key");
          }
        }),
      );

      // "api-key:saved" is now ONLY a refetch nudge, not a completion signal.
      // A missed emit — a new save path that forgets to fire it — is
      // harmless: the query cache subscription above still catches the
      // completion on the next natural refetch (focus, remount, staleTime
      // expiry, or the poll below). This is what fixed the RedeemCouponForm
      // bug's whole class: no call site is load-bearing for correctness
      // anymore, only for latency.
      try {
        const unsubscribeApiKey = getEventBus().on("api-key:saved", () => {
          invalidateProviderStatuses();
        });
        unsubscribers.push(unsubscribeApiKey);
      } catch {
        /* bus not ready yet */
      }

      // Belt-and-suspenders poll: refetch provider status periodically while
      // "add-api-key" is outstanding, so the item still completes for a user
      // who never triggers a refetch any other way (no window blur/focus, no
      // remount, no event emitted at all). `fetchQuery` (not
      // `invalidateQueries`) so this fires even with no active observer of
      // the query — the cache-subscribe listener above reacts to the write
      // either way. Stops once the item is complete — nothing left to detect.
      const providerPoll = setInterval(() => {
        if (get().completedItems.has("add-api-key")) return;
        // Signed out there is nothing to detect and no credential to detect it
        // with — this is the timer that produced the visible 401 "polling".
        // Skipping a tick (rather than clearing the interval) means the poll
        // resumes by itself the moment a session lands, with no re-arming.
        if (!hasSession()) return;
        // Nobody is reading a hidden tab, and a returning user is caught up by
        // the focus refetch. Background tabs throttle this timer to once a
        // minute rather than stopping it, which is how one forgotten tab made
        // 1,096 of prod's 1,183 GetProviderStatuses in a day (2026-10-08,
        // exactly 60s apart for fourteen hours).
        if (typeof document !== "undefined" && document.visibilityState === "hidden") return;
        void fetchProviderStatuses(0).catch(() => undefined);
      }, 15_000);
      unsubscribers.push(() => clearInterval(providerPoll));

      return () => {
        unsubscribers.forEach((unsub) => unsub());
      };
    },

    reset: () => {
      generation++;
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