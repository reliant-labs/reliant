// Copyright (c) 2025 Reliant Labs

import type { UserUpdate } from "../types/streaming";

/**
 * Where the filter learns a resource's org when the update does not say:
 * what the app already holds of the chat and project it names.
 */
export interface OrgLookup {
  chatOrg: (chatId: string) => string | undefined;
  projectOrg: (projectId: string) => string | undefined;
}

/**
 * Whether a user-stream update is about an org other than the active one, so
 * the app (which shows one org at a time) leaves it out.
 *
 * The stream delivers every org the person is in; it names the org it decided
 * each update by (`org_id`). An update from a server that predates the field
 * is judged by the chat or project it names, as far as the app holds them
 * (Chat.org_id, Project.org_id). An update about nothing of an org (a daemon
 * signal), or one whose org is unknown, is kept: every handler it reaches
 * patches only what the app already shows, or asks the server again, and the
 * server's lists answer for the active org alone.
 *
 * `openChatId` is the chat the person has open: its updates are kept whatever
 * its org, since they opened it.
 */
export function isOtherOrgUpdate(
  update: UserUpdate,
  activeOrgId: string | null,
  lookup: OrgLookup,
  openChatId: string | null = null,
): boolean {
  if (!activeOrgId) return false;
  if (openChatId && update.chat_id === openChatId) return false;
  const org =
    update.org_id ||
    (update.chat_id ? lookup.chatOrg(update.chat_id) : undefined) ||
    (update.project_id ? lookup.projectOrg(update.project_id) : undefined);
  return !!org && org !== activeOrgId;
}
