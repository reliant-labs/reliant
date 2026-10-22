import { test, expect, type Page, type Request, type Response } from '@playwright/test';

// Cold loads of deep (2+ segment) routes — the gap that let ed96ce8 ship.
//
// ## Why this file exists, and why the rest of the suite could not catch it
//
// `vite.config.ts` sets `base: "/"`. With a relative base (`"./"`) the built
// index.html references `./assets/index-<hash>.js`, which the browser resolves
// against the DOCUMENT's directory. That only differs from the absolute form
// when the document's path HAS a directory component:
//
//   at /                       ./assets/x.js -> /assets/x.js          identical, loads fine
//   at /auth/github/callback   ./assets/x.js -> /auth/github/assets/x.js  404
//
// and the 404 is invisible, because the SPA fallback answers it with
// index.html — so the browser gets HTML where it asked for a module and dies
// with "Expected a JavaScript module". Every other spec in this directory
// navigates to `/`, `/onboarding`, or `/onboarding?plan=...`; a query string
// adds no path segment, so none of them is a deep case and none of them could
// have failed.
//
// Two properties are therefore load-bearing here and must not be "simplified":
//
//  1. COLD loads only. `page.goto` per route, never client-side navigation.
//     Once the SPA has booted at `/`, a pushState to a deep route fetches no
//     assets at all, so a warm navigation cannot reproduce this no matter how
//     deep the route is.
//  2. DEPTH >= 2. A one-segment route is as safe as `/`.
//
// The routes below are the ones users arrive at COLD from an external
// redirect — OAuth callbacks, billing/settings returns. That is not a
// coincidence: cold deep-link entry is exactly what an identity provider or
// Stripe does to you, which is why this whole bug class presents as an
// "OAuth/billing is broken" report rather than as a build problem.
//
// Assertions are deliberately restricted to asset-loading facts and a boot
// probe. CI runs with `retries: 2` and a single worker, so anything
// non-deterministic (real network, auth state, page copy) would triple in cost
// and eventually get muted.

/** Routes with 2+ path segments that a user reaches cold, from off-site. */
const DEEP_ROUTES = [
  // Supabase OAuth lands here after provider consent.
  '/auth/callback',
  // GitHub redirects here directly; GITHUB_REDIRECT_URI points at this exact
  // path, so it is reached cold, in a tab that may have no app session.
  '/auth/github/callback',
  // Supabase OAuth-server consent screen — reached mid-flow from a
  // third-party client, often on a device that has never opened the app.
  '/oauth/consent',
  // The workspace proxy's auth bounce.
  '/auth/proxy',
  // Settings sub-routes: where the post-Stripe return lands, and where a
  // connector authorization redirect arrives.
  '/settings/billing',
  '/settings/connectors/authorize',
  // Deepest route in the app (3 segments) — the widest resolution error.
  '/m/chats/abc123/workflow',
];

/**
 * Watches a page for the two network signatures of a base-path break.
 *
 * A wrong base does NOT surface as a clean 404: the SPA fallback returns
 * index.html with status 200 and `content-type: text/html`, so status alone
 * sees nothing wrong. The MIME mismatch is the reliable tell, and the failed
 * request is the belt-and-braces case for a server without a fallback.
 */
function watchAssetLoading(page: Page) {
  const failedAssets: string[] = [];
  const htmlForScript: string[] = [];

  page.on('requestfailed', (request: Request) => {
    const url = request.url();
    if (/\/assets\/|\.(?:js|mjs|css)(?:\?|$)/.test(url)) {
      failedAssets.push(`${url} (${request.failure()?.errorText ?? 'unknown'})`);
    }
  });

  page.on('response', (response: Response) => {
    const url = response.url();
    if (!/\.(?:js|mjs|css)(?:\?|$)/.test(url)) return;
    const contentType = response.headers()['content-type'] ?? '';
    if (contentType.includes('text/html')) {
      htmlForScript.push(`${url} -> ${contentType} (status ${response.status()})`);
    }
  });

  return { failedAssets, htmlForScript };
}

test.describe('deep routes survive a cold load', () => {
  for (const route of DEEP_ROUTES) {
    test(`cold load of ${route} boots the app`, async ({ page }) => {
      const { failedAssets, htmlForScript } = watchAssetLoading(page);

      // Cold entry. `domcontentloaded` rather than `networkidle`: these routes
      // legitimately fire RPCs that will fail without a backend, and waiting
      // for the network to settle would make the test depend on that.
      await page.goto(route, { waitUntil: 'domcontentloaded' });

      // Assets resolved. This is the ed96ce8 assertion: with a relative base,
      // the module request resolves into the route's own directory and comes
      // back as the fallback's index.html.
      expect(
        htmlForScript,
        `a script/style request was answered with HTML at ${route} — the classic ` +
          `relative-base failure (vite.config.ts must keep base: "/")`,
      ).toEqual([]);
      expect(failedAssets, `asset requests failed at ${route}`).toEqual([]);

      // The app actually booted. #root having children separates "the bundle
      // parsed and React mounted" from "the document loaded and the script
      // died" — the latter still yields a 200 and an empty body.
      const root = page.locator('#root');
      await expect(root).toBeAttached();
      await expect
        .poll(() => root.evaluate((el) => el.childElementCount), {
          message: `#root never gained children at ${route} — the app did not mount`,
          timeout: 15_000,
        })
        .toBeGreaterThan(0);

      // And it rendered something a human would see, not just an empty shell.
      await expect
        .poll(() => root.evaluate((el) => (el.textContent ?? '').trim().length), {
          message: `#root mounted but rendered no visible text at ${route}`,
          timeout: 15_000,
        })
        .toBeGreaterThan(0);
    });
  }
});

// A guard on the guard: if the deep routes above are ever changed to shallow
// ones, every test still passes while testing nothing — the failure mode this
// whole file exists to fix. Pin the depth invariant explicitly.
test('every route under test is actually deep enough to catch the bug', () => {
  for (const route of DEEP_ROUTES) {
    const segments = route.split('/').filter(Boolean);
    expect(
      segments.length,
      `${route} has ${segments.length} path segment(s). A relative base resolves ` +
        `identically to an absolute one at depth 0 and 1, so this route cannot ` +
        `detect the bug and gives false confidence.`,
    ).toBeGreaterThanOrEqual(2);
  }
});
