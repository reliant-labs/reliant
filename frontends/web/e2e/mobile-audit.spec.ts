import { test, type Page } from '@playwright/test';
import { mockMobileBackend, PHONE } from './mobileMocks';

// The mobile surface audit (research/MOBILE_AUDIT.md), as a script: every
// top-level surface the desktop app has, opened on a phone, with where it
// ended up, what it measures, and a screenshot. Not an assertion suite —
// mobile.spec.ts is that — so it runs only when MOBILE_SHOTS=<dir> names where
// to write the shots:
//
//   MOBILE_SHOTS=/tmp/mobile-before BASE_URL=http://127.0.0.1:5173 \
//     npx playwright test e2e/mobile-audit.spec.ts
//
// Each visit is a cold load (page.goto), the way a link from a notification or
// another app arrives on a phone. `?desktop=1` entries load the desktop
// component at phone width (the redirect's opt-out), to measure what reusing
// it on mobile starts from.

const shotDir = process.env.MOBILE_SHOTS;

const SURFACES: { name: string; path: string; drawer?: boolean }[] = [
  { name: 'chats', path: '/m/chats' },
  { name: 'drawer', path: '/m/chats', drawer: true },
  { name: 'chat', path: '/m/chats/chat_a' },
  { name: 'inbox', path: '/inbox' },
  { name: 'm-inbox', path: '/m/inbox' },
  { name: 'apps', path: '/forge' },
  { name: 'observability', path: '/forge/observability' },
  { name: 'workflows-area', path: '/workflows' },
  { name: 'runs', path: '/workflows/runs' },
  { name: 'settings', path: '/settings' },
  { name: 'm-settings', path: '/m/settings' },
  { name: 'm-workflows', path: '/m/workflows' },
  { name: 'm-daemons', path: '/m/daemons' },
  { name: 'm-search', path: '/m/search' },
  { name: 'm-projects', path: '/m/projects' },
  { name: 'm-account', path: '/m/account' },
  { name: 'm-diagnostics', path: '/m/diagnostics' },
  // The desktop components at phone width.
  { name: 'desktop-inbox', path: '/inbox?desktop=1' },
  { name: 'desktop-runs', path: '/workflows/runs?desktop=1' },
  { name: 'desktop-settings', path: '/settings?desktop=1' },
  { name: 'desktop-observability-harness', path: '/forge-observability-preview?desktop=1' },
  { name: 'desktop-workloads-harness', path: '/forge-workloads-preview?desktop=1' },
];

/** Layout facts a phone user feels: sideways scroll, clipped panels, small targets, tables. */
async function measure(page: Page) {
  return page.evaluate(() => {
    const vw = window.innerWidth;
    const visible = (el: Element) => {
      const r = el.getBoundingClientRect();
      const s = getComputedStyle(el);
      return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none' && Number(s.opacity) > 0;
    };
    const label = (el: Element) =>
      (el.getAttribute('aria-label') || (el as HTMLElement).innerText || el.getAttribute('title') || el.tagName)
        .trim()
        .replace(/\s+/g, ' ')
        .slice(0, 28);
    const targets = Array.from(
      document.querySelectorAll('a[href], button, [role="button"], [role="tab"], [role="radio"], input, select, textarea'),
    ).filter(visible);
    const small = targets.filter((el) => {
      const r = el.getBoundingClientRect();
      return r.height < 44 || r.width < 44;
    });
    const overflowing = Array.from(document.querySelectorAll('body *'))
      .filter(visible)
      .filter((el) => el.getBoundingClientRect().right > vw + 1).length;
    const fixedWide = Array.from(document.querySelectorAll('aside, nav, [class*="w-60"], [class*="w-64"], [class*="w-72"]'))
      .filter(visible)
      .map((el) => `${el.tagName.toLowerCase()}:${Math.round(el.getBoundingClientRect().width)}px`)
      .slice(0, 4);
    return {
      scrollWidth: document.documentElement.scrollWidth,
      targets: targets.length,
      smallTargets: small.length,
      smallSample: small.slice(0, 6).map(label),
      overflowing,
      tables: document.querySelectorAll('table').length,
      fixedWide,
      h1: (document.querySelector('h1') as HTMLElement | null)?.innerText ?? '',
    };
  });
}

test.describe('mobile surface audit', () => {
  test.skip(!shotDir, 'set MOBILE_SHOTS=<dir> to run the audit');
  test.use(PHONE);

  for (const surface of SURFACES) {
    test(surface.name, async ({ page }) => {
      const errors: string[] = [];
      page.on('pageerror', (error) => {
        // A source build without Supabase keys says so once per load; the
        // API-key login these mocks use does not need them.
        if (!error.message.includes('VITE_SUPABASE_ANON_KEY')) errors.push(error.message);
      });
      await mockMobileBackend(page);
      await page.goto(surface.path);
      await page.waitForTimeout(4000);
      if (surface.drawer) {
        await page.getByRole('button', { name: /open menu/i }).first().click();
        await page.waitForTimeout(500);
      }
      const facts = await measure(page);
      const landed = new URL(page.url());
      console.log(
        `[audit] ${surface.name}: ${surface.path} -> ${landed.pathname} ${JSON.stringify(facts)}` +
          (errors.length ? ` errors=${JSON.stringify(errors)}` : ''),
      );
      await page.screenshot({ path: `${shotDir}/${surface.name}.png` });
    });
  }
});
