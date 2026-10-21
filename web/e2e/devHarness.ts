import type { Locator, Page } from '@playwright/test';

// The fixture harnesses (/forge-observability-preview, /forge-workloads-preview,
// ...) exist only in a DEV build: DevOnlyRoute redirects a production build to
// `/`, which is what CI's `vite preview` serves. A spec against one skips when
// it is absent.
//
// Absence used to be detected by waiting out a 20s timeout for the harness to
// render, once per test: 7 observability tests spent 151s of a 190s
// Playwright shard waiting to skip. The redirect is the positive signal, so
// this settles on whichever comes first: the harness renders (present) or the
// page is no longer on the harness path (absent). The 20s timeout remains
// only as the fallback for a page that does neither.
//
// harnessPath is passed in rather than read from page.url(): in CI the
// redirect usually lands before page.goto() resolves, so the URL read
// afterwards is already `/`.
export async function devHarnessPresent(page: Page, harnessPath: string, target: Locator): Promise<boolean> {
  const rendered = target.first().waitFor({ timeout: 20_000 }).then(() => true, () => false);
  const redirected = page
    .waitForFunction((path) => window.location.pathname !== path, harnessPath, { timeout: 20_000 })
    // A wait that times out (or outlives the page) decides nothing: the
    // render wait above settles it.
    .then(() => false, () => new Promise<boolean>(() => {}));
  return Promise.race([rendered, redirected]);
}
