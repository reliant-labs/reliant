import { test, expect, type Page } from '@playwright/test';
import { devHarnessPresent } from './devHarness';
import { COLOR_SCHEMES, FORGE_COLOR_TOKENS } from '../src/lib/forgeTokens';

// The forge component library in every color scheme, light and dark, as
// screenshots: the visual half of the token bridge's contract (the textual
// half is colorSchemeTokenContract.test.ts and forgeTokenBridge.test.tsx).
//
// It drives /forge-token-sandbox (components/forge-ui/ForgeTokenSandbox.tsx),
// a dev-only harness: no backend, no session. Against CI's `vite preview` of
// a production build the route redirects to / and this suite skips.
//
// Three regions per scheme × mode — 60 baselines in
// color-schemes.spec.ts-snapshots/:
//
//   library   every forge-ui component the app imported before R5 S1
//   s1        the call sites R5 S1 moved off components/ui (Card, Badge,
//             ErrorSummary, Progress), outside .forge-ui as in the app
//   overlays  sidebar_layout, modal and confirmation_dialog, each pinned in
//             a box
//
// An unbridged token renders as a transparent fill or inherited text, which
// a baseline catches and a jsdom test cannot. When a change is MEANT to move
// pixels, re-capture with `npx playwright test e2e/color-schemes.spec.ts
// --update-snapshots` and review the image diff in the PR.

const SCHEMES = COLOR_SCHEMES;

const MODES = ['light', 'dark'] as const;

const HARNESS = '/forge-token-sandbox';

async function openHarness(page: Page, query: string, scheme: string, mode: string) {
  await page.goto(`${HARNESS}${query}`);
  const present = await devHarnessPresent(page, HARNESS, page.getByTestId('forge-scope'));
  test.skip(!present, 'the token sandbox is a dev-build-only route');

  // The sandbox's own controls set data-color-scheme and .dark on <html>,
  // which is exactly what the app's appearance settings do.
  await page.getByTestId('scheme-select').selectOption(scheme);
  const toggle = page.getByTestId('toggle-dark');
  if ((await toggle.textContent()) !== (mode === 'dark' ? 'Dark' : 'Light')) await toggle.click();
  await expect(page.locator('html')).toHaveAttribute('data-color-scheme', scheme);
  if (mode === 'dark') await expect(page.locator('html')).toHaveClass(/\bdark\b/);
  else await expect(page.locator('html')).not.toHaveClass(/\bdark\b/);

  // Fonts change glyph metrics; a shot taken before they load is a flake.
  await page.evaluate(() => document.fonts.ready);
}

/**
 * Every forge token, painted as a background inside and outside .forge-ui.
 * A var() that does not resolve makes the declaration invalid at computed
 * time, and background-color falls back to transparent — so a transparent
 * probe is an unbridged token, named, rather than a pixel to squint at.
 */
async function unresolvedTokens(page: Page): Promise<string[]> {
  return page.evaluate((tokens) => {
    const bad: string[] = [];
    const hosts: Array<[string, HTMLElement]> = [
      ['<html>', document.body],
      ['.forge-ui', document.querySelector<HTMLElement>('[data-testid="forge-scope"]')!],
    ];
    for (const [where, host] of hosts) {
      for (const token of tokens) {
        const probe = document.createElement('div');
        probe.style.backgroundColor = `var(--color-${token})`;
        host.appendChild(probe);
        const color = getComputedStyle(probe).backgroundColor;
        probe.remove();
        if (!color || color === 'rgba(0, 0, 0, 0)' || color === 'transparent') bad.push(`${token} at ${where}`);
      }
    }
    return bad;
  }, [...FORGE_COLOR_TOKENS]);
}

const SHOT = { animations: 'disabled', caret: 'hide', maxDiffPixelRatio: 0.002 } as const;

test.describe('forge components across color schemes', () => {
  // Font rasterization differs per OS, so a baseline is only comparable on
  // the platform that captured it. These were captured on linux chromium.
  test.skip(process.platform !== 'linux', 'screenshot baselines are linux-only');

  // Tall enough that every region fits without scrolling. An element
  // screenshot that has to scroll lands on a fractional offset, and two
  // consecutive captures then round to different heights and never settle.
  test.use({ viewport: { width: 1280, height: 4000 }, deviceScaleFactor: 1 });
  // Two navigations and three captures per test; on a loaded machine the
  // default 30s is spent before the last capture settles.
  test.setTimeout(120_000);

  for (const scheme of SCHEMES) {
    for (const mode of MODES) {
      test(`${scheme} ${mode}`, async ({ page }) => {
        const errors: string[] = [];
        page.on('pageerror', (error) => errors.push(error.message));

        await openHarness(page, '', scheme, mode);
        expect(await unresolvedTokens(page), 'forge tokens that paint transparent').toEqual([]);
        await expect(page.getByTestId('forge-library')).toHaveScreenshot(`library-${scheme}-${mode}.png`, SHOT);
        await expect(page.getByTestId('s1-sites')).toHaveScreenshot(`s1-${scheme}-${mode}.png`, SHOT);

        await openHarness(page, '?view=overlays', scheme, mode);
        await expect(page.getByTestId('overlays')).toHaveScreenshot(`overlays-${scheme}-${mode}.png`, SHOT);

        expect(errors).toEqual([]);
      });
    }
  }
});
