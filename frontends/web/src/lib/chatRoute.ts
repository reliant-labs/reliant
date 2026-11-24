// Copyright (c) 2025 Reliant Labs

/**
 * WHICH CHAT THE URL OPENS (control-plane docs/design/unified-shell-and-projects.md
 * §3.1, §9 R1–R3): the chat shell's paths, read back.
 *
 *   /c/<chatId>                 a chat
 *   /c/<chatId>/t/<threadId>    one of its threads, in full view
 *   /new                        the new-chat view
 *   / and /project/<id>         the chat home: the remembered chat, if any
 *
 * each also under `/o/<org>` (read through orgFreePath). The URL is the
 * authority for the open chat and thread: the stores follow it
 * (hooks/useChatRoute.ts), and every "open this chat" navigates to it
 * (store/openChat.ts).
 *
 * Read from the pathname, never from route params: the router moves its
 * location the moment a navigation starts and commits the params only once
 * it has loaded (components/Layout/ProjectFilterRouteSync.tsx says why that
 * matters).
 */

import { chatPath, newChatPath, threadPath } from "@/lib/paths";
import { orgFreePath } from "@/lib/paths/org";
import { chatViewSearchSchema, type NewChatSearch } from "@/routeSchemas";
import { orgSegmentOf } from "@/store/activeOrgStore";

export type ChatShellRoute =
  /** `/` or `/project/<id>`: no chat named; the workspace restore picks one. */
  | { kind: "home" }
  /** `/new`: the new-chat view. */
  | { kind: "new" }
  /** `/c/<chatId>[/t/<threadId>]`. */
  | { kind: "chat"; chatId: string; threadId: string | null };

const CHAT = /^\/c\/([^/]+)(?:\/t\/([^/]+))?\/?$/;
const PROJECT = /^\/project\/[^/]+\/?$/;
const PROJECT_ID = /^\/project\/([^/]+)\/?$/;

function decode(segment: string): string {
  try {
    return decodeURIComponent(segment);
  } catch {
    return segment;
  }
}

/** The chat-shell page `pathname` (either form) is, or null for any other page. */
export function chatShellRouteOf(pathname: string): ChatShellRoute | null {
  const path = orgFreePath(pathname);
  if (path === "/" || PROJECT.test(path)) return { kind: "home" };
  if (path === "/new" || path === "/new/") return { kind: "new" };
  const chat = CHAT.exec(path);
  if (chat) return { kind: "chat", chatId: decode(chat[1]), threadId: chat[2] ? decode(chat[2]) : null };
  return null;
}

/** The org-less prefixes of the pages that name the open chat: a chat (and its threads), and the new-chat view. */
export const CHAT_SHELL_PREFIXES: readonly string[] = [chatPath("", { org: null }), newChatPath({ org: null })];

const MOBILE_CHAT = /^\/m\/chats\/([^/]+)\/?$/;

/**
 * The phone's own chat pages (`/m/chats/<id>`, `/m/new`): they name the open
 * chat too, but are not the desktop chat shell's pages, so chatShellRouteOf
 * (whose kinds drive the desktop's own chat navigation) does not read them.
 */
export function mobileChatRouteOf(pathname: string): { kind: "new" } | { kind: "chat"; chatId: string } | null {
  const path = orgFreePath(pathname);
  if (path === "/m/new" || path === "/m/new/") return { kind: "new" };
  const chat = MOBILE_CHAT.exec(path);
  return chat ? { kind: "chat", chatId: decode(chat[1]) } : null;
}

/** Whether `pathname` names the open chat itself: a chat, a thread, or the new-chat view (the phone's included). */
export function pathOwnsChat(pathname: string): boolean {
  const route = chatShellRouteOf(pathname);
  return route?.kind === "chat" || route?.kind === "new" || mobileChatRouteOf(pathname) !== null;
}

interface RouterLike {
  latestLocation?: { pathname: string; search?: unknown };
  state: { location: { pathname: string; search?: unknown }; status?: string };
  navigate: (options: { href?: string; to?: string; search?: unknown; replace?: boolean }) => unknown;
}

function router(): RouterLike | null {
  return (globalThis as { __RELIANT_ROUTER?: RouterLike }).__RELIANT_ROUTER ?? null;
}

/** The segment a link into `orgId` carries, when the tab knows it; else the page's own org (lib/paths/org.ts). */
function orgCtx(orgId: string | null | undefined): { org?: string } {
  const segment = orgId ? orgSegmentOf(orgId) : null;
  return segment ? { org: segment } : {};
}

/**
 * Put chat `chatId` (or one of its threads) in the URL: a push, so Back
 * returns to what was on screen, or a replace with `replace` (the app moved
 * on by itself: a chat archived from under the person, a draft that became
 * a chat). Nothing when the URL already names it. `orgId` is the chat's org
 * (`Chat.org_id`): the link goes there rather than to the page's org, so a
 * chat of another org never loads under this one first. Opening or closing
 * a thread of the chat on screen keeps its `view` and `panel`: only the
 * thread changed, not how the chat looks.
 *
 * Synchronous up to the router's own location: once this returns,
 * routeOwnsChat() and every reader of the latest location see the chat, so a
 * selection made right after it is the URL's, not one to follow
 * (lib/chatUrlFollow.ts).
 */
export function navigateToChatUrl(
  chatId: string,
  { orgId, threadId = null, replace = false }: { orgId?: string | null; threadId?: string | null; replace?: boolean } = {},
): void {
  const r = router();
  if (!r) return;
  const now = chatShellRouteOf(latestPathname());
  if (now?.kind === "chat" && now.chatId === chatId && now.threadId === threadId) return;
  const ctx = orgCtx(orgId);
  const path = threadId ? threadPath(chatId, threadId, ctx) : chatPath(chatId, ctx);
  // Only the thread changes: the chat keeps how it looks (its view and panel).
  const query = now?.kind === "chat" && now.chatId === chatId ? chatViewQuery(r) : "";
  void r.navigate({ href: path + query, replace });
}

/** The page's chat view state (`view`, `panel`), validated, as a query string ("" for none). */
function chatViewQuery(r: RouterLike): string {
  const parsed = chatViewSearchSchema.safeParse(r.latestLocation?.search ?? r.state.location.search ?? {});
  if (!parsed.success) return "";
  const params = new URLSearchParams();
  if (parsed.data.view) params.set("view", parsed.data.view);
  if (parsed.data.panel) params.set("panel", parsed.data.panel);
  const query = params.toString();
  return query ? `?${query}` : "";
}

/**
 * Show the new-chat view with its draft context in the URL (`/new?project=&machine=&ws=&app=`).
 * A push from anywhere else; a replace when the new-chat view is already on
 * screen (the project chip, the machine picker), so Back leaves it.
 */
export function navigateToNewChatUrl(search: NewChatSearch, { replace }: { replace?: boolean } = {}): void {
  const r = router();
  if (!r) return;
  const clean = Object.fromEntries(Object.entries(search).filter(([, value]) => value)) as NewChatSearch;
  const onNew = chatShellRouteOf(latestPathname())?.kind === "new";
  void r.navigate({ to: newChatPath(), search: clean, replace: replace ?? onNew });
}

/** The draft context the new-chat view last applied (store/openChat.ts applyNewChat). */
let appliedNewChat: NewChatSearch | null = null;
/** Settles once the view shows appliedNewChat: true, or false when its apply failed. */
let appliedNewChatShown: Promise<boolean> = Promise.resolve(true);

/** A stable key for a new-chat draft context, to compare with the one applied. */
export function newChatTargetKey(search: NewChatSearch): string {
  return JSON.stringify([search.project ?? null, search.machine ?? null, search.ws ?? null, search.app ?? null, search.with ?? null]);
}

/** `?with=`'s ids, in order, each once: [] for none. */
export function attachedProjectsOfParam(value: unknown): string[] {
  const raw = typeof value === "number" ? String(value) : value;
  if (typeof raw !== "string") return [];
  const out: string[] = [];
  for (const part of raw.split(",")) {
    const id = part.trim();
    if (id && !out.includes(id)) out.push(id);
  }
  return out;
}

/** The `?with=` value for `ids` (undefined: no param). */
export function attachedProjectsParam(ids: readonly string[]): string | undefined {
  return ids.length > 0 ? ids.join(",") : undefined;
}

/** Record `search` as the applied draft context; `shown` settles once the view shows it (default: it already does). */
export function markNewChatApplied(search: NewChatSearch, shown: Promise<boolean> = Promise.resolve(true)): void {
  appliedNewChat = { ...search };
  appliedNewChatShown = shown;
}

/** When the applied draft context is on screen (true), or its apply failed (false). */
export function newChatAppliedShown(): Promise<boolean> {
  return appliedNewChatShown;
}

export function newChatAppliedKey(): string | null {
  return appliedNewChat ? newChatTargetKey(appliedNewChat) : null;
}

/**
 * Record a choice made in the new-chat view (its machine, its workspace) in
 * `/new`'s search, with a replace, so a refresh keeps it. Nothing on any
 * other page: the home page shows the same view with no draft context of
 * its own.
 */
export function patchNewChatUrl(patch: NewChatSearch): void {
  const r = router();
  if (!r || chatShellRouteOf(latestPathname())?.kind !== "new") return;
  const current = (r.latestLocation?.search ?? r.state.location.search ?? {}) as Record<string, unknown>;
  const next = { ...current, ...patch } as Record<string, unknown>;
  if (Object.keys(patch).every((key) => current[key] === next[key])) return;
  for (const key of Object.keys(next)) if (next[key] === undefined || next[key] === "") delete next[key];
  // The view already shows it: the route has nothing to apply.
  if (appliedNewChat) appliedNewChat = { ...appliedNewChat, ...patch };
  void r.navigate({ to: newChatPath(), search: next, replace: true });
}

/** Where the tab is going: the router's latest location (ahead of its committed one), else the window's. */
export function latestPathname(): string {
  const r = router();
  if (r) return r.latestLocation?.pathname ?? r.state.location.pathname;
  return typeof window === "undefined" ? "/" : window.location.pathname;
}

/** Where the tab is going: the router's latest search (ahead of its committed one), else the window's. */
export function latestSearch(): Record<string, unknown> {
  const r = router();
  if (r) return (r.latestLocation?.search ?? r.state.location.search ?? {}) as Record<string, unknown>;
  return typeof window === "undefined" ? {} : Object.fromEntries(new URLSearchParams(window.location.search));
}

/**
 * The project the chat shell's URL names, if any: `/project/<id>`, or the
 * new-chat view's `?project=`. (A chat's URL names its chat; its project
 * follows once the chat is known.)
 */
export function routeProjectId(): string | null {
  const pathname = latestPathname();
  const route = chatShellRouteOf(pathname);
  if (route?.kind === "home") {
    const match = PROJECT_ID.exec(orgFreePath(pathname));
    return match ? decode(match[1]) : null;
  }
  if (route?.kind === "new") {
    const project = latestSearch().project;
    return typeof project === "string" && project ? project : null;
  }
  return null;
}

/**
 * Whether the URL, not a remembered selection, decides the open chat right
 * now. A restore (startup, a project switch, an Electron window's context)
 * checks this before it opens the chat it remembers: over `/c/<id>` or
 * `/new` it would show a chat the URL does not name.
 */
export function routeOwnsChat(): boolean {
  return pathOwnsChat(latestPathname());
}
