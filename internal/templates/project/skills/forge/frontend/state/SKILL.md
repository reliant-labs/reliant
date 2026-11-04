---
name: state
description: Frontend state management — choosing the right tool for each kind of state, ownership rules, and async handling in Forge frontends.
---

# Frontend State Management

## The decision table

Use the simplest scope that solves the problem:

| Kind of state | Tool | Example |
|---|---|---|
| Temporary UI for one component | `useState` | dropdown open, hover, form input |
| Avoiding middleman props | Component composition (slots) | pass `<Sidebar>` as a prop, not `project` through 5 layers |
| Stable app-wide dependencies | React Context | auth (`useAuth()`), event bus (`useEventBus()`), theme |
| Shared client UI state | Zustand store (`src/stores/`) | `sidebarCollapsed`, `commandPaletteOpen`, `activeModal` |
| Cross-page selection (survives reload) | Zustand `persist` store (`src/stores/`) | `currentOrgId`, `currentTeamId`, `currentWorkspaceId` |
| Server/backend data | Generated React Query hooks | workflows, users, runs — from `src/hooks/*-hooks.ts` |
| Real-time / streamed server data | React Query cache, patched via `setQueryData` from the stream | live chat messages, statuses, approvals pushed over a websocket/gRPC stream |
| Durable navigation | URL (route params, search params) | current project, selected run, active tab, filters |
| Imperative cross-cutting actions | Event bus (`src/lib/events.ts`) | `toast:show`, `navigate`, `auth:expired` |
| Persistent preferences | `localStorage` | sidebar width, theme preference |

Do not jump to global state because props feel annoying. A few props is fine.

## State vs events

**State** answers "what is true right now?" — `selectedNodeId`, `currentUser`, `sidebarCollapsed`.

**Events** answer "what just happened?" — `toast:show`, `auth:expired`, `workflow:runRequested`.

Never use events as your source of truth. Update state first, then emit events for side effects:

```typescript
uiStore.setState({ selectedNodeId: node.id });
events.emit("toast:show", { message: "Node selected", variant: "success" });
```

## Ownership

For every piece of state, answer:

- **Who owns it?** (one component, a store, the server, the URL)
- **Who can update it?** (only the owner, or via actions/mutations)
- **Should it survive refresh?** (URL or localStorage if yes)

## Async states

Every async operation has at least: `loading`, `error`, `success`, `empty`. Generated hooks provide these via React Query — always handle all states:

```tsx
import { userMessage } from "@reliantlabs/forge-web-runtime";

if (query.isLoading) return <SkeletonLoader />;
if (query.isError) return <AlertBanner variant="error" message={userMessage(query.error)} />;
if (!query.data?.length) return <EmptyState />;
return <DataList items={query.data} />;
```

`SkeletonLoader` and `Status` pulses stop under `prefers-reduced-motion`; loading spinners slow instead of freezing. Add `motion-reduce:` to any `animate-*` you write.

A generated hook's `error` is a `ConnectClientError`, so `userMessage(err)`
renders it without the backend framing, and `err.reason` / `err.codeName` are
there when a failure needs its own branch. Never render `err.message` and
never `switch` on its text — see the `frontend-runtime` skill.

## Real-time server data (streams, websockets, subscriptions)

When the backend *pushes* updates — a websocket, a gRPC/Connect server
stream, SSE — the data is still **server data**, so it still belongs in
the React Query cache, not copied into a Zustand store. The only question
is how a pushed event reaches the cache. There are two tools, and picking
the wrong one is the most common real-time mistake:

- **`queryClient.setQueryData(key, updater)` — push the new value straight
  into the cache.** Every hook subscribed to that key re-renders
  immediately, with **no network round-trip**. Use this when the event
  payload already contains the new data (which is the whole point of a
  push stream). This is the fast, correct default for real-time.
- **`queryClient.invalidateQueries({ queryKey })` — mark stale and
  refetch.** This fires a network request. Use it only when you *don't*
  have the new value in hand (e.g. the event is a bare "something changed"
  nudge), or when a refetch is genuinely cheap and simpler than
  reconstructing the value.

> **Do not turn a push stream into a poller.** Calling `invalidateQueries`
> on every stream event refetches over the network for data the server
> already handed you — the latency and load pattern of polling, defeating
> the reason you have a stream. Reach for `invalidateQueries` on stream
> events only when you truly lack the payload; otherwise patch.

Keep the write path in the query module next to the hooks, so components
never touch the cache directly:

```typescript
// src/hooks/chat-queries.ts
export function patchChatInCache(chatId: string, patch: Partial<Chat>) {
  // setQueryData, never invalidate — no refetch round-trip on a live event.
  queryClient.setQueryData(
    chatKeys.detail(chatId),
    (prev: Chat | undefined) => (prev ? { ...prev, ...patch } : prev),
  );
}
```

```typescript
// stream handler (Zustand store, event bus, or effect that owns the socket)
socket.on("chat.updated", (evt) => {
  patchChatInCache(evt.chatId, evt.patch); // fast, no refetch
});
```

**Query-owned vs stream-owned caches.** Decide which channel is
authoritative for each cache, because it flips two rules:

- **Query-owned** (the fetch is authoritative; stream events are
  best-effort patches — e.g. an entity detail page): patch
  *defensively* — if the cache entry is absent (`prev === undefined`),
  return it unchanged; don't fabricate a partial entity. Default
  staleness tuning applies.
- **Stream-owned** (the stream is the authoritative update channel; the
  `queryFn` is only a cold-start seed — e.g. a live message list): patch
  must *seed* (`prev ?? empty`) so an event arriving before the first
  fetch isn't lost, and set `staleTime: Infinity` — here that is a
  **correctness requirement, not a tuning choice**: a background refetch
  would overwrite the stream's merged state with a raw server snapshot.

**Every request of one generated read** — each filter a list was opened
with, plus its `<rpc>Infinite` read — is one prefix:
`<svc>Keys.<rpc>.all`, i.e. `[service, entity?, method]`. Patch or
invalidate through it, never through a predicate over key positions:

```typescript
queryClient.setQueriesData(
  { queryKey: taskServiceKeys.listTasks.all },
  (prev: unknown) => patchListOrPages(prev, evt.task), // a response OR { pages }
);
```

`query.queryKey[1] === "listTasks"` breaks silently the day a
`// forge:query-scope` marker inserts the entity at index 1. For a paged
read the prefix covers both cache shapes (the response and the infinite
read's `{ pages, pageParams }`), so its `setQueriesData` updater must handle
both.

If the `queryFn` returns an envelope (`{ items, total, ... }`), the cache
stores the envelope: patch *inside* it and remember `select` runs on
read, not write — patching the bare array into an envelope key is a
silent no-op for subscribers.

Rules for real-time data:

- **The cache is the single source of truth.** Do **not** also mirror the
  same records into a Zustand store and hand-sync them on each event — that
  is the "copy backend data into Zustand" anti-pattern, and every event
  then has to update two places. The stream patches the cache; components
  read the cache through the normal hooks.
- **Patch, don't refetch,** when the event carries the data (see above).
  And never both: patching and *then* invalidating the same key on the
  same event refetches data you just wrote — the poller anti-pattern
  wearing a patch as a disguise.
- **Don't issue cache patches from inside a store updater.** If a Zustand
  reducer coordinates stream handling, compute the next state inside
  `set()`, commit it, then apply `setQueryData`/other-store writes *after*
  the commit (same synchronous task — React still batches the renders).
  Patching mid-updater notifies query subscribers while your own store's
  commit is still in flight.
- **Wire eviction to entity lifecycle, not just logout.** A stream-owned
  cache with `gcTime: Infinity` retains every entity until something
  removes it. Deleting/archiving an entity must evict its cache entries
  (and any per-entity client slices), or long-lived sessions leak.
- **Genuinely ephemeral in-flight state** — a partial message still being
  streamed token-by-token, not yet a persisted record — is the one piece
  that can live in a small Zustand slice, and the render layer composes it
  over the cached persisted list. Once it finalizes, it's server data and
  belongs to the cache like everything else.

## What the scaffold provides

- **React Query hooks** (`src/hooks/*-hooks.ts`) — generated, handle server state
- **Auth context** (`useAuth()`) — the server-held session, read as an identity
- **Event bus** (`src/lib/events.ts`) — typed, extensible, for imperative actions
- **UI store** (`src/stores/ui-store.ts`) — Zustand baseline, extend for your domain
- **URL state** — App Router params and `useSearchParams` for navigation state

## Cross-page selection state

A specific shape of shared client state that comes up in almost every
admin-style frontend: **"the user picked an org / team / workspace on
one page, and every other page needs to know which one is selected."**

This is NOT server data (the org list is, but the *current selection*
isn't). It's NOT URL state (it persists across navigations and reloads,
not part of the URL). It's NOT auth (the user might have access to many
orgs and switch between them). It's shared client state with one extra
requirement: **it must survive a hard reload** so the page the user
lands on after refresh is the page they were looking at.

Use a small Zustand store with `persist` middleware:

```typescript
// src/stores/org-store.ts
import { create } from "zustand";
import { persist } from "zustand/middleware";

interface OrgState {
  currentOrgId: string | null;
  currentOrgName: string | null;
  setCurrentOrg: (id: string, name: string) => void;
  clearCurrentOrg: () => void;
}

export const useOrgStore = create<OrgState>()(
  persist(
    (set) => ({
      currentOrgId: null,
      currentOrgName: null,
      setCurrentOrg: (id, name) =>
        set({ currentOrgId: id, currentOrgName: name }),
      clearCurrentOrg: () =>
        set({ currentOrgId: null, currentOrgName: null }),
    }),
    { name: "<project>-org" }, // localStorage key — scope per project
  ),
);
```

Use it from any page as a slice subscription:

```tsx
const orgId = useOrgStore((s) => s.currentOrgId);
const setCurrentOrg = useOrgStore((s) => s.setCurrentOrg);
```

Conventions:

- **`name`** the localStorage key. Persistence is scoped to the
  origin/basePath; multiple forge apps on the same domain need
  different `name`s.
- **Persist only IDs and display labels**, never full server objects.
  Re-fetch the rich record via a React Query hook (`useGetOrg(orgId)`)
  so the cache stays the single source of truth.
- **Reset on logout.** Wire `clearCurrentOrg()` into your auth
  `logout()` path so the next user doesn't inherit the previous user's
  selection.
- **Don't use this for URL-shaped selections.** Selected row in a list,
  active tab, filters — those belong in `useSearchParams`. The persisted
  store is for selections that travel with the user across the entire
  app.

The same pattern fits `currentTeamId`, `currentWorkspaceId`,
`currentProjectId`, etc. One store per orthogonal selection axis; don't
pile them into the base `ui-store` (the UI store should stay
non-persisted client UI state).

## Auth state

Auth is **owned scaffold, born with every frontend** — there is no
`forge.yaml` key that turns it on and nothing to install.

- Sign-in is NATIVE: the browser POSTs credentials to this app's own API
  (`src/lib/auth/native-login.ts`) and the server runs the OIDC flow, returning
  an HttpOnly session cookie. `useAuth()` reads the resulting identity from
  `GET /auth/session`.
- The base `EventMap` already includes `auth:expired`, `auth:login`, `auth:logout`
- Browser frontends also ship `src/components/session_nav.tsx`, which reads
  `useAuth()`. Never mirror the identity into a second store: the cookie the
  requests ride on is unreadable to script, so a divergent copy authenticates
  the UI and not the requests.
- Put auth *UI* state (modal open, last error) in its own
  `src/stores/auth-store.ts` — never mirror the identity into it,
  `useAuth()` stays the source of truth:
  ```typescript
  import { create } from "zustand";

  interface AuthUiState {
    showLoginModal: boolean;
    setShowLoginModal: (show: boolean) => void;
    lastAuthError: string | null;
    setLastAuthError: (error: string | null) => void;
  }

  export const useAuthUiStore = create<AuthUiState>((set) => ({
    showLoginModal: false,
    setShowLoginModal: (show) => set({ showLoginModal: show }),
    lastAuthError: null,
    setLastAuthError: (error) => set({ lastAuthError: error }),
  }));
  ```

### Network events
- `network:error` and `network:unauthorized` are emitted by the Connect interceptors
- Listen for `network:unauthorized` to trigger auth refresh flows:
  ```typescript
  useEvent("network:unauthorized", () => {
    // Trigger token refresh or redirect to login
    events.emit("auth:expired");
  });
  ```

## Rules

- Use generated hooks for server data — do not copy backend data into Zustand.
- Real-time server data belongs in the React Query cache too: patch it with `setQueryData` from the stream, don't refetch-on-event and don't shadow it in Zustand.
- Subscribe to Zustand slices, not the whole store: `useUiStore(s => s.sidebarCollapsed)`.
- Derive values during render — do not `useEffect` to set derived state.
- Extend the base UI store or create domain stores in `src/stores/` — do not create one giant global store.

## Sub-skills

Load the parent `frontend` skill for project structure, hooks, and dev workflow.
