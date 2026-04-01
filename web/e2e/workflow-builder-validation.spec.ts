import { test as base, expect, type ConsoleMessage, chromium } from '@playwright/test';

const consoleErrors: string[] = [];

const test = base.extend({});

test.describe.serial('Workflow Builder Final Validation', () => {
  test.setTimeout(120000);

  test('Full validation flow', async () => {
    const browser = await chromium.launch({
      args: ['--ignore-certificate-errors'],
    });
    const context = await browser.newContext({
      ignoreHTTPSErrors: true,
      viewport: { width: 1400, height: 900 },
    });
    const page = await context.newPage();

    page.on('console', (msg: ConsoleMessage) => {
      if (msg.type() === 'error') {
        consoleErrors.push(`[console.error] ${msg.text()}`);
      }
    });
    page.on('pageerror', (err) => {
      consoleErrors.push(`[pageerror] ${err.message}`);
    });

    try {
      // ========================================
      // STEP 0: Load and select project
      // ========================================
      console.log('=== STEP 0: Loading app ===');
      await page.goto('http://localhost:3046/');
      // Don't wait for networkidle - it may never resolve due to gRPC streaming
      await page.waitForLoadState('domcontentloaded');
      await page.waitForTimeout(4000);
      await page.screenshot({ path: 'web/e2e/screenshots/00-initial-load.png', fullPage: true });

      // Select the "builder" project from the welcome screen
      const builderProject = page.locator('button:has-text("builder")').first();
      if (await builderProject.isVisible().catch(() => false)) {
        console.log('  Selecting "builder" project');
        await builderProject.click();
        await page.waitForTimeout(4000);
      }
      await page.screenshot({ path: 'web/e2e/screenshots/00b-after-project-select.png', fullPage: true });

      // ========================================
      // STEP 1: Navigate to one-ring workflow
      // ========================================
      console.log('\n=== STEP 1: Open one-ring workflow ===');

      // Find workflows button
      let wfClicked = false;
      const wfSelectors = [
        'button[data-onboarding="workflow-button"]',
        'button[aria-label="Open Workflows"]',
      ];
      for (const sel of wfSelectors) {
        const btn = page.locator(sel).first();
        if (await btn.isVisible().catch(() => false)) {
          await btn.click();
          wfClicked = true;
          console.log(`  Clicked workflows via: ${sel}`);
          break;
        }
      }
      if (!wfClicked) {
        // Dump buttons for debugging
        const btns = page.locator('button:visible');
        const count = await btns.count();
        console.log(`  Visible buttons: ${count}`);
        for (let i = 0; i < Math.min(count, 25); i++) {
          const t = await btns.nth(i).innerText().catch(() => '');
          const a = await btns.nth(i).getAttribute('aria-label').catch(() => '');
          const d = await btns.nth(i).getAttribute('data-onboarding').catch(() => '');
          if (t.trim() || a || d) console.log(`    btn[${i}] text="${t.trim().substring(0, 50)}" aria="${a}" data="${d}"`);
        }
        throw new Error('Workflows button not found');
      }

      await page.waitForTimeout(3000);
      await page.screenshot({ path: 'web/e2e/screenshots/01-workflows-list.png', fullPage: true });

      // Click "one-ring" workflow
      const oneRingLink = page.locator('text=one-ring').first();
      await expect(oneRingLink).toBeVisible({ timeout: 10000 });
      await oneRingLink.click();
      await page.waitForTimeout(4000);
      await page.screenshot({ path: 'web/e2e/screenshots/02-one-ring-workflow.png', fullPage: true });

      // Count nodes
      const nodes = page.locator('.react-flow__node');
      await expect(nodes.first()).toBeVisible({ timeout: 10000 });
      const nodeCount = await nodes.count();
      console.log(`  Visible nodes: ${nodeCount}`);
      for (let i = 0; i < nodeCount; i++) {
        const text = await nodes.nth(i).innerText().catch(() => '(no text)');
        console.log(`    Node ${i}: ${text.replace(/\n/g, ' | ').substring(0, 120)}`);
      }

      const switchNodes = page.locator('.react-flow__node:has-text("Switch")');
      const switchCount = await switchNodes.count();
      console.log(`  Switch nodes: ${switchCount}`);
      console.log(`  TEST 1 NODE COUNT: ${nodeCount === 6 ? 'PASS' : 'FAIL'} (expected 6, got ${nodeCount})`);
      console.log(`  TEST 1 NO SWITCH: ${switchCount === 0 ? 'PASS' : 'FAIL'}`);

      // ========================================
      // STEP 2: Planning node expand
      // ========================================
      console.log('\n=== STEP 2: Planning node expand ===');
      const planningNode = page.locator('.react-flow__node:has-text("planning")').first();
      await expect(planningNode).toBeVisible();

      const planBtns = planningNode.locator('button');
      const planBtnCount = await planBtns.count();
      console.log(`  Buttons in planning node: ${planBtnCount}`);

      let expandClicked = false;
      for (let i = 0; i < planBtnCount; i++) {
        const btn = planBtns.nth(i);
        const html = await btn.innerHTML();
        const ariaLabel = (await btn.getAttribute('aria-label')) || '';
        const title = (await btn.getAttribute('title')) || '';
        console.log(`    btn[${i}] aria="${ariaLabel}" title="${title}" html_preview="${html.substring(0, 100)}"`);
        if (html.includes('maximize') || html.includes('Maximize') ||
            ariaLabel.toLowerCase().includes('expand') || ariaLabel.toLowerCase().includes('enter') ||
            ariaLabel.toLowerCase().includes('inline') ||
            title.toLowerCase().includes('expand') || title.toLowerCase().includes('enter') ||
            title.toLowerCase().includes('inline') ||
            html.includes('polyline') || html.includes('M15 3') || html.includes('M4 14')) {
          await btn.click();
          expandClicked = true;
          console.log(`    Clicked: btn[${i}]`);
          break;
        }
      }
      if (!expandClicked && planBtnCount > 0) {
        console.log('  No expand button matched, trying first button');
        await planBtns.first().click();
      }

      await page.waitForTimeout(2500);
      await page.screenshot({ path: 'web/e2e/screenshots/03-planning-expanded.png', fullPage: true });

      const inlineNodes = page.locator('.react-flow__node');
      const inlineCount = await inlineNodes.count();
      console.log(`  Nodes after expand: ${inlineCount}`);
      for (let i = 0; i < inlineCount; i++) {
        const text = await inlineNodes.nth(i).innerText().catch(() => '');
        console.log(`    Node ${i}: ${text.replace(/\n/g, ' | ').substring(0, 120)}`);
      }
      const expandedText = await page.locator('body').innerText();
      const hasCriticize = expandedText.includes('criticize');
      const hasRevise = expandedText.includes('revise');
      console.log(`  TEST 2 INLINE: ${hasCriticize && hasRevise ? 'PASS' : 'FAIL'} (criticize=${hasCriticize}, revise=${hasRevise})`);

      // Go back to parent
      const backBtn = page.locator('button[aria-label*="back" i], button[aria-label*="Back"], button:has-text("Back")').first();
      if (await backBtn.isVisible().catch(() => false)) {
        await backBtn.click();
        console.log('  Clicked back');
      } else {
        await page.keyboard.press('Escape');
        console.log('  Pressed Escape');
      }
      await page.waitForTimeout(2000);
      await page.screenshot({ path: 'web/e2e/screenshots/04-back-to-parent.png', fullPage: true });

      // ========================================
      // STEP 3: Config panel
      // ========================================
      console.log('\n=== STEP 3: Config panel ===');
      const planningNode2 = page.locator('.react-flow__node:has-text("planning")').first();
      if (await planningNode2.isVisible().catch(() => false)) {
        await planningNode2.click();
        await page.waitForTimeout(2000);
        await page.screenshot({ path: 'web/e2e/screenshots/05-config-panel.png', fullPage: true });

        const configText = await page.locator('body').innerText();
        const hasInline = configText.includes('Inline');
        const has3Nodes = configText.includes('3 nodes');
        const has2Edges = configText.includes('2 edges');
        console.log(`  Inline: ${hasInline}, 3 nodes: ${has3Nodes}, 2 edges: ${has2Edges}`);
        console.log(`  TEST 3 INLINE TAB: ${hasInline ? 'PASS' : 'FAIL'}`);
        console.log(`  TEST 3 SUMMARY: ${has3Nodes && has2Edges ? 'PASS' : 'FAIL'}`);
      } else {
        console.log('  Planning node not visible after back nav');
        console.log(`  TEST 3: FAIL`);
      }

      // ========================================
      // STEP 4: Parameters tab
      // ========================================
      console.log('\n=== STEP 4: Parameters tab ===');
      let paramsDone = false;
      // Try with node selected
      let paramsBtn = page.locator('button:has-text("Parameters")').first();
      if (!(await paramsBtn.isVisible().catch(() => false))) {
        // Deselect node
        await page.keyboard.press('Escape');
        await page.waitForTimeout(500);
        paramsBtn = page.locator('button:has-text("Parameters")').first();
      }
      if (await paramsBtn.isVisible().catch(() => false)) {
        await paramsBtn.click();
        await page.waitForTimeout(1500);
        await page.screenshot({ path: 'web/e2e/screenshots/06-parameters.png', fullPage: true });

        const pText = await page.locator('body').innerText();
        const hasMaxRetries = pText.includes('max_retries');
        const hasModel = pText.includes('model');
        console.log(`  max_retries: ${hasMaxRetries}, model: ${hasModel}`);
        console.log(`  TEST 4 PARAMS: ${hasMaxRetries || hasModel ? 'PASS' : 'FAIL'}`);
        paramsDone = true;
      }
      if (!paramsDone) {
        console.log('  Parameters button not found');
        // Show all buttons for debugging
        const btns = page.locator('button:visible');
        const count = await btns.count();
        for (let i = 0; i < Math.min(count, 30); i++) {
          const t = await btns.nth(i).innerText().catch(() => '');
          if (t.trim()) console.log(`    btn: "${t.trim().substring(0, 50)}"`);
        }
        console.log(`  TEST 4 PARAMS: FAIL`);
      }

      // ========================================
      // STEP 5: Agent workflow + YAML
      // ========================================
      console.log('\n=== STEP 5: Agent workflow + YAML ===');
      // Navigate back to workflow list
      const backBtn2 = page.locator('button:has-text("Back"), [aria-label*="back" i]').first();
      if (await backBtn2.isVisible().catch(() => false)) {
        await backBtn2.click();
        await page.waitForTimeout(2000);
      }
      await page.screenshot({ path: 'web/e2e/screenshots/07-workflows-list-2.png', fullPage: true });

      // Look for agent workflow
      const agentLink = page.locator('text=/my-agent/i').first();
      if (await agentLink.isVisible().catch(() => false)) {
        const lt = await agentLink.innerText();
        console.log(`  Found agent link: "${lt}"`);
        await agentLink.click();
        await page.waitForTimeout(3000);
        await page.screenshot({ path: 'web/e2e/screenshots/08-agent-workflow.png', fullPage: true });

        const nodeTexts = await page.locator('.react-flow__node').allInnerTexts();
        const hasLoop = nodeTexts.some(t => t.includes('agent_loop'));
        console.log(`  agent_loop node: ${hasLoop}`);

        const yamlBtn = page.locator('button:has-text("YAML")').first();
        if (await yamlBtn.isVisible().catch(() => false)) {
          await yamlBtn.click();
          await page.waitForTimeout(2000);
          await page.screenshot({ path: 'web/e2e/screenshots/09-yaml-view.png', fullPage: true });

          const yamlText = await page.locator('body').innerText();
          const hasCharArray = yamlText.includes('[a, g, e, n, t');
          console.log(`  Character array (BAD): ${hasCharArray}`);
          console.log(`  TEST 5 YAML: ${!hasCharArray ? 'PASS' : 'FAIL'}`);
        } else {
          console.log(`  TEST 5 YAML: SKIP (no YAML button)`);
        }
      } else {
        console.log(`  No my-agent workflow found`);
        // List what workflows are visible
        const wfItems = page.locator('[class*="workflow"], [class*="list-item"]');
        const wfTexts = await wfItems.allInnerTexts();
        console.log(`  Available items: ${wfTexts.join(', ').substring(0, 300)}`);
        console.log(`  TEST 5 YAML: SKIP`);
      }

      // ========================================
      // STEP 6: Console errors
      // ========================================
      console.log('\n=== STEP 6: Console errors ===');
      console.log(`Total console errors: ${consoleErrors.length}`);
      const criticalErrors = consoleErrors.filter(e =>
        !e.includes('favicon') &&
        !e.includes('net::ERR') &&
        !e.includes('Failed to load resource') &&
        !e.includes('Proxyman') &&
        !e.includes('websocket') &&
        !e.includes('WebSocket') &&
        !e.includes('CERT_AUTHORITY')
      );
      console.log(`Critical JS errors: ${criticalErrors.length}`);
      for (const err of criticalErrors) {
        console.log(`  ${err.substring(0, 300)}`);
      }
      console.log(`  TEST 6 CONSOLE: ${criticalErrors.length === 0 ? 'PASS' : 'FAIL'}`);

      console.log('\n=== DONE ===');

    } finally {
      await browser.close();
    }
  });
});
