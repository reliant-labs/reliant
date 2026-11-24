// Copyright (c) 2026 Reliant Labs

/**
 * THE PHONE'S SCOPE IN THE URL (owner, 2026-11-24: the URL is the only source
 * of view state; the stores follow it, and a pick only navigates).
 *
 * The phone's screens read the params the desktop shell does
 * (lib/urlState/shellScope.ts), which the root route carries on every
 * navigation that does not name them:
 *
 *   `?projects=<id>|all`    which project's chats the list and search show
 *                           (one project, else every project)
 *   `/m/new?project=<id>`   the project a new chat starts in, as on desktop
 *                           `/new?project=`; it wins over `projects`, which
 *                           stays for Back to the list
 *
 * So a reload, Back and a shared link show what the screen showed. The
 * stores follow the URL in one place, MobileScopeSync.
 */

import { useSearch } from "@tanstack/react-router";

import { oneProjectOf } from "@/lib/urlState/shellScopeStore";
import { scopeOfSearch, type ShellScope } from "@/lib/urlState/shellScope";
import type { SearchRecord } from "@/lib/urlState/searchParams";

/** The one project the chat list shows, else null (every project: `all`, several, or none named). */
export function chatScopeOfSearch(search: SearchRecord | null | undefined): string | null {
  const scope = scopeOfSearch(search);
  return scope ? oneProjectOf(scope) : null;
}

/** Re-renders with the one project the URL scopes the chat list to (null: every project). */
export function useMobileChatScope(): string | null {
  return chatScopeOfSearch(useSearch({ strict: false }) as SearchRecord);
}

/** `/m/new`'s `?project=`, if it names one (a lone numeric id arrives JSON-parsed). */
export function newChatProjectParam(search: SearchRecord | null | undefined): string | undefined {
  const raw = search?.project;
  const value = typeof raw === "number" ? String(raw) : raw;
  return typeof value === "string" && value ? value : undefined;
}

/**
 * The project a new chat starts in: `?project=`, else the URL scope's one
 * project, else the context project. Once the projects are listed, a URL's
 * that is not (removed, another org's) is passed over.
 */
export function newChatProjectOf({
  project,
  scope,
  contextProjectId,
  listed,
}: {
  project: string | undefined;
  scope: ShellScope | undefined;
  contextProjectId: string | null | undefined;
  /** The projects listed, once loaded. */
  listed: ReadonlySet<string> | undefined;
}): string | null {
  for (const id of [project, scope ? oneProjectOf(scope) : null]) {
    if (id && (!listed || listed.has(id))) return id;
  }
  return contextProjectId ?? null;
}
