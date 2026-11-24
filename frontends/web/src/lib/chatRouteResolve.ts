// Copyright (c) 2025 Reliant Labs

/**
 * WHAT A CHAT URL OPENS: the loader of the chat routes (routes.tsx,
 * `/c/$chatId`, `/c/$chatId/t/$threadId`, both forms), per control-plane
 * docs/design/unified-shell-and-projects.md §3.4 (D8).
 *
 *   the chat, in its org        the chat; the stores follow (hooks/useChatRoute.ts)
 *   the chat, in another org    the resource's org wins: redirect (replace) to
 *                               the same URL in the chat's org, with a notice,
 *                               when the person is a member there; otherwise
 *                               not found. A chat is never shown under an org
 *                               it is not in.
 *   no such chat, or not theirs not found: the chat shell says so (ChatNotFound),
 *                               never a silent hop home
 *   GetChat failed otherwise    an error with a Retry, never "not found"
 *
 * The org-less form (`/c/<id>`) opens as it is with the flag `web.org_routes`
 * off; on, it redirects into the chat's org. The chat names its org
 * (`Chat.org_id`); a chat from before orgs has none and is its owner's
 * default org's. That is the rule `ResolveResourceOrg` (U4) will answer
 * server-side for every id-only link; until it ships the chat itself
 * carries the answer.
 *
 * Writes nothing a page reads (bar seeding the chat's detail cache): a load
 * can be superseded and never commit. A redirect into another org leaves its
 * notice ("This chat is in Side, so it opened there.") for the page it lands
 * on, shown when that page commits (lib/orgArrivalNotice.ts): for the
 * wrong-org form always, for the org-less form when it moved the tab's org.
 * It is the arrival's one notice: the generic "Switched to …" is not shown
 * beside it.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { redirect } from "@tanstack/react-router";

import type { Chat } from "@/api/client";
import * as chatActions from "@/hooks/chat-actions";
import { getChatFromCache, seedChatDetail } from "@/hooks/chat-queries";
import { noteResourceOrgRedirect } from "@/lib/orgArrivalNotice";
import { orgLinksEnabled } from "@/lib/orgRoutesFlag";
import { defaultOrgRoutingDeps, type LocationLike, type OrgRoutingDeps } from "@/lib/orgRouting";
import { inOrg, orgFreePath } from "@/lib/paths/org";
import type { OrgRouteState, RouteOrg } from "@/lib/routeOrg";
import type { MyOrg, MyOrgs } from "@/services/controlPlane/orgs";
import { isMachineCredentialUser } from "@/store/activeOrgStore";

/** What a chat URL resolved to: the chat routes' loader data. */
export type ChatRouteData =
  | { kind: "chat"; chat: Chat }
  /** No such chat, or not one the person may read (the server answers the same for both). */
  | { kind: "missing"; chatId: string }
  /** GetChat failed for another reason (offline, a server error). */
  | { kind: "error"; chatId: string; error: Error }
  /** Nobody signed in, or the org layout shows its own page: the guards handle it. */
  | { kind: "unresolved" };

export interface ChatRouteDeps extends OrgRoutingDeps {
  /** The chat, or null when the server says there is none for this person. Throws on any other failure. */
  getChat: (chatId: string) => Promise<Chat | null>;
}

/** Codes GetChat answers for a chat that is not there for this caller. */
const ABSENT = new Set([Code.NotFound, Code.PermissionDenied, Code.InvalidArgument]);

export const defaultChatRouteDeps: ChatRouteDeps = {
  ...defaultOrgRoutingDeps,
  getChat: async (chatId) => {
    const cached = getChatFromCache(chatId);
    if (cached) return cached;
    try {
      const chat = await chatActions.getChat(chatId);
      seedChatDetail(chat);
      return chat;
    } catch (error) {
      if (error instanceof ConnectError && ABSENT.has(error.code)) return null;
      throw error;
    }
  },
};

/** The notice for a chat URL redirected into the chat's org. */
export function movedChatNotice(orgName: string): string {
  return `This chat is in ${orgName}, so it opened there.`;
}

/** Redirect (replace) to `location`'s page in `org`, leaving the notice for the page it lands on. */
function redirectIntoOrg(org: MyOrg, location: LocationLike, { always }: { always: boolean }): never {
  const href = inOrgOf(org, location);
  noteResourceOrgRedirect(org.id, href, movedChatNotice(org.name || org.slug || org.id), { always });
  throw redirect({ href, replace: true });
}

function suffixOf(location: LocationLike): string {
  return `${location.searchStr ?? ""}${location.hash ? `#${location.hash}` : ""}`;
}

/** The org a chat is in: the one it names, else (a chat from before orgs) its owner's default. */
function chatOrg(chat: Chat, mine: MyOrgs): MyOrg | null {
  const id = chat.orgId || mine.defaultOrgId;
  return mine.orgs.find((org) => org.id === id) ?? null;
}

/** `location`'s page in `org`. */
function inOrgOf(org: MyOrg, location: LocationLike): string {
  return `${inOrg(org.slug || org.id, orgFreePath(location.pathname))}${suffixOf(location)}`;
}

/**
 * The chat routes' loader: see the module comment. `orgRoute` is the org
 * layout's answer (its context), absent for the org-less form. Throws the
 * redirects.
 */
export async function resolveChatRoute(
  chatId: string,
  location: LocationLike,
  { orgRoute, deps = defaultChatRouteDeps }: { orgRoute?: OrgRouteState; deps?: ChatRouteDeps } = {},
): Promise<ChatRouteData> {
  // Under an org the person is not in (or not signed in to), the layout renders its own page.
  if (orgRoute && orgRoute.kind !== "member") return { kind: "unresolved" };
  const routeOrg: RouteOrg | null = orgRoute?.kind === "member" ? orgRoute.org : null;

  const userId = await deps.signedInUser();
  if (!userId) return { kind: "unresolved" };

  let chat: Chat | null;
  try {
    chat = await deps.getChat(chatId);
  } catch (error) {
    return { kind: "error", chatId, error: error instanceof Error ? error : new Error(String(error)) };
  }
  if (!chat) return { kind: "missing", chatId };

  if (routeOrg) {
    let mine: MyOrgs;
    try {
      mine = await deps.listOrgs();
    } catch (error) {
      return { kind: "error", chatId, error: error instanceof Error ? error : new Error(String(error)) };
    }
    const org = chatOrg(chat, mine);
    // An unstamped chat of a person with no default org yet: the route's org is all there is.
    if (!chat.orgId && !mine.defaultOrgId) return { kind: "chat", chat };
    if (org?.id === routeOrg.id) return { kind: "chat", chat };
    // Readable, but in an org the person is not in: not under this one either.
    if (!org) return { kind: "missing", chatId };
    redirectIntoOrg(org, location, { always: true });
  }

  // The org-less form: into the chat's org once links carry the org.
  if (!orgLinksEnabled() || !deps.hasOrgs() || isMachineCredentialUser(userId)) return { kind: "chat", chat };
  let mine: MyOrgs;
  try {
    mine = await deps.listOrgs();
  } catch {
    // The org page would only fail the same way: open it where it is.
    return { kind: "chat", chat };
  }
  const org = chatOrg(chat, mine);
  if (!org) return { kind: "chat", chat };
  // Into another org than the tab's: said once the page commits.
  redirectIntoOrg(org, location, { always: false });
}
