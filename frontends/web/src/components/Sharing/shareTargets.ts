// Copyright (c) 2025 Reliant Labs

/**
 * Who may be offered a share toggle, decided once for every menu that offers
 * one (the chat header, a chat row in the sidebar, the Workflows library).
 * The server is the authority (SharingService refuses anyone else); these
 * keep a menu from offering what it would refuse.
 */

import { hasControlPlane } from "@/services/controlPlane/config";

/** Whether sharing is wired up at all: it goes through the control plane. */
export function sharingAvailable(): boolean {
  return hasControlPlane;
}

/**
 * Whether the caller may share this chat: only its owner (a viewer reads
 * someone else's shared chat), and only a chat of an org, with a control
 * plane to share through (SetChatVisibility).
 */
export function canShareChat(chat: { orgId?: string; viewerAccess?: boolean } | null | undefined): boolean {
  return sharingAvailable() && !!chat && !chat.viewerAccess && !!chat.orgId;
}

/** A saved workflow its owner can share: what the share dialog needs. */
export interface ShareableWorkflow {
  /** The saved workflow's id (SetWorkflowDraftSharing's workflow_id). */
  draftId: string;
  /** Its display name. */
  name: string;
  orgId: string;
  shared: boolean;
}

/**
 * The saved workflow behind a list or get answer, when its owner can share
 * it: one of the caller's own (source "user"; ListWorkflows and GetWorkflow
 * answer only the owner's saved workflows), in an org, from a server that
 * says whether it is shared. Builtin and project workflows are nobody's to
 * share.
 */
export function shareableWorkflow(
  workflow: { source?: string; draftId?: string; orgId?: string; shared?: boolean } | null | undefined,
  name: string,
): ShareableWorkflow | null {
  if (!sharingAvailable() || !workflow || workflow.source !== "user") return null;
  if (!workflow.draftId || !workflow.orgId || workflow.shared === undefined) return null;
  return { draftId: workflow.draftId, name, orgId: workflow.orgId, shared: workflow.shared };
}
