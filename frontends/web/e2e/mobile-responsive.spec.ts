import { test, expect } from '@playwright/test';
import { mockMobileBackend } from './mobileMocks';
import { COMPOSER_WORKFLOWS } from './helpers/composerFixtures';
import { expectNoPageOverflow, expectReachable, responsiveViewports } from './helpers/responsive';

test('composer wire fixtures retain required inputs, toolbar controls and tagged groups', () => {
  const workflow = (name: string) => COMPOSER_WORKFLOWS.find(item => item.workflow.name === name)!.workflow;
  expect(workflow('agent').inputs).toMatchObject({ mode: { enumInput: { enumValues: ['manual', 'auto', 'plan'], base: { ui: 'toolbar' } } } });
  expect(workflow('review-request').inputs).toMatchObject({ repository: { stringInput: { base: { required: true, example: 'reliant-labs/reliant' } } } });
  expect(workflow('parallel-compete').inputs).toMatchObject({ implementer: { groupInput: { presets: { tag: 'agent', default: 'general' } } } });
  expect(workflow('build-workflow').inputs).toMatchObject({ ask: { booleanInput: { default: true, base: { ui: 'toolbar' } } } });
});

// Required rendered-route coverage: no dev harness and no conditional skips.
// RPC fixtures isolate layout from services; this is not a native keyboard test.
for (const route of ['/m/new', '/m/chats/chat_a']) {
  test(`composer draft survives resize and options dismissal on ${route}`, async ({ page }, info) => {
    await mockMobileBackend(page);
    // Start compact even in the desktop project, then exercise wide/short windows.
    await page.setViewportSize(responsiveViewports.phone);
    await page.goto(route);
    const composer = page.getByRole('textbox', { name: 'Message the agent', exact: true });
    await expect(composer).toBeEditable();
    const draft = 'Keep this draft while rotating and opening chat options.';
    await composer.fill(draft);
    for (const viewport of [responsiveViewports.wide, responsiveViewports.splitPane,
      { width: 390, height: 460 }, responsiveViewports.phone]) {
      await page.setViewportSize(viewport);
      await expect(page).toHaveURL(new RegExp(`${route}$`));
      await expect(page.locator('[data-surface="mobile"]').first()).toBeVisible();
      await expect(composer).toHaveValue(draft);
      await expectNoPageOverflow(page);
      await expectReachable(composer, !!info.project.use.hasTouch);
    }
    const add = page.getByRole('button', { name: 'Add', exact: true });
    await expectReachable(add);
    await add.click();
    const sheet = page.getByRole('dialog', { name: 'Chat options' });
    await expect(sheet).toBeVisible();
    await expectNoPageOverflow(page);
    await page.keyboard.press('Escape');
    await expect(sheet).toBeHidden();
    await expect(composer).toHaveValue(draft);
    await expect(add).toBeFocused();
  });
}
test('Chat options expose canonical agent settings and editable thinking levels', async ({ page }) => {
  await mockMobileBackend(page);
  await page.goto('/m/new');
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await expect(page.getByRole('dialog', { name: 'Chat options' })).toBeVisible();
  await expect(page.getByText('Agent', { exact: true }).first()).toBeVisible();
  await page.getByRole('button', { name: /^All settings/ }).click();
  await expect(page.getByText('Execution Mode', { exact: true })).toBeVisible();
  for (const mode of ['manual', 'auto', 'plan']) {
    await expect(page.getByRole('button', { name: mode, exact: true })).toBeVisible();
  }
  await expect(page.getByRole('button', { name: /^Preset/ })).toBeVisible();
  await page.getByRole('dialog').getByRole('button', { name: /^Model / }).click();
  const thinking = page.getByRole('combobox');
  await expect(thinking).toBeVisible();
  await expect(thinking.locator('option')).toHaveText(['Auto (high)', 'Low', 'Medium', 'High', 'X-High']);
  await thinking.selectOption('high');
  await expect(thinking).toHaveValue('high');
  await page.getByRole('button', { name: 'Close settings', exact: true }).click();
  await page.getByRole('button', { name: 'Model settings', exact: true }).click();
  await expect(page.getByRole('combobox')).toHaveValue('high');
  await page.getByRole('button', { name: 'Close settings', exact: true }).click();
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await page.getByRole('button', { name: /^All settings/ }).click();
  await page.getByRole('button', { name: /^Preset/ }).click();
  await page.getByRole('button', { name: /^researcher/ }).click();
  await expect(page.getByRole('button', { name: 'Preset researcher', exact: true })).toBeVisible();
  await expectNoPageOverflow(page);
});

test('Inbox selection survives rotation and split-pane resizing', async ({ page }, info) => {
  const mock = await mockMobileBackend(page);
  await page.goto('/m/inbox');
  const item = page.getByTestId('inbox-item-question:q_1');
  await expect(item).toBeVisible();
  const answer = item.getByRole('button', { name: /Answer/ });
  await expectReachable(answer, !!info.project.use.hasTouch);
  await answer.click();
  const choice = item.getByRole('radio', { name: /Read replica/ });
  await choice.click();
  await expect(choice).toBeChecked();
  const initial = page.viewportSize()!;
  for (const viewport of [
    { width: initial.height, height: initial.width },
    responsiveViewports.splitPane,
    responsiveViewports.narrow,
    initial,
  ]) {
    await page.setViewportSize(viewport);
    await expect(choice).toBeChecked();
    await expectNoPageOverflow(page);
    await expectReachable(item.getByRole('button', { name: /Submit/ }), !!info.project.use.hasTouch);
  }
  await item.getByRole('button', { name: /Submit/ }).click();
  await expect.poll(() => mock.answered.length).toBe(1);
  expect(mock.answered[0]).toMatchObject({ questionId: 'q_1', action: 'reply' });
});

test('navigation dialog traps focus, closes on Escape, and restores focus', async ({ page }, info) => {
  await mockMobileBackend(page);
  await page.goto('/m/chats');
  const open = page.getByRole('button', { name: /Open menu/ });
  await expectReachable(open, !!info.project.use.hasTouch);
  await open.click();
  const dialog = page.getByRole('dialog', { name: 'Navigation' });
  await expect(dialog).toBeVisible();
  await expect.poll(() => dialog.evaluate(el => el.contains(document.activeElement))).toBe(true);
  const count = await dialog.locator('a[href], button, input, select, textarea, [tabindex="0"]').count();
  for (let i = 0; i <= count; i++) {
    await page.keyboard.press('Tab');
    await expect.poll(() => dialog.evaluate(el => el.contains(document.activeElement))).toBe(true);
  }
  await expectNoPageOverflow(page);
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(open).toBeFocused();
  await open.click();
  const inbox = dialog.getByRole('link', { name: /^Inbox/ });
  await expectReachable(inbox, !!info.project.use.hasTouch);
  await inbox.click();
  await expect(page.getByRole('heading', { name: 'Inbox', exact: true })).toBeVisible();
  await expectNoPageOverflow(page);
});
