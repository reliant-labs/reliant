import { test, type Page } from '@playwright/test';

test.use({ ignoreHTTPSErrors: true });
test.setTimeout(90000);

async function screenshot(page: Page, name: string) {
  await page.screenshot({ path: `e2e/screenshots/${name}.png` });
}

async function setupApp(page: Page) {
  await page.goto('/');
  await page.waitForTimeout(3000);
  const pi = page.locator('[data-testid="project-item"]').filter({ hasText: 'reliant' }).first();
  if (await pi.isVisible({ timeout: 8000 }).catch(() => false)) {
    await pi.click();
    await page.waitForTimeout(5000);
  }
  const dlg = page.locator('[role="dialog"][aria-modal="true"]');
  if (await dlg.isVisible({ timeout: 3000 }).catch(() => false)) {
    await page.keyboard.press('Escape');
    await page.waitForTimeout(1000);
  }
  await page.locator('[data-onboarding="workflow-button"]').click();
  await page.waitForTimeout(2000);
}

async function clickCanvasNode(page: Page, dataId: string) {
  const node = page.locator(`[data-id="${dataId}"]`);
  // First try to use "Fit to View" to make sure everything is visible
  const fitBtn = page.locator('button[aria-label="Fit to View"], button:has-text("Fit to View")').first();
  if (await fitBtn.isVisible({ timeout: 2000 }).catch(() => false)) {
    await fitBtn.click();
    await page.waitForTimeout(500);
  }
  // Scroll into view and click with force
  await node.scrollIntoViewIfNeeded();
  await node.click({ force: true });
  await page.waitForTimeout(1500);
}

test('FULL: verify call_llm config panel features', async ({ page }) => {
  await setupApp(page);

  // Open parallel-compete workflow (has call_llm at top level)
  const pcCard = page.locator('div[class*="cursor-pointer"]').filter({ hasText: /parallel-compete/ }).first();
  await pcCard.click();
  await page.waitForTimeout(3000);
  await screenshot(page, 'f01-parallel-compete');

  // Click improve_prompt (call_llm node) - force click since it may be outside viewport
  await clickCanvasNode(page, 'improve_prompt');
  await screenshot(page, 'f02-call-llm-config');
  
  console.log('\n============ CALL_LLM CONFIG PANEL VERIFICATION ============');
  
  // 1. Field labels
  const labels = await page.locator('label').allInnerTexts();
  console.log('1. FIELD LABELS:', JSON.stringify(labels));
  const hasModel = labels.some(t => /model/i.test(t.trim()));
  const hasTemp = labels.some(t => /temperature/i.test(t));
  const hasSysPrompt = labels.some(t => /system.?prompt/i.test(t));
  console.log(`   Model: ${hasModel ? 'PASS' : 'FAIL'}`);
  console.log(`   Temperature: ${hasTemp ? 'PASS' : 'FAIL'}`);
  console.log(`   System prompt: ${hasSysPrompt ? 'PASS' : 'FAIL'}`);
  
  // 2. Category grouping (uppercase headers)
  const catHeaders = await page.locator('p[class*="uppercase"]').allInnerTexts();
  console.log(`2. CATEGORY HEADERS: ${catHeaders.length > 0 ? 'PASS' : 'FAIL'} ${JSON.stringify(catHeaders)}`);
  
  // 3. Basic/Advanced split
  const advBtn = page.locator('button').filter({ hasText: /Advanced/ });
  const hasAdv = await advBtn.isVisible().catch(() => false);
  let advBadge = 'N/A';
  if (hasAdv) {
    advBadge = await advBtn.locator('span[class*="rounded"]').innerText().catch(() => 'none');
  }
  console.log(`3. BASIC/ADVANCED SPLIT: ${hasAdv ? 'PASS' : 'FAIL'} badge=${advBadge}`);
  
  // 4. Field types
  // Model picker (should be a ModelDropdown select or CEL input)
  const selects = page.locator('select');
  const selCount = await selects.count();
  console.log(`4a. SELECT ELEMENTS: ${selCount}`);
  for (let i = 0; i < selCount; i++) {
    const id = await selects.nth(i).getAttribute('id').catch(() => '');
    const opts = await selects.nth(i).locator('option').count();
    console.log(`    select #${id}: ${opts} options`);
  }
  
  const textareas = await page.locator('textarea').count();
  console.log(`4b. TEXTAREAS: ${textareas}`);
  
  const toggles = await page.locator('[role="switch"]').count();
  console.log(`4c. TOGGLES: ${toggles}`);
  
  const textInputs = await page.locator('input[type="text"], input:not([type])').count();
  console.log(`4d. TEXT INPUTS: ${textInputs}`);
  
  // 5. CEL badges
  const celBadges = await page.locator('span').filter({ hasText: /^CEL$/ }).count();
  console.log(`5. CEL BADGES: ${celBadges > 0 ? 'PASS' : 'FAIL'} count=${celBadges}`);
  
  // 5b. Mode toggles (dropdown/CEL switcher)
  const modeToggles = await page.locator('button[title="Use dropdown"], button[title="Use CEL expression"]').count();
  console.log(`5b. MODE TOGGLES: ${modeToggles}`);
  
  // 6. Help text
  const helpTexts = await page.locator('p[class*="text-xs"]').allInnerTexts();
  console.log(`6. HELP TEXTS: ${helpTexts.length}`);
  helpTexts.slice(0, 5).forEach((h, i) => console.log(`    [${i}]: "${h.substring(0, 120)}"`));
  
  // 7. Available Outputs
  const outputsBtn = page.locator('button').filter({ hasText: /Available Outputs/ });
  const hasOutputs = await outputsBtn.isVisible().catch(() => false);
  console.log(`7. AVAILABLE OUTPUTS: ${hasOutputs ? 'PASS' : 'FAIL'}`);
  
  if (hasOutputs) {
    const badge = await outputsBtn.locator('span[class*="rounded"]').innerText().catch(() => '0');
    console.log(`   Fields count badge: ${badge}`);
    await outputsBtn.click();
    await page.waitForTimeout(500);
    
    const copyBtns = page.locator('button[title="Copy CEL path"]');
    const pathCount = await copyBtns.count();
    console.log(`   Copy buttons: ${pathCount}`);
    for (let i = 0; i < Math.min(pathCount, 8); i++) {
      console.log(`   path: "${await copyBtns.nth(i).innerText().catch(() => '')}"`);
    }
    await screenshot(page, 'f03-outputs-expanded');
  }
  
  // Expand advanced if present
  if (hasAdv) {
    await advBtn.click();
    await page.waitForTimeout(500);
    const advLabels = await page.locator('label').allInnerTexts();
    console.log(`\n   LABELS AFTER ADVANCED EXPAND: ${JSON.stringify(advLabels)}`);
    await screenshot(page, 'f04-advanced-expanded');
  }
  
  // Full panel screenshot
  await screenshot(page, 'f05-full-panel');
  
  console.log('============ END VERIFICATION ============\n');
});

test('FULL: verify settings panel tabs', async ({ page }) => {
  await setupApp(page);

  const agentCard = page.locator('div[class*="cursor-pointer"]').filter({ hasText: /^agent/ }).first();
  await agentCard.click();
  await page.waitForTimeout(2000);

  // Click Settings button using aria-label
  const settingsBtn = page.locator('button[aria-label="Settings"]');
  if (await settingsBtn.isVisible({ timeout: 5000 }).catch(() => false)) {
    await settingsBtn.click();
    await page.waitForTimeout(1000);
    await screenshot(page, 'f10-settings');
    
    // Get all content of the settings panel
    const panelHeadings = await page.locator('h2, h3').allInnerTexts();
    console.log('Panel headings:', panelHeadings);
    
    // Look for tab-like buttons
    const allBtns = await page.locator('button').allInnerTexts();
    console.log('All buttons:', allBtns.filter(t => t.length > 0 && t.length < 30));
    
    // Check for param-related content
    const paramContent = page.locator('text=/param/i');
    console.log('Param-related elements:', await paramContent.count());
  } else {
    console.log('Settings button not found');
    // List all buttons for debug
    const allBtns = page.locator('button');
    for (let i = 0; i < Math.min(await allBtns.count(), 30); i++) {
      const aria = await allBtns.nth(i).getAttribute('aria-label').catch(() => '');
      if (aria) console.log(`  btn[${i}]: aria="${aria}"`);
    }
  }
});
