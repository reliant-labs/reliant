---
name: proto-hooks
description: The React Query hooks forge generates from a service proto — read vs write classification, forge:query-scope, paged use<Rpc>Infinite, and <svc>Queries / <svc>Mutations for reads and writes outside React.
---

# The generated frontend hooks

`forge generate` writes one hooks file per service (`src/hooks/<svc>-service-hooks_gen.ts`).
Every rpc gets a hook; how it is classified and keyed is decided by the proto.

**Reads get `useQuery`.** An rpc is a read when it declares protobuf's
`option idempotency_level = NO_SIDE_EFFECTS;`, else when its name opens with a
read verb (Get, List, Search, Find, Check, Has, Is, Count, Exists). Declare it
on every read named as a noun (`Health`, `Version`) instead of hand-building
its query key; `IDEMPOTENT` does not make a read. `// forge:mutation` forces a
mutation. Mutation hooks infer `onMutate`'s return as `onError`'s `context`,
so an optimistic rollback needs no cast. A query hook's `select` sets its
`data` type, as in useQuery: `useGetTask(req, { select: toView })` — no
`useMemo` over `data`. A flip leaves the owned starter test's row stale:
`forge generate` prints its line and the new body, and
`forge lint --conventions` fails until it is fixed.

**Scope a custom read with `// forge:query-scope <Entity>`.** A CRUD read is
keyed under its entity, so that entity's CRUD mutations refresh it; a custom
read sits at service scope, which only `<svc>Keys.all` reaches. Above a read
of runs, `// forge:query-scope Run` keys it under `<svc>Keys.run`. Name the
message exactly (a CRUD entity or a message the service declares); it is
declared, not inferred from a `run_id` field, because a filter field is not
what the read reads. Reads only: generate refuses it on a mutation.
`<svc>Keys.<rpc>.all` matches every request of one read, infinite included:
`invalidateQueries({ queryKey: taskServiceKeys.listTasks.all })`. Never test
`queryKey[1]`; a scope moves the method to index 2.

**Paged reads also get `use<Rpc>Infinite`.** A read whose request has a
singular `string page_token` and whose response has `string next_page_token`
(the List shape in `proto`, whatever the rpc is called) gets a useInfiniteQuery
hook too. The shape is the opt-in; there is no marker. It takes the request
minus `pageToken` (the hook owns the token; empty ends the listing), keys
under `<svc>Keys.<rpc>Infinite(req)` within the service and entity scopes so
mutations invalidate it, and `select` reshapes `{ pages }`:
`useListTasksInfinite({ pageSize: 50 }, { select: (d) => d.pages.flatMap((p) => p.tasks) })`.
Never hand-write `useInfiniteQuery` over a generated key.

**Outside React, use `<svc>Queries`.** Each read's TanStack options:
`<svc>Queries.<rpc>(req, options?)` is `queryOptions`, `.<rpc>Infinite(req)`
`infiniteQueryOptions`; the hooks are built over them, so they share one cache
entry and fetch. `await queryClient.fetchQuery(taskServiceQueries.getTask({ id }))`
(also `ensureQueryData`, `prefetchQuery`, `fetchInfiniteQuery`,
`useQueries({ queries })`), typed with no annotation. Never hand-write
`{ queryKey, queryFn }` for a generated rpc.

**Writes outside React use `<svc>Mutations`.** A store, sync service or OAuth
callback calls `await taskServiceMutations(queryClient).createTask(req, { signal })`:
the rpc, then on success the same invalidation `useCreateTask` does (entity
scope for CRUD, `<svc>Keys.all` otherwise). It resolves with the response,
rejects with a `ConnectClientError`, and bypasses the MutationCache, so no
error toast fires. Never hand-write a client call plus `invalidateQueries`.
