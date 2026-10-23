---
name: frontend-testing
description: Frontend testing methodology for forge — what to test, what NOT to test, recipes for utilities/pages/stores, mockTransport seam, and anti-patterns to forbid.
---

# Frontend Testing

Use this skill when writing tests for any forge frontend (`frontends/<name>/`). For backend tests see `testing` and `testing/patterns`.

## What to test, by layer

| Layer | Test type | What for |
|-------|-----------|----------|
| Pure utilities (`lib/billing.ts`, `lib/connect-errors.ts`, formatters) | Table-driven, no React | Input → output, every branch is a row |
| Zod schemas | Table-driven, no React | The schema IS the contract — accept/reject rows |
| Zustand stores | Direct state assertion, no React | `store.getState()` after action — reducer-shaped |
| Pages (`app/.../page.tsx`) | Integration via Testing Library | Loading / error / empty / success — one screen assertion per state |
| Custom domain components | Behavior tests only if they have interaction/state | Click handlers, form submission, internal state transitions |
| Forms (`react-hook-form`) | Schema test separately, then form flow integrates schema | Submit valid, submit invalid, surface server error |
| Auth providers / transport / env | One smoke test at a page boundary | Bearer header attached, refresh path |

## What NOT to test

- Generated code (`src/hooks/<svc>-service-hooks_gen.ts`, `src/gen/**`, `src/lib/connect.ts`) — forge owns it
- Forge UI primitives (`components/ui/button|input|...`) — forge owns it
- React / Next / TanStack internals — they have their own tests
- JSX branch coverage — test visible behavior (`screen.findByText`), not which conditional ran

## How to test (recipes)

### Recipe 1 — Pure utility (table-driven)

```ts
import { describe, it, expect } from "vitest";
import { formatCents } from "./billing";

describe("formatCents", () => {
  it.each([
    { cents: 0, want: "$0.00" },
    { cents: 99, want: "$0.99" },
    { cents: 12345, want: "$123.45" },
    { cents: -250, want: "-$2.50" },
  ])("$cents -> $want", ({ cents, want }) => {
    expect(formatCents(cents)).toBe(want);
  });
});
```

### Recipe 2 — Page (mock at the transport)

```tsx
import { describe, it, expect } from "vitest";
import { screen } from "@testing-library/react";
import { renderWithTransport, mockTransport } from "@/lib/test-utils";
import UsersPage from "./page";

describe("UsersPage", () => {
  it("renders the user list on success", async () => {
    const transport = mockTransport({
      "UserService.ListUsers": {
        response: { users: [{ id: "u_1", email: "a@b.com" }] },
      },
    });

    renderWithTransport(<UsersPage />, { transport });

    expect(await screen.findByText("a@b.com")).toBeTruthy();
  });

  it("renders an error state when the RPC fails", async () => {
    const transport = mockTransport({
      "UserService.ListUsers": { error: "boom" },
    });

    renderWithTransport(<UsersPage />, { transport });

    expect(await screen.findByRole("alert")).toBeTruthy();
  });
});
```

### Recipe 3 — Zustand store (direct state assertion)

```ts
import { describe, it, expect, beforeEach } from "vitest";
import { useUIStore } from "./ui-store";

describe("ui-store", () => {
  beforeEach(() => {
    useUIStore.setState({ sidebarOpen: false });
  });

  it("toggleSidebar flips the boolean", () => {
    useUIStore.getState().toggleSidebar();
    expect(useUIStore.getState().sidebarOpen).toBe(true);

    useUIStore.getState().toggleSidebar();
    expect(useUIStore.getState().sidebarOpen).toBe(false);
  });
});
```

## Anti-patterns to forbid

- **Snapshot tests on components** (locks implementation, not behavior). Only OK for stable serialized output (CSV export, generated SQL).
- **Mocking React Query directly** (`vi.mock('@tanstack/react-query')`) — wraps the wrong layer. Mock the transport; let React Query work normally.
- **Mocking `useAuth()`** — render inside a real `<AuthContextProvider>` and control the session at the seam below it (stub `GET /auth/session`), so the integration shape is tested.
- **Implementation-detail assertions** (`vi.spyOn` on private functions, querying by component name). Test from the user's perspective.

## Tooling

- Runner: **Vitest** (matches Next.js / Tailwind v4 / forge's TS toolchain)
- DOM: `jsdom` or `happy-dom`
- React: `@testing-library/react` + `@testing-library/user-event`
- Network: forge ships a `mockTransport()` helper in `src/lib/test-utils.tsx` (see the file)
- Coverage target: utilities + stores + page integrations. **NOT** 100% coverage; aim for behavioral coverage of the four states (loading / error / empty / success) per page.

## File layout

- Co-located: `users-list.tsx` next to `users-list.test.tsx`
- Pages: `app/<route>/page.test.tsx` next to `page.tsx`
- Test utilities: `src/test-utils/` (renamed from `src/lib/test-utils.tsx` if it grows; default is the single file)
- One Vitest config at the frontend root: `vitest.config.ts`

## Running tests

- `npm test` — single run
- `npm run test:watch` — watch mode
- CI: the `test` job in `.github/workflows/ci.yml` runs `npx vitest run` per frontend

## Rules

- Test behavior, not implementation.
- Mock at the seam that's stable (the transport, the auth endpoints), not the layer above (hook return).
- Four states per data-fetching page (loading / error / empty / success). Generated hooks expose all four.
- Co-locate tests with the file under test.
- Snapshot tests are forbidden except for stable serialized output.
- Generated code, forge UI primitives, and React/Next internals are not yours to test.

## When this skill is not enough

- Backend testing — see `testing` and `testing/patterns`.
- Mobile (React Native / Expo) — testing patterns differ; check the `frontend` skill.
- Visual regression — out of scope for forge's defaults; project-specific.
