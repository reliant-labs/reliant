/**
 * The Setup Guide's items (OnboardingChecklist.tsx), and where a retired item's
 * progress goes. Its own module, apart from constants.ts: it reads whether this
 * deployment has a control plane, which the tour's lighter importers (the forge
 * demo's bundle) must not pull in.
 */

import { orgMembershipAvailable } from "../../services/controlPlane/members";
import type { ChecklistItem, ChecklistItemId, LegacyChecklistItemId } from "./types";

// ─── Checklist Items ─────────────────────────────────────────────────────────

/**
 * The Setup Guide: the new app in the order a new person meets it. Each item
 * completes on a real signal, never on a click (store/onboardingChecklistStore.ts
 * `detectCompletedItems` and `subscribeToStoreChanges`):
 *
 *   connect-machine    the daemon list holds a usable machine (ACTIVE, PENDING or SUSPENDED)
 *   add-api-key        a model provider is configured (provider statuses)
 *   start-chat         the chat list holds a chat
 *   add-repo           the project catalog holds a project of yours with a git remote
 *   run-workflow       a chat ran a workflow other than the default agent, or you have a workflow of your own
 *   deploy-app         an app in the org's catalog has been deployed (`lastDeployedAt`)
 *   invite-teammate    the org's seats show a second member or a pending invitation
 *   take-product-tour  a tour journey was completed (Finish, not Exit)
 *
 * The org's items (deploy, invite) need a control plane; without one they are
 * neither shown nor counted.
 */
export const CHECKLIST_ITEMS: ChecklistItem[] = [
  {
    id: "connect-machine",
    title: "Connect a machine",
    description: "Your computer or a Reliant sandbox runs the agents",
  },
  {
    id: "add-api-key",
    title: "Choose your AI",
    description: "An API key or a subscription you already pay for",
  },
  {
    id: "start-chat",
    title: "Start a chat",
    description: "Ask it to fix a bug, write a test, or explain some code",
  },
  {
    id: "add-repo",
    title: "Add your repository",
    description: "Clone a GitHub repo so agents work on your code",
  },
  {
    id: "run-workflow",
    title: "Run a workflow",
    description: "A repeatable process: review, test, fix, then ask before merging",
  },
  {
    id: "deploy-app",
    title: "Deploy an app",
    description: "Build one with forge and get a live URL on Reliant hosting",
    available: () => orgMembershipAvailable,
  },
  {
    id: "invite-teammate",
    title: "Invite a teammate",
    description: "Share chats, apps and machines with your org",
    available: () => orgMembershipAvailable,
  },
  {
    id: "take-product-tour",
    title: "Take a tour",
    description: "Two minutes on workflows, or on a forge app's console",
  },
];

/** The items that apply in this deployment, in order: what the guide shows and counts. */
export function activeChecklistItems(): ChecklistItem[] {
  return CHECKLIST_ITEMS.filter((item) => item.available?.() ?? true);
}

const CHECKLIST_ITEM_IDS = new Set<string>(CHECKLIST_ITEMS.map((item) => item.id));

export function isChecklistItemId(id: string): id is ChecklistItemId {
  return CHECKLIST_ITEM_IDS.has(id);
}

/**
 * Where a retired id's progress goes. The two workflow items became one; the
 * chat-only extras (a workspace, a preset, an MCP server, the docs) left the
 * guide, and their progress counts toward nothing.
 */
export const LEGACY_CHECKLIST_ITEMS: Record<LegacyChecklistItemId, ChecklistItemId | null> = {
  "use-custom-workflow": "run-workflow",
  "create-workflow": "run-workflow",
  "create-workspace": null,
  "create-preset": null,
  "install-mcp": null,
  "read-docs": null,
};

/** The old guide's required items: someone who had them all saw it retire, and is not shown the new one unasked. */
export const LEGACY_GUIDE_FINISHED: readonly string[] = [
  "add-api-key",
  "start-chat",
  "use-custom-workflow",
  "create-workflow",
  "take-product-tour",
];

/** A stored list of ids, current and retired, as the current ids it stands for. */
export function migrateChecklistIds(stored: Iterable<string>): Set<ChecklistItemId> {
  const out = new Set<ChecklistItemId>();
  for (const id of stored) {
    if (isChecklistItemId(id)) out.add(id);
    else if (id in LEGACY_CHECKLIST_ITEMS) {
      const next = LEGACY_CHECKLIST_ITEMS[id as LegacyChecklistItemId];
      if (next) out.add(next);
    }
  }
  return out;
}

export function getChecklistItem(
  id: ChecklistItemId,
): ChecklistItem | undefined {
  return CHECKLIST_ITEMS.find((item) => item.id === id);
}
