---
name: frontend/components
description: The base UI primitives every forge frontend ships under src/components/ui/ (button, input/label/form with FormField auto-binding, card, status/tag/mono, tabs, table, select, the toast stack), the other auto-installed components, the Badge and Modal aliases, and the owned higher-level components (status-badge, enum-select, entity-picker/entity-name, session_nav).
---

## Base UI primitives (always available)

Every frontend ships low-level primitives under `src/components/ui/`. Compose these instead of inlining `<button>`/`<input>`/`<table>` markup:

| Primitive | What it is |
|-----------|-----------|
| `button` | `primary`/`secondary`/`outline`/`ghost`/`danger`, `xs`–`xl`, `leftIcon`/`rightIcon`, icon-only, `loading`, `href` |
| `input`, `label`, `form` | Text input, field label, and `Form`/`FormField`/`FormError`/`FormActions`. `<FormField>` mints an id via `useId()`, so child `<Label>`/`<Input>`/`<Select>` auto-bind without `htmlFor`/`id` |
| `textarea`, `toggle_switch` | On demand: `forge component install textarea toggle_switch`. ToggleSwitch is `role="switch"` with `label` or `ariaLabel`; both auto-bind inside `<FormField>` |
| `card` | Generic surface primitive |
| `status`, `tag`, `mono` | State (dot + word), kind label, copyable sha |
| `avatar`, `tabs`, `chip` | Avatar, tab nav (underline/pills/boxed), filter chip. `Tabs`/`FilterBar`: pass `activeTab`/`values` to control them from the URL |
| `table` | Bare structural table. For a wired list view use `<Resource>` (see `frontend-runtime/resource`) |
| `select` | Options array, sizes, invalid state |
| `toast_notification` | The toast STACK, rendered once by the owned `providers.tsx`. Never call it directly — fire `emitToast({ message, variant })` from `@/lib/events` |

Plus auto-installed: `sidebar_layout`, `page_header`, `badge`, `modal`, `skeleton_loader`, `pagination`, `search_input`, `alert_banner`, `key_value_list`, `login_form`. All `overwrite: once` — yours after install.

Two carry aliases; write against canonical. **`Badge`** — `error`/`success`/`warning`/`info`/`neutral`, aliases `danger→error`, `default→neutral`. **`Modal`** — canonical footer is the **`footer` slot prop**; rewrite footer-in-`children` when you next touch it. (`Button`'s destructive variant is `danger`, not `error`.)

## Owned higher-level components

Owned scaffold (not `ui/`), composing the base primitives — yours to edit or delete:

- **`status-badge.tsx`** / **`enum-select.tsx`** — proto-enum badge + select. See `frontend`, **Formatting**, for the enum-object rule and status colors.
- **`entity-picker.tsx`** / **`entity-name.tsx`** — the foreign-key pair: searchable select over a generated LIST hook, id→display-name over a GET hook. Never hand-roll a per-entity picker. See `frontend/pages`.
- **`src/components/session_nav.tsx`** — the user menu, on `useAuth()` and the library `dropdown_menu`: who is signed in, the theme, sign-out. Add your own pages (an account page) as a group in its `groups`. See `auth/frontend`.
- **`src/components/theme-toggle.tsx`** — one icon button cycling light / dark / system, with a `tooltip` that is also its name. The user menu holds the theme when someone is signed in; this is the signed-out stand-in.

Typed list views are NOT owned scaffold: the generated CRUD pages import `<Resource>` from `@reliantlabs/forge-web-runtime`, which owns the loading/error/empty/data ladder and cursor pagination (`frontend-runtime/resource`).
