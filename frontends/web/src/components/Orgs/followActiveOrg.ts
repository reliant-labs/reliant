// Copyright (c) 2025 Reliant Labs

import type { QueryClient, QueryKey } from "@tanstack/react-query";

import { chatKeys } from "@/hooks/chat-queries";
import { chatServiceKeys } from "@/hooks/chat-service-hooks_gen";
import { inboxServiceKeys } from "@/hooks/inbox-service-hooks_gen";
import { projectServiceKeys } from "@/hooks/project-service-hooks_gen";
import { runServiceKeys } from "@/hooks/run-service-hooks_gen";
import { sharingServiceKeys } from "@/hooks/sharing-service-hooks_gen";
import { triggerServiceKeys } from "@/hooks/trigger-service-hooks_gen";
import { workflowServiceKeys } from "@/hooks/workflow-service-hooks_gen";
import { useProjectStore } from "@/store/projectStore";

/**
 * The product reads the server answers for the active org: projects, chats,
 * saved workflows, triggers and runs each belong to one org, and their list
 * rpcs (the archived chats, chat search and the Inbox too) filter by the
 * request's org_id, which orgInterceptor fills from the active org. Their cache keys do not name the org, so a switch resets them:
 * the next read asks for the new org's, and nothing shows the old org's
 * meanwhile.
 *
 * The chat list is reset, not every chat read: the open chat's detail and
 * messages are not the list, and dropping them would blank the pane before
 * followActiveOrg moves off it. Those are only marked stale.
 */
export const ORG_SCOPED_PRODUCT_QUERIES: readonly QueryKey[] = [
  chatKeys.lists(),
  chatServiceKeys.listArchivedChats.all,
  chatServiceKeys.searchChats.all,
  inboxServiceKeys.all,
  projectServiceKeys.all,
  workflowServiceKeys.all,
  triggerServiceKeys.all,
  runServiceKeys.all,
  // The shared lists name their org, but a switch drops another org's anyway.
  sharingServiceKeys.all,
];

/** Kept, but re-read when next observed: a chat's own reads. */
export const ORG_STALE_PRODUCT_QUERIES: readonly QueryKey[] = [chatKeys.details()];

/**
 * ActiveOrgBinding's `onSwitch` for the product app: reset the org's product
 * reads, then move the sidebar filter and the context project off another
 * org's project (projectStore.followActiveOrg).
 */
export function followActiveOrg(orgId: string | null, client: QueryClient): void {
  for (const queryKey of ORG_SCOPED_PRODUCT_QUERIES) void client.resetQueries({ queryKey });
  for (const queryKey of ORG_STALE_PRODUCT_QUERIES) void client.invalidateQueries({ queryKey });
  void useProjectStore.getState().followActiveOrg(orgId);
}
