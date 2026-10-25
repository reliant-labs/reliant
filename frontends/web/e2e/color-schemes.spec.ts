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
// Eight captures per scheme × mode — 96 baselines in
// color-schemes.spec.ts-snapshots/:
//
//   library   every forge-ui component the app imported before R5 S1
//   s1        the call sites R5 S1 moved off components/ui (Card, Badge,
//             ErrorSummary, Progress), outside .forge-ui as in the app
//   buttons   forge's Button: every variant, size and state, outside
//             .forge-ui on a card and on the page background
//   overlays  sidebar_layout, modal and confirmation_dialog, each pinned in
//             a box
//   r5        the R5 overlays (components/forge-ui/R5Sandbox.tsx), each
//             opened: r5-tooltip-hover (a disabled button's reason, hovered),
//             r5-tooltip-focus (a toolbar button focused), r5-contextmenu (a
//             file row right-clicked), r5-helppopover (a field's help open),
//             r5-picker-single / r5-picker-multi (each picker opened)
//   s2        the R5 S2 primitives as their call sites render them
//             (components/forge-ui/S2Sandbox.tsx): s2-sites (switches, text
//             fields), s2-menus (a row menu and a multi-select filter, each
//             opened), s2-modal (a form dialog, as the viewport)
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
  // The scheme picker renders in every view (the s2 views leave the
  // .forge-ui scope empty, so it is not visible there).
  const present = await devHarnessPresent(page, HARNESS, page.getByTestId('scheme-select'));
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
  test.use({ viewport: { width: 1280, height: 4800 }, deviceScaleFactor: 1 });
  // Two navigations and three captures per test; on a loaded machine the
  // default 30s is spent before the last capture settles.
  test.setTimeout(240_000);

  for (const scheme of SCHEMES) {
    for (const mode of MODES) {
      test(`${scheme} ${mode}`, async ({ page }) => {
        const errors: string[] = [];
        page.on('pageerror', (error) => errors.push(error.message));

        await openHarness(page, '', scheme, mode);
        expect(await unresolvedTokens(page), 'forge tokens that paint transparent').toEqual([]);
        await expect(page.getByTestId('forge-library')).toHaveScreenshot(`library-${scheme}-${mode}.png`, SHOT);
        await expect(page.getByTestId('s1-sites')).toHaveScreenshot(`s1-${scheme}-${mode}.png`, SHOT);
        await expect(page.getByTestId('buttons')).toHaveScreenshot(`buttons-${scheme}-${mode}.png`, SHOT);

        await expect(page.getByTestId('s2-sites')).toHaveScreenshot(`s2-sites-${scheme}-${mode}.png`, SHOT);

        await openHarness(page, '?view=overlays', scheme, mode);
        await expect(page.getByTestId('overlays')).toHaveScreenshot(`overlays-${scheme}-${mode}.png`, SHOT);

        // Menus portal to <body>, so the region's capture is a clip of the
        // page that includes the open menu below its trigger.
        await openHarness(page, '?view=s2-menus', scheme, mode);
        const menus = page.getByTestId('s2-menus');
        // Opened from the keyboard: a pointer would leave the trigger's
        // tooltip racing the capture.
        await page.getByRole('button', { name: 'Workflow actions' }).focus();
        await page.keyboard.press('Enter');
        await expect(page.getByRole('menu', { name: 'Workflow actions' })).toBeVisible();
        await expect(menus).toHaveScreenshot(`s2-rowmenu-${scheme}-${mode}.png`, SHOT);
        await page.keyboard.press('Escape');
        await expect(page.getByRole('menu')).toHaveCount(0);
        await page.getByRole('button', { name: /^State:/ }).focus();
        await page.keyboard.press('Enter');
        await expect(page.getByRole('menu', { name: 'State' })).toBeVisible();
        await expect(menus).toHaveScreenshot(`s2-filter-${scheme}-${mode}.png`, SHOT);

        // A modal covers the viewport; capture the viewport at a dialog's
        // real proportions rather than the 4800px-tall harness page.
        await page.setViewportSize({ width: 1280, height: 800 });
        await openHarness(page, '?view=s2-modal', scheme, mode);
        await page.getByRole('button', { name: 'Open dialog' }).focus();
        await page.keyboard.press('Enter');
        await expect(page.getByRole('dialog')).toBeVisible();
        await expect(page).toHaveScreenshot(`s2-modal-${scheme}-${mode}.png`, SHOT);

        expect(errors).toEqual([]);
      });

      test(`${scheme} ${mode} r5`, async ({ page }) => {
        const errors: string[] = [];
        page.on('pageerror', (error) => errors.push(error.message));

        // Tooltips portal to <body>: each capture is a clip of the page
        // that includes the bubble beside its trigger.
        await openHarness(page, '?view=r5-tooltips', scheme, mode);
        const tooltips = page.getByTestId('r5-tooltips');
        await page.getByRole('button', { name: 'Add step' }).hover();
        await expect(page.getByRole('tooltip')).toBeVisible();
        await expect(tooltips).toHaveScreenshot(`r5-tooltip-hover-${scheme}-${mode}.png`, SHOT);
        await page.mouse.move(0, 0);
        await expect(page.getByRole('tooltip')).toHaveCount(0);
        // From the keyboard: a hint shows for focus the user can see
        // (:focus-visible), and a programmatic focus after the mode toggle's
        // click is not that.
        await page.getByTestId('toggle-dark').focus();
        await page.keyboard.press('Tab');
        await expect(page.getByRole('button', { name: 'Copy path' })).toBeFocused();
        await expect(page.getByRole('tooltip')).toBeVisible();
        await expect(tooltips).toHaveScreenshot(`r5-tooltip-focus-${scheme}-${mode}.png`, SHOT);

        // The context menu opens at the pointer; the help popover below its
        // icon. Both portal to <body>, inside the region's box.
        await openHarness(page, '?view=r5-menus', scheme, mode);
        const menus = page.getByTestId('r5-menus');
        const row = page.getByRole('button', { name: 'main.go' });
        const box = (await row.boundingBox())!;
        await page.mouse.click(box.x + 40, box.y + box.height / 2, { button: 'right' });
        await expect(page.getByText('Copy path')).toBeVisible();
        await page.mouse.move(0, 0);
        await expect(menus).toHaveScreenshot(`r5-contextmenu-${scheme}-${mode}.png`, SHOT);
        await page.keyboard.press('Escape');
        await expect(page.getByText('Copy path')).toHaveCount(0);
        await page.getByRole('button', { name: /^Help/ }).click();
        await expect(page.getByText(/BEFORE the step runs/)).toBeVisible();
        await page.mouse.move(0, 0);
        await expect(menus).toHaveScreenshot(`r5-helppopover-${scheme}-${mode}.png`, SHOT);

        // The pickers' panels sit below their triggers, inside the region.
        await openHarness(page, '?view=r5-pickers', scheme, mode);
        const pickers = page.getByTestId('r5-pickers');
        await page.getByTestId('pick-branch').getByRole('button').first().click();
        await expect(page.getByText('Remote Branches')).toBeVisible();
        await page.mouse.move(0, 0);
        await expect(pickers).toHaveScreenshot(`r5-picker-single-${scheme}-${mode}.png`, SHOT);
        await page.keyboard.press('Escape');
        await expect(page.getByText('Remote Branches')).toHaveCount(0);
        await page.getByTestId('pick-presets').getByRole('button').first().click();
        await expect(page.getByText('Breaks the work into steps')).toBeVisible();
        await page.mouse.move(0, 0);
        await expect(pickers).toHaveScreenshot(`r5-picker-multi-${scheme}-${mode}.png`, SHOT);


        expect(errors).toEqual([]);
      });
    }
  }
});
