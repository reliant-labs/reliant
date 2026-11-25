---
name: migrations/v1.11.0/auth-none-public
description: A project nobody signs in to (`forge project new --auth-mode none`, forge v1.11.0) now gets entity rpcs born `auth_required: false` and a public frontend that never calls the API's /auth/* routes. A project scaffolded earlier keeps what it was born with — CRUD rpcs that answer 401 to everyone, and a frontend that asks GET /auth/session (a 404) twice per page load and ships a password-field component — until you apply this. Use when deploy/kcl/identity.k is absent and a frontend still has src/lib/auth/native-login.ts.
version: v1.11.0
detection: test ! -f deploy/kcl/identity.k && test -f deploy/kcl/dev/main.k && ! grep -qs -E 'zitadel|"idp"' deploy/kcl/dev/main.k && ls frontends/*/src/lib/auth/native-login.ts > /dev/null 2>&1
---

# A project nobody signs in to works out of the box (v1.11.0)

`forge project new --auth-mode none` scaffolds an app with no sign-in: no
`deploy/kcl/identity.k`, no dev identity provider, a public frontend. Before
v1.11.0 two things in it still assumed a signed-in caller:

- `forge scaffold` births each entity's CRUD rpcs `auth_required: true`. The
  API fails closed and nothing can issue a token, so every one of them answers
  **401 to every caller**.
- The frontend's auth context asks `GET /auth/session` on every page load (and
  its telemetry asks again), which a project with no sign-in does not serve:
  two 404s per load. It also ships `src/lib/auth/native-login.ts` and the
  `login_form` component, a password field nothing renders.

From v1.11.0 a new project gets neither. Your files are yours, so forge does not
rewrite them; this playbook is the edit.

## What changes by itself

- `forge scaffold` births each NEW entity's CRUD rpcs `auth_required: false`,
  written in the proto, and prints that it did.
- `forge lint` warns (`forge-auth-required-no-sign-in`, advisory) about every
  rpc that still requires a caller, with its file and line.
- Once `native-login.ts` is gone, the next `forge generate` stops writing the
  session lookup into `src/lib/otel_gen.ts`, and never installs `login_form`.

## What to do

1. **Decide each rpc's posture.** `forge lint` lists the rpcs that require a
   caller. For each one anyone may call, change its option to
   `option (forge.v1.method) = {auth_required: false};` (keep any other fields
   in the literal). Making a write public means anyone who reaches the API can
   create, change or delete those rows — that is what an app nobody signs in to
   is. An rpc meant for callers with an API key or a token from an issuer you
   trust stays `true`; say so above it with
   `// forge:lint-disable-next-line forge-auth-required-no-sign-in: <who calls it>`.
   Then run `forge generate`: `pkg/middleware/procedures_gen.go` follows the
   proto.
2. **Stop the frontend asking for a session.** In each frontend under
   `frontends/`:
   - delete `src/lib/auth/native-login.ts` and `src/components/ui/login_form.tsx`
     (in a pnpm workspace, `packages/ui-web/src/components/ui/login_form.tsx` and
     its `LoginForm` line in `packages/ui-web/src/index.ts`, once no frontend
     signs in);
   - in `src/lib/auth/use-auth.ts`, replace `import type { Identity } from "./native-login";`
     with the interface itself:
     `export interface Identity { authenticated: boolean; email?: string; name?: string; subject?: string; }`;
   - in `src/components/session_nav.tsx`, import `Identity` from `@/lib/auth/use-auth`;
   - in `src/lib/auth/context.tsx`, drop the `./native-login` import (take
     `Identity` from `./use-auth`), have `refresh` set `{ authenticated: false }`
     instead of `await fetchIdentity()` outside mock mode, and have `logout` set
     it instead of `await apiSignOut()`. The telemetry `identifyUser` effect and
     the `auth:expired` listener have nothing to do and can go.

   The quickest way to get the exact files is a scratch project:
   `forge project new scratch --service s --frontend web --auth-mode none`, then
   copy `src/lib/auth/context.tsx`, `use-auth.ts` and its `context.test.tsx`.
3. Run `forge generate`, then the frontend's typecheck and tests, and
   `forge lint`.

## Verify

- `forge lint` reports no `forge-auth-required-no-sign-in` finding you did not
  mean to keep.
- `grep -rn 'native-login\|/auth/session' frontends/*/src --include='*.ts*'`
  finds only comments.
- In the browser's network tab, a page load makes no request to `/auth/session`.
