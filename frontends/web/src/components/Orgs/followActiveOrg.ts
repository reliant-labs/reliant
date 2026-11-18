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
import { keyNamesOrg } from "@/lib/orgScope";
import { useProjectStore } from "@/store/projectStore";

import type { OrgChange } from "./ActiveOrgBinding";

/**
 * The product reads the server answers for the active org: projects, chats,
 * saved workflows, triggers and runs each belong to one org, and their list
 * rpcs (the archived chats, chat search and the Inbox too) filter by the
 * request's org_id. The lists name the org in their request and so in their
 * cache key (lib/orgScope), but a switch still resets every product read: an
 * rpc that is not a list (a project, a run) is answered for the org the
 * request named too, and nothing should show the old org's meanwhile.
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
 * The product's org-scoped LISTS: each names its org in its key, and one that
 * names none was answered with every org's rows (lib/orgScope).
 */
export const ORG_SCOPED_PRODUCT_LISTS: readonly QueryKey[] = [
  projectServiceKeys.listProjects.all,
  chatKeys.lists(),
  chatServiceKeys.listArchivedChats.all,
  chatServiceKeys.searchChats.all,
  inboxServiceKeys.listInbox.all,
  workflowServiceKeys.listWorkflows.all,
  triggerServiceKeys.listTriggers.all,
  runServiceKeys.listRuns.all,
];

/**
 * ActiveOrgBinding's `onSwitch` for the product app: reset the org's product
 * reads, then move the sidebar filter and the context project off another
 * org's project (projectStore.followActiveOrg).
 *
 * On a tab's first load (`firstLoad`: from no org to its first) nothing was
 * shown in another org, so the selection stays; only the list entries that
 * do not name the org are dropped, so an answer read with no org (every org's
 * rows) is never served again.
 */
export function followActiveOrg(orgId: string | null, client: QueryClient, change?: OrgChange): void {
  if (change?.firstLoad) {
    if (!orgId) return;
    for (const queryKey of ORG_SCOPED_PRODUCT_LISTS) {
      void client.resetQueries({ queryKey, predicate: (query) => !keyNamesOrg(query.queryKey, orgId) });
    }
    return;
  }
  for (const queryKey of ORG_SCOPED_PRODUCT_QUERIES) void client.resetQueries({ queryKey });
  for (const queryKey of ORG_STALE_PRODUCT_QUERIES) void client.invalidateQueries({ queryKey });
  void useProjectStore.getState().followActiveOrg(orgId);
}
