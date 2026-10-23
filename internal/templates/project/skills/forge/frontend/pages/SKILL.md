---
name: pages
description: Cookbook — scaffold a CRUD page with React Query hooks + Zustand state + forge component library + Tailwind v4.
---

# Frontend Pages Cookbook

End-to-end recipe for adding a typed CRUD page to a forge frontend. Follow it linearly; every step has a "see also" pointing at deeper material.

The example walks through adding `app/users/page.tsx` for a hypothetical `UserService`. Substitute your domain — the recipe is mechanical.

## Step 1 — scaffold the entity (migration + CRUD RPCs)

The schema lives in `db/migrations/`, not in proto — there is no
`(forge.v1.entity)` annotation (those are retired and ignored). Declare the
message under a leading `// forge:entity` comment and run `forge scaffold`,
which writes the create-table migration and scaffolds the CRUD messages +
RPCs in one step. An entity is a table plus matching CRUD RPCs;
`forge generate` projects the entity struct, ORM, and these pages from the
applied schema.

The resulting `proto/services/users/v1/users.proto` is a plain message
plus the CRUD service (no field annotations needed — columns are read off
the migration):

```proto
syntax = "proto3";
package services.users.v1;

import "forge/v1/forge.proto";
import "google/protobuf/timestamp.proto";

message User {
  string id = 1;
  string org_id = 2;
  string email = 3;
  string name = 4;
  google.protobuf.Timestamp created_at = 100;
}

service UserService {
  option (forge.v1.service) = { name: "users" version: "v1" };

  rpc CreateUser(CreateUserRequest) returns (CreateUserResponse) {
    option (forge.v1.method) = { auth_required: true };
  }
  rpc ListUsers(ListUsersRequest) returns (ListUsersResponse) {
    option (forge.v1.method) = { auth_required: true };
  }
  rpc GetUser(GetUserRequest)   returns (GetUserResponse)   { option (forge.v1.method) = { auth_required: true }; }
  rpc UpdateUser(UpdateUserRequest) returns (UpdateUserResponse) { option (forge.v1.method) = { auth_required: true }; }
  rpc DeleteUser(DeleteUserRequest) returns (DeleteUserResponse) { option (forge.v1.method) = { auth_required: true }; }
}

message CreateUserRequest  { string email = 1; string name = 2; }
message CreateUserResponse { User user = 1; }
message ListUsersRequest   { int32 page_size = 1; string page_token = 2; optional string search = 3; }
message ListUsersResponse  { repeated User users = 1; string next_page_token = 2; }
message GetUserRequest     { string id = 1; }
message GetUserResponse    { User user = 1; }
message UpdateUserRequest  { string id = 1; string name = 2; }
message UpdateUserResponse { User user = 1; }
message DeleteUserRequest  { string id = 1; }
message DeleteUserResponse {}
```

See `proto` for annotation rules and the full reference. `forge lint --conventions` enforces the structural rules.

## Step 2 — generate

```bash
forge generate
```

This produces:

- Go service stubs and CRUD handler wiring (`handlers_crud_ops_gen.go` op constructors plus thin delegations in the user-owned `handlers_crud.go` for matching method names — see `api`).
- Generated React Query hooks: `useListUsers`, `useGetUser`, `useCreateUser`, `useUpdateUser`, `useDeleteUser`. They are emitted per PROTO SERVICE, not per entity — `src/hooks/<service>-service-hooks_gen.ts` — so import from the barrel (`@/hooks`), which re-exports every generated hook file.
- Generated Connect transport in `src/lib/connect.ts`.

Never hand-edit `*-hooks.ts` — overwritten on next `forge generate`. Add custom hooks in a separate file (`src/hooks/custom-hooks.ts`).

## Step 3 — build the page

`data_table` is in the component library but is NOT one of the components the
scaffold installs — run `forge component install data_table` first, or reach
for `<Resource>` from `@reliantlabs/forge-web-runtime` (see `frontend-runtime`).

```tsx
// frontends/web/src/app/users/page.tsx
"use client";

import { useState } from "react";

import {
  useListUsers,
  useCreateUser,
  useDeleteUser,
} from "@/hooks";
import { emitToast } from "@/lib/events";

import DataTable from "@/components/ui/data_table";
import Button from "@/components/ui/button";
import Card from "@/components/ui/card";
import Input from "@/components/ui/input";

export default function UsersPage() {
  const [search, setSearch] = useState("");

  const { data, isLoading, error } = useListUsers({
    pageSize: 20,
    search: search || undefined,
  });
  const createUser = useCreateUser();
  const deleteUser = useDeleteUser();

  if (isLoading) return <div className="p-8">Loading…</div>;
  if (error)    return <div className="p-8 text-red-600">Error: {userMessage(error)}</div>;

  return (
    <div className="p-8 space-y-6">
      <header className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold">Users</h1>
        <Button
          onClick={async () => {
            // Mutation hooks take the plain request INIT object, not a
            // create()d message. Errors already surface through the
            // app-wide MutationCache toast — only success is yours.
            await createUser.mutateAsync({
              email: "new@example.com",
              name:  "New User",
            });
            emitToast({ variant: "success", message: "User created" });
          }}
        >
          New user
        </Button>
      </header>

      <Card className="p-4">
        <Input
          placeholder="Search by name…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </Card>

      <DataTable
        columns={[
          { key: "name",  header: "Name" },
          { key: "email", header: "Email" },
          {
            key: "actions",
            header: "",
            render: (u) => (
              <Button
                variant="ghost"
                onClick={() => deleteUser.mutate({ id: u.id })}
              >
                Delete
              </Button>
            ),
          },
        ]}
        data={data?.users ?? []}
      />
    </div>
  );
}
```

A few patterns to copy:

- **`"use client"` only when you need it.** This page uses hooks and event handlers, so client.
- **Hooks take a plain init object**, not a constructed message (`mutate({ name })`, not `mutate(create(Schema, { name }))`). Reach for `create(Schema, {...})` only when you need an actual message value — and never `new MessageType(...)`, which is protobuf-es v1.
- **`useListUsers({ pageSize, search })` direct.** The hook takes a plain object; pass the search filter as `undefined` (not empty string) when not set.
- **Three states: loading / error / success.** Always.
- **Forge component library first.** Use `component_library(action="search", query="...")` before hand-rolling. See `frontend`.

## Step 3b — enum fields and foreign-key fields

Two field kinds have a shared component already; both are the ones hand-rolled pages get wrong.

**Proto enums are NUMBERS at runtime.** protobuf-es v2 emits a numeric TS enum, so `order.status === 1`, not `"PENDING"`. Hand the raw field plus the enum OBJECT to the shared components and they reverse-map it:

```tsx
import { StatusBadge } from "@/components/status-badge";
import { EnumSelect } from "@/components/enum-select";
import { OrderStatus } from "@/gen/services/orders/v1/orders_pb";

<StatusBadge value={order.status} enumType={OrderStatus} />   {/* "Pending" */}
<EnumSelect enumObject={OrderStatus} {...register("status")} />
```

Passing the type NAME (`enumType="OrderStatus"`) instead of the object cannot reverse an ordinal — the badge renders its unset state rather than the digit. The name form exists only for plain string columns storing `ORDER_STATUS_…`. Register domain colors once with `registerStatusVariants({ payment_captured: "success" })`; never edit the badge.

**A foreign-key field means "search another entity and pick one"** — never a raw text input for a UUID, and never a `<select>` over a preloaded array (the other table is paged and filtered server-side). Drive `<EntityPicker>` with the generated LIST hook, and resolve an id back to a name with `<EntityName>` and the generated GET hook:

```tsx
<Controller
  control={control}
  name="patientId"
  render={({ field }) => (
    <EntityPicker
      useList={useListPatients}
      buildRequest={(search) => ({ pageSize: 20, search: search || undefined })}
      itemsOf={(res) => res.patients}
      hasMoreOf={(res) => Boolean(res.nextPageToken)}
      optionValue={(p) => p.id}
      optionLabel={(p) => p.fullName}
      optionHint={(p) => p.email}
      value={field.value}
      onChange={(id) => field.onChange(id ?? "")}
      aria-label="Patient"
      selectedLabel={
        <EntityName id={field.value} useGet={useGetPatient}
          buildRequest={(id) => ({ id })} nameOf={(res) => res.patient?.fullName} />
      }
    />
  )}
/>
```

It is a custom control, so `<Controller>` — `register()` has nothing to attach to. The picker owns the debounce, popover, keyboard nav and loading/error/empty ladder; it queries only while open, and fetches ONE page (search narrows; `hasMoreOf` renders the "keep typing" hint). Do NOT write a `<PatientPicker>` in `_components/` — extend or restyle these instead, and report a genuinely missing shared component rather than forking one per entity.

## Step 3c — the field rules the scaffolded forms already follow

The born create/edit pages derive each field's zod line from the proto. Keep these rules when you rewrite a form. Breaking them reproduces bugs that have already shipped once:

- **Required means a rule says so.** A field is required (`*`, a length floor in zod) only when it has protovalidate `required = true` or `string.min_len >= 1`, or when it is a non-optional foreign key, whose column is `NOT NULL REFERENCES` with no DEFAULT. A plain `string notes` is born `NOT NULL DEFAULT ''`, so `""` is a valid value. To make a field mandatory, add the rule to the proto. It then applies on the wire and in the DB CHECK as well as in the form.
- **An empty `optional` field is unset, not zero.** For a proto3 `optional` scalar, zod maps `""` to `undefined` (`z.preprocess((v) => (v === "" ? undefined : v), z.coerce.number().optional())`), so the request omits it and the column stays NULL. Without this, `z.coerce.number()` turns an empty `optional int32 year` into year 0. On edit, keep the field in `update_mask`: a masked path whose value is unset is written as NULL, which is how a user clears a stored value.
- **The enum zero is never a choice.** `*_UNSPECIFIED` (wire number 0) is how the wire says "unset". The born CHECK rejects it, so form selects and list filters list only real members. A `NOT NULL` enum's create select starts on the first real member, which is the column DEFAULT. An `optional` enum adds a None option that submits as unset.
- **Create lands on the new record:** `onSuccess: ({ order: created }) => router.push(created ? entityViewHref("orders", created.id) : "/orders")`.

## Step 4 — Zustand for client state, if needed

For state that lives only on the client (sidebar collapse, modal open, toast queue), extend an existing store or create a domain store in `src/stores/`:

```ts
// src/stores/users-store.ts
import { create } from "zustand";

interface UsersUiState {
  selectedIds: string[];
  toggleSelect: (id: string) => void;
  clearSelection: () => void;
}

export const useUsersUiStore = create<UsersUiState>((set) => ({
  selectedIds: [],
  toggleSelect: (id) =>
    set((s) => ({
      selectedIds: s.selectedIds.includes(id)
        ? s.selectedIds.filter((x) => x !== id)
        : [...s.selectedIds, id],
    })),
  clearSelection: () => set({ selectedIds: [] }),
}));
```

In the component, **subscribe to slices, not the whole store**, so re-renders stay tight:

```ts
const selectedIds  = useUsersUiStore((s) => s.selectedIds);
const toggleSelect = useUsersUiStore((s) => s.toggleSelect);
```

Server data does NOT belong in Zustand. The `data` from `useListUsers` is already cached by React Query — copying it into a store creates a stale-data bug waiting to happen. See `frontend/state` for the full ownership table.

## Step 5 — styling with Tailwind v4

Use utility classes directly. The project ships Tailwind v4, configured via `@theme` in `src/app/globals.css` — no `tailwind.config.js`. See `frontend` for v4 specifics, including the `@import "tailwindcss"` rule and `@tailwindcss/postcss` plugin.

For the tokens you'll repeat — colors, spacing, radii — extend the `@theme` block. Do NOT hard-code one-off colors across multiple components.

## Step 6 — wire the route

Next.js App Router auto-routes anything under `src/app/`. Adding `src/app/users/page.tsx` creates `/users`. To gate the page behind auth, the auth provider in `src/lib/auth/` already runs at layout level — components can read `useAuth()` for current user / token.

**Every route is static.** A frontend is a static export by default (`output: static`). An export contains only pages it can enumerate at build time, and a row's id exists only at runtime, so **never add a `[id]` segment**. `next build` fails with `missing "generateStaticParams()"`. The generated pages put the id in the query string instead:

| Route | File | URL |
|---|---|---|
| list | `src/app/users/page.tsx` | `/users` |
| create | `src/app/users/new/page.tsx` | `/users/new` |
| detail | `src/app/users/view/page.tsx` | `/users/view?id=<id>` |
| edit | `src/app/users/edit/page.tsx` | `/users/edit?id=<id>` |

Build and read those URLs with `src/lib/entity-routes.ts`. Do not hand-roll the query string:

```tsx
import { entityEditHref, entityViewHref, useEntityIdParam } from "@/lib/entity-routes";

router.push(entityViewHref("users", user.id));     // "/users/view?id=…"
<Link href={entityEditHref("users", id)}>Edit</Link>

// In the page: useEntityIdParam() calls useSearchParams, so render the
// component that calls it inside <Suspense>. Without the boundary the export fails.
export default function UserDetailPage() {
  return <Suspense fallback={<SkeletonLoader />}><UserDetailRoute /></Suspense>;
}
function UserDetailRoute() {
  const id = useEntityIdParam();   // undefined when ?id= is missing
  if (id === undefined) return <MissingIdState />;
  return <UserDetail id={id} />;
}
```

Hand-written pages that take a parameter follow the same rule. An entity nested under another reads two query parameters (`/orders/lines?order=…`), not two segments. A frontend set to `output: standalone` can use dynamic segments, but then it cannot ship on hosted static hosting.

For navigation links, use the project's existing nav component (likely in `src/components/layout/`).

## Step 7 — verify visually

```bash
forge env up dev
```

Open the page in a browser. Use Chrome DevTools' MCP integration:

```
take_snapshot()      # element tree, accessibility
take_screenshot()    # actual rendered pixels
```

**Both.** Snapshots miss CSS bugs (overflow, z-index, broken responsive). Screenshots miss accessibility regressions. Run both at every breakpoint you support.

## Common mistakes

1. **`new CreateUserRequest({...})`** — protobuf-es v1 syntax. Hooks want a plain object; `create(Schema, {...})` when you need a message.
2. **Copying server data into Zustand.** React Query already caches it. Subscribe to the query, don't duplicate it.
3. **Using `useState` for form state on a non-trivial form.** Use `react-hook-form` + Zod schema. See `frontend/patterns`.
4. **Forgetting `"use client"`** on a page that uses hooks. Build error.
5. **Hand-editing a generated `*-hooks.ts`.** Overwritten on next `forge generate`.
6. **Skipping screenshots.** Snapshots compile, tests pass, layout is broken in the browser.
7. **Leaving a scaffolded form on the raw CRUD RPC when a domain verb owns the operation.** The generated create page wires `Create<Entity>`: the generator cannot tell whether `IssuePrescription` creates a row or transitions one, so it never guesses. You can. Point the form at the domain RPC when one exists — that is where the invariants and side effects live. Rewire the mutation hook only. To stop the *edit* page writing a column a domain RPC owns, declare it in the proto with `// forge:guards <table>.<column>` on that RPC's request (see `proto`): the column then leaves the scaffolded `update_mask` and renders as a disabled row naming the RPC, instead of being written raw and tripping whatever CHECK the RPC exists to enforce. Note that pages are scaffolded **once** — a page that predates the marker keeps writing the column, and `forge lint --guarded-fields` is what finds it.
8. **Adding a `src/app/<entity>/[id]/` route.** `npm run build` refuses a dynamic segment in a static export. Use `/<entity>/view?id=…` (Step 6). A project scaffolded before forge v0.1.44 still has `[id]` pages. `forge skill load migrations/v0.1.44` converts them.
9. **Hand-rolling mutation error toasts.** Mutation failures already surface through the app-wide chokepoint (`MutationCache.onError` in `src/lib/query-client.ts`). If your page renders the error inline (form banner), pass `meta: { silenceErrorToast: true }` to the mutation so the failure isn't announced twice — and show users `userMessage(err)` (from `@reliantlabs/forge-web-runtime`), never raw `err.message`.

## Rules

- Generated hooks (`use<Method>`) come from `forge generate`. Never hand-edit `*-hooks.ts`.
- Three states (loading, error, success) on every data-fetching page.
- Server data lives in React Query; client UI state lives in Zustand. Never mix.
- Subscribe to Zustand slices, not the whole store.
- Component library before custom UI. `component_library(action="search", ...)`.
- Verify visually with both `take_snapshot` and `take_screenshot` before declaring done.
- Tailwind v4 only — no `tailwind.config.js`, no `@tailwind base/components/utilities`.

## When this skill is not enough

- **State management decision tree** (URL vs Zustand vs query vs ref) — see `frontend/state`.
- **Component composition / container-presentational patterns** — see `frontend/patterns`.
- **The proto and codegen side** of the recipe — see `proto` and `api`.
- **Auth wiring on the frontend** (Auth0, Clerk, Supabase) — see `auth` (auth components are owned scaffold under `frontends/<name>/src/`).
- **Mobile (React Native / Expo)** — see `frontend`. The hook layer is shared; the UI patterns differ.
