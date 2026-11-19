import { expect, test } from '@playwright/test';

import { PROJECT, mockAppsBackend } from './appsMocks';

// The Monaco editor is a lazy chunk of this build (lib/monacoBundle), with its
// workers, styles and fonts served from our own origin. It used to come from
// the jsdelivr CDN, and when a CI runner blocked that CDN
// (net::ERR_BLOCKED_BY_RESPONSE) every desktop page raised an uncaught error
// and no editor could open. So: with every jsdelivr request blocked, a page
// that renders an editor mounts it, asks jsdelivr for nothing, and raises no
// uncaught error and no Content-Security-Policy violation.
//
// The editor here is the workflow builder's Outputs editor (a MonacoCELEditor),
// on a user workflow answered by the mock below.

const WORKFLOW = 'e2e-monaco';

// lib/monacoManager exposes itself on window for debugging.
type MonacoDebugWindow = {
  __monacoManager?: {
    current(): {
      editor: {
        createModel(value: string, language: string): unknown;
        getModelMarkers(filter: { owner: string }): unknown[];
      };
    } | null;
  };
};

test('the editor mounts from our own build with the Monaco CDN blocked', async ({ page }) => {
  test.setTimeout(60_000);
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  const cspViolations: string[] = [];
  page.on('console', (message) => {
    if (message.type() === 'error' && /Content Security Policy/i.test(message.text())) cspViolations.push(message.text());
  });
  const cdnRequests: string[] = [];
  await page.route(/jsdelivr\.net/, (route) => {
    cdnRequests.push(route.request().url());
    return route.abort('blockedbyresponse');
  });
  const workers: string[] = [];
  page.on('worker', (worker) => workers.push(worker.url()));

  await mockAppsBackend(page, { person: true });
  // A user workflow (editable) with one output: the Outputs tab renders a CEL
  // editor for it.
  await page.route('**/reliant.v1.WorkflowService/GetWorkflow', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        workflow: { name: WORKFLOW, nodes: [], edges: [], outputs: { result: 'inputs.value' } },
        source: 'user',
        draftId: 'draft_1',
        version: '1',
        yamlDefinition: `name: ${WORKFLOW}\n`,
        status: 'WORKFLOW_DRAFT_STATUS_DRAFT',
      }),
    }),
  );

  await page.goto(`/workflow/${WORKFLOW}?project=${PROJECT.id}`);
  await page.locator('[data-tour="workflow-inputs"]').click({ timeout: 20_000 });
  await page.getByRole('button', { name: /^Outputs/ }).click();

  const editor = page.locator('.monaco-editor');
  await expect(editor.first()).toBeVisible({ timeout: 30_000 });
  // Monaco renders the expression's text into its view lines.
  await expect(editor.first().locator('.view-lines')).toContainText('inputs.value');

  // A language worker starts from our origin and answers: a JSON model gets
  // the JSON service's diagnostics.
  await page.evaluate(() => {
    const monaco = (window as unknown as MonacoDebugWindow).__monacoManager?.current();
    monaco?.editor.createModel('{"a": 1,}', 'json');
  });
  await expect
    .poll(() => page.evaluate(() => (window as unknown as MonacoDebugWindow).__monacoManager?.current()?.editor.getModelMarkers({ owner: 'json' }).length ?? 0), {
      message: 'the JSON worker reported the trailing comma',
      timeout: 15_000,
    })
    .toBeGreaterThan(0);
  const origin = new URL(page.url()).origin;
  expect(workers.some((url) => url.startsWith(`${origin}/`) && /json\.worker/.test(url)), `the JSON worker came from ${origin}: ${workers}`).toBe(true);

  expect(cdnRequests, 'nothing is requested from jsdelivr').toEqual([]);
  for (const url of workers) expect(url.startsWith(origin) || url.startsWith('blob:'), `worker ${url} is ours`).toBe(true);
  expect(cspViolations, 'no Content-Security-Policy violations').toEqual([]);
  expect(errors, 'no uncaught JavaScript errors').toEqual([]);
});
