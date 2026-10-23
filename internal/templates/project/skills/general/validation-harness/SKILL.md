---
name: validation-harness
description: >-
  Create validation harnesses to verify fixes and features work end-to-end.
  Load this skill when you've made multiple fix attempts that haven't worked,
  when working on UI flows or multi-step integrations, or when you need
  to prove a fix actually works before declaring it done. Covers E2E test
  setup (Playwright, Cypress), API smoke tests, mock strategies for
  gRPC/REST backends, and parameterized test patterns.
---

# Validation Harness

## When to Reach for This Skill

Load this skill when any of these triggers apply:

- **Repeated fix attempts** — you've tried the same fix more than once and it still doesn't work
- **Multi-component interactions** — the fix spans frontend ↔ backend ↔ database boundaries
- **No manual verification** — you can't open a browser, hit an endpoint, or otherwise manually confirm the fix works (CI-only, headless, etc.)
- **Multiple user paths** — the feature has permutations (different roles, input combinations, feature flags) that need coverage

## Phase 1: Discovery — Before Writing Tests

Don't add new test tooling blindly. Check what's already there.

### Find Existing Test Infrastructure
- Look for config files: `playwright.config.*`, `vitest.config.*`, `jest.config.*`, `cypress.config.*`, `.storybook/`
- Check `package.json` scripts for test commands
- Search for existing test directories: `e2e/`, `tests/`, `__tests__/`, `*.spec.ts`, `*.test.ts`

### Study Existing Patterns
- How do existing tests import utilities? (fixtures, helpers, factories)
- What mocking approach is used? (MSW, Playwright route mocking, Cypress intercept, manual stubs)
- Where do test fixtures/data live?

### Pick the Fastest Path
Choose the **lightest-weight** approach that proves the fix:

| Situation | Approach |
|-----------|----------|
| Pure logic bug | Unit test |
| Component renders wrong | Component render test (vitest + testing-library) |
| UI flow broken | E2E test (Playwright/Cypress) |
| API returns wrong data | API smoke test (vitest or test script hitting endpoint) |
| Integration between services | Integration test with mocked boundaries |

**Always prefer the existing test infrastructure** over adding new tools.

## Phase 2: Mock Strategy — Isolate What You're Testing

### Principle: Mock at the Boundary

Mock HTTP/gRPC calls, not internal functions. This tests real code paths while isolating from external services.

**Playwright — route mocking:**
```ts
await page.route('**/service.Method', route => route.fulfill({
  status: 200,
  contentType: 'application/json',
  body: JSON.stringify({ field: 'value' }),
}));
```

**Cypress — intercept:**
```ts
cy.intercept('POST', '**/service.Method', {
  statusCode: 200,
  body: { field: 'value' },
}).as('apiCall');
```

### Return Realistic Responses
- Check proto files or TypeScript types for actual response shapes
- Don't invent response structures — copy from real API definitions
- Include edge-case fields (empty arrays, null optionals, pagination tokens)

### Don't Depend on a Running Backend for UI Tests
- Mock all API calls in E2E tests
- If you need a real backend, that's an integration test — keep it separate

## Phase 3: Test Design Patterns

### Start With the Happy Path
Write one test that exercises the full flow end-to-end. Get it passing. Then layer in failure cases.

### Parameterize Permutations
Don't write N individual tests for N options — use a test matrix:
```ts
const CASES = [
  { input: 'admin', expectVisible: ['settings', 'users'] },
  { input: 'viewer', expectVisible: ['dashboard'] },
  { input: 'editor', expectVisible: ['dashboard', 'editor'] },
];

for (const c of CASES) {
  test(`role ${c.input} sees ${c.expectVisible}`, async ({ page }) => {
    // setup role, verify visible elements
  });
}
```

### Test What Matters
- **Transitions/navigation** — not just static rendering
- **Error states** — backend down, timeout, invalid response, 403/404
- **State persistence** — localStorage hydration, stale state recovery:
  ```ts
  await page.addInitScript(() => {
    localStorage.setItem('key', JSON.stringify({ staleValue: 99 }));
  });
  ```
- **Loading states** — delay mock responses to verify spinners/skeletons render

## Phase 4: Verification Loop

Follow this strict loop — don't skip steps:

1. **Write the test** targeting the broken behavior
2. **Run it** — it should **fail** (this confirms the test actually catches the bug)
3. **If the test passes immediately**, your test isn't testing the right thing — fix the test
4. **Apply the fix**
5. **Run the test again** — it should **pass**
6. **Run twice** to catch flakiness: `--repeat-each=2` (Playwright) or re-run in CI

### If You Can't Make the Test Fail First
Your test probably:
- Is asserting on the wrong element/value
- Has a race condition hiding the failure (add `await` or `waitFor`)
- Is testing a mock instead of real code

Go back and fix the test before fixing the code.

## Anti-Patterns

- **Don't add Playwright/Cypress to a project that doesn't have it** just for one test — use what's already there
- **Don't write tests that only assert on mocks** — the point is to test YOUR code, not the mock
- **Don't skip the red-green loop** — a test that never failed proves nothing
- **Don't over-mock** — if you're mocking 10 things to test 1, your test scope is wrong
- **Don't ignore flakiness** — a flaky test is worse than no test; fix timing issues immediately
