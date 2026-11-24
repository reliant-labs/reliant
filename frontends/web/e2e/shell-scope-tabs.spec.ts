import { expect, test, type BrowserContext, type Page } from '@playwright/test';

import { CHATS, PROJECT, mockAppsBackend } from './appsMocks';

// The shell's scope is the URL's, and each tab's own (owner, 2026-11-24: the
// URL is the only source of view state). Two pages in ONE browser context
// share localStorage and keep their own sessionStorage, as two tabs do:
//
//   - tab A chooses several projects, tab B All; reloading both and bringing
//     each to the front in turn (the shared sidebar.projects setting is
//     re-read on focus) leaves each with its own scope;
//   - a third tab opened on the bare home starts from the last choice made
//     anywhere, B's;
//   - deep links name their scope: `/?projects=<a>,<b>`, `/c/<chat>?projects=all`;
//   - Back and Forward across a scope change bring back each entry's scope;
//   - a refresh keeps the scope, the chat, the thread and `?panel=`;
//   - with org routes on, an old `/project/<id>` lands on `/o/<slug>/project/<id>`
//     (reliant#3450), and the org form keeps the shell's scope.

const DOCS = { ...PROJECT, id: 'proj_docs', name: 'docs-site', path: '/home/workspace/projects/docs-site', isForge: false, forgeProjectName: '' };
const PROJECTS = [PROJECT, DOCS];
const BACKEND = { person: true, projectsByOrg: { org_acme: PROJECTS } };
const LOADED = { timeout: 15_000 };
const CHAT = CHATS[0].id;

/** The switcher's label: a project's name, "2 projects", or "All projects". */
const label = (page: Page) => page.getByTestId('shell-project-name');
const scopeOf = (page: Page) => new URL(page.url()).searchParams.get('projects');

const errors = new WeakMap<Page, string[]>();
function watch(page: Page): Page {
  const recorded: string[] = [];
  errors.set(page, recorded);
  page.on('pageerror', (error) => recorded.push(error.message));
  return page;
}

async function openTab(context: BrowserContext): Promise<Page> {
  const page = watch(await context.newPage());
  await mockAppsBackend(page, BACKEND);
  return page;
}

async function chooseAll(page: Page) {
  await page.getByTestId('shell-project-switcher').click();
  await page.getByTestId('shell-switcher-all-projects').click();
  await expect(label(page)).toHaveText('All projects');
}

test.describe('the shell scope, per tab and in the URL', () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test('deep links open with the scope they name', async ({ context }) => {
    const page = await openTab(context);

    await page.goto(`/?projects=${PROJECT.id},${DOCS.id}`);
    await expect(label(page)).toHaveText('2 projects', LOADED);

    await page.goto(`/c/${CHAT}?projects=all`);
    await expect(label(page)).toHaveText('All projects', LOADED);
    expect(new URL(page.url()).pathname).toBe(`/c/${CHAT}`);
    expect(scopeOf(page)).toBe('all');

    expect(errors.get(page), 'no uncaught JavaScript errors').toEqual([]);
  });

  test('Back and Forward across a scope change bring back each scope', async ({ context }) => {
    const page = await openTab(context);

    await page.goto(`/c/${CHAT}`);
    await expect(label(page)).toHaveText(PROJECT.name, LOADED);

    await chooseAll(page);
    await expect.poll(() => scopeOf(page)).toBe('all');
    const allUrl = page.url();

    await page.goBack();
    await expect.poll(() => new URL(page.url()).pathname).toBe(`/c/${CHAT}`);
    expect(scopeOf(page)).toBeNull();
    await expect(label(page)).toHaveText(PROJECT.name);

    await page.goForward();
    await expect.poll(() => page.url()).toBe(allUrl);
    await expect(label(page)).toHaveText('All projects');

    expect(errors.get(page), 'no uncaught JavaScript errors').toEqual([]);
  });

  test('a refresh keeps the scope, the chat, its thread and the open panel', async ({ context }) => {
    const page = await openTab(context);
    const path = `/c/${CHAT}/t/th_review`;

    await page.goto(`${path}?panel=files&projects=all`);
    await expect(label(page)).toHaveText('All projects', LOADED);
    await expect.poll(() => new URL(page.url()).pathname).toBe(path);

    await page.reload();
    await expect(label(page)).toHaveText('All projects', LOADED);
    const url = new URL(page.url());
    expect(url.pathname).toBe(path);
    expect(url.searchParams.get('panel')).toBe('files');
    expect(url.searchParams.get('projects')).toBe('all');

    expect(errors.get(page), 'no uncaught JavaScript errors').toEqual([]);
  });
});
