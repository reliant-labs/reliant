import { expect, test, type Page, type Route } from '@playwright/test';

import { DEPLOY_CHAT, mockAppsBackend } from './appsMocks';

// The chat's Trace view (dev-docs/design/agent-analytics.md §2, B3), in a real
// browser against a mocked backend (appsMocks.ts): the header's Trace tab, the
// owner's waterfall and cost, a viewer's shape with no dollars, a PARTIAL
// banner in words, and an outsider who is told nothing about the chat.
//
// ChatTraceService answers in protojson, as the server does: int64 as
// strings, enums by name, timestamps as RFC 3339.

const T0 = Date.parse('2026-11-24T12:00:00Z');
const at = (ms: number) => new Date(T0 + ms).toISOString();
const ns = (ms: number) => String(ms * 1_000_000);

const SPANS = [
  { spanId: 's-run', name: 'RunWorkflow:DynamicWorkflow', kind: 'Internal', serviceName: 'reliant-worker', startTime: at(0), durationNs: ns(9000), statusCode: 'Ok' },
  { spanId: 's-node', parentSpanId: 's-run', name: 'workflow.node agent', kind: 'Internal', serviceName: 'reliant-worker', startTime: at(100), durationNs: ns(8800), statusCode: 'Unset' },
  { spanId: 's-turn', parentSpanId: 's-node', name: 'invoke_agent main', kind: 'Internal', serviceName: 'reliant-worker', startTime: at(200), durationNs: ns(8000), statusCode: 'Unset' },
  {
    spanId: 's-chat', parentSpanId: 's-turn', name: 'chat claude-sonnet-5', kind: 'Client', serviceName: 'reliant-worker', startTime: at(300), durationNs: ns(4000), statusCode: 'Ok',
    attributes: { 'gen_ai.request.model': 'claude-sonnet-5', 'gen_ai.usage.input_tokens': '1200', 'gen_ai.usage.output_tokens': '300', 'forge.ai.cost.usd': '0.0123' },
    events: [{ name: 'gen_ai.first_token', time: at(900) }],
  },
  { spanId: 's-tool', parentSpanId: 's-turn', name: 'execute_tool shell', kind: 'Internal', serviceName: 'reliant-worker', startTime: at(4500), durationNs: ns(2500), statusCode: 'Error', attributes: { 'gen_ai.tool.name': 'shell' } },
];

const CALLS = [
  { id: 'call-1', attempt: 1, time: at(300), purpose: 'chat', provider: 'anthropic', model: 'claude-sonnet-5', outcome: 'success', inputTokens: '1200', outputTokens: '300', cacheReadTokens: '800', latencyMs: '4000', ttftMs: '600', authMode: 'managed', costUsdNanos: '12300000', costBasis: 'reported' },
  { id: 'call-2', attempt: 1, time: at(7000), purpose: 'title', provider: 'openai', model: 'gpt-5-mini', outcome: 'error', errorClass: 'rate_limit', inputTokens: '50', latencyMs: '200', authMode: 'byok', costUsdNanos: '2000000', costBasis: 'estimated' },
  { id: 'call-3', attempt: 1, time: at(8000), purpose: 'chat', provider: 'anthropic', model: 'claude-opus-5-5', outcome: 'success', inputTokens: '10', outputTokens: '10', latencyMs: '100', authMode: 'subscription', costBasis: 'unpriced' },
];

const OWNER = {
  chatId: DEPLOY_CHAT.id,
  completeness: 'CHAT_TRACE_COMPLETENESS_COMPLETE',
  spans: SPANS.map((span) => ({ traceId: 't-1', ...span })),
  modelCalls: CALLS,
  totals: {
    calls: '3', errorCalls: '1', inputTokens: '1260', outputTokens: '310', cacheReadTokens: '800', latencyMsTotal: '4300',
    reportedCostUsdNanos: '12300000', reportedCalls: '1', estimatedCostUsdNanos: '2000000', estimatedCalls: '1', unpricedCalls: '1',
  },
  asOf: at(60_000),
};

// What the server answers a viewer (internal/handlers/chat_trace): no trace
// id, no cost, no ids, no auth mode.
const VIEWER = {
  chatId: DEPLOY_CHAT.id,
  completeness: 'CHAT_TRACE_COMPLETENESS_COMPLETE',
  viewer: true,
  spans: SPANS.map((span) => ({
    ...span,
    attributes: Object.fromEntries(Object.entries(span.attributes ?? {}).filter(([key]) => key.startsWith('gen_ai.'))),
  })),
  modelCalls: CALLS.map(({ id: _id, authMode: _auth, costUsdNanos: _cost, costBasis: _basis, ...call }) => call),
  totals: { calls: '3', errorCalls: '1', inputTokens: '1260', outputTokens: '310', cacheReadTokens: '800', latencyMsTotal: '4300' },
  asOf: at(60_000),
};

const json = (route: Route, body: unknown) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
const notFound = (route: Route) =>
  route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ code: 'not_found', message: 'chat not found' }) });

async function openTrace(page: Page) {
  await page.goto(`/c/${DEPLOY_CHAT.id}`, { waitUntil: 'domcontentloaded' });
  const tab = page.getByRole('tab', { name: 'Trace' });
  await expect(tab).toBeVisible({ timeout: 20_000 });
  await tab.click();
  await expect(page.getByTestId('chat-trace-panel')).toBeVisible();
}

test.describe('Chat Trace view', () => {
  test.use({ viewport: { width: 1440, height: 1000 } });

  test('the owner reads the waterfall, reported and estimated cost apart, and every call with its cost', async ({ page }) => {
    await mockAppsBackend(page);
    await page.route('**/reliant.v1.ChatTraceService/GetChatTrace', (route) => json(route, OWNER));
    await openTrace(page);

    const report = page.getByTestId('chat-trace-report');
    await expect(report).toHaveAttribute('data-viewer', 'false');
    await expect(page.getByTestId('trace-waterfall-summary')).toContainText('5 spans · 1 error');
    await expect(page.getByTestId('trace-span-row')).toHaveCount(5);
    await expect(page.getByTestId('chat-trace-cost-reported')).toContainText('$0.0123');
    await expect(page.getByTestId('chat-trace-cost-estimated')).toContainText('~$0.0020');
    await expect(page.getByTestId('chat-trace-cost-unpriced')).toContainText('33%');
    await expect(page.getByTestId('chat-trace-call-cost')).toHaveText(['$0.0123', '~$0.0020', 'Unpriced']);
    await page.locator('[data-testid="trace-span-row"][data-span-id="s-chat"]').click();
    await expect(page.getByTestId('chat-trace-span-facts')).toContainText('$0.0123');
    await page.screenshot({ path: test.info().outputPath('trace-owner.png'), fullPage: true });

    // "Chat" brings the transcript and composer back.
    await page.getByRole('tab', { name: 'Chat' }).click();
    await expect(page.getByTestId('chat-trace-panel')).toHaveCount(0);
  });

  test('a viewer of a shared chat reads structure, timing, models and tokens, and no dollars', async ({ page }) => {
    await mockAppsBackend(page);
    await page.route('**/reliant.v1.ChatService/GetChat', (route) =>
      json(route, { chat: { ...DEPLOY_CHAT, userId: 'user_owner', viewerAccess: true, visibility: 'RESOURCE_VISIBILITY_ORG', orgId: 'org_acme' } }),
    );
    await page.route('**/reliant.v1.ChatTraceService/GetChatTrace', (route) => json(route, VIEWER));
    await openTrace(page);

    const report = page.getByTestId('chat-trace-report');
    await expect(report).toHaveAttribute('data-viewer', 'true');
    await expect(page.getByTestId('trace-span-row')).toHaveCount(5);
    await expect(page.getByText('gpt-5-mini')).toBeVisible();
    await expect(page.getByTestId('chat-trace-viewer-note')).toBeVisible();
    await expect(page.getByTestId('chat-trace-cost')).toHaveCount(0);
    await page.locator('[data-testid="trace-span-row"][data-span-id="s-chat"]').click();
    await expect(page.getByTestId('chat-trace-span-facts')).toContainText('claude-sonnet-5');
    expect(await page.getByTestId('chat-trace-panel').innerText()).not.toMatch(/\$/);
    await page.screenshot({ path: test.info().outputPath('trace-viewer.png'), fullPage: true });
  });

  test('a partial trace names its reasons in words and still lists the model calls', async ({ page }) => {
    await mockAppsBackend(page);
    await page.route('**/reliant.v1.ChatTraceService/GetChatTrace', (route) =>
      json(route, {
        ...OWNER,
        spans: [],
        completeness: 'CHAT_TRACE_COMPLETENESS_PARTIAL',
        partialReasons: ['CHAT_TRACE_PARTIAL_REASON_SPANS_UNAVAILABLE', 'CHAT_TRACE_PARTIAL_REASON_CALLS_TRUNCATED'],
      }),
    );
    await openTrace(page);

    const banner = page.getByTestId('chat-trace-partial');
    await expect(banner).toContainText('Partial trace');
    await expect(banner).toContainText("Timing couldn't be loaded right now.");
    await expect(banner).toContainText('only the earliest are listed');
    await expect(page.getByTestId('chat-trace-no-spans')).toBeVisible();
    await expect(page.getByTestId('chat-trace-call-cost')).toHaveCount(3);
    await page.screenshot({ path: test.info().outputPath('trace-partial.png'), fullPage: true });
  });

  test('an outsider is told the trace is not available, and nothing about it', async ({ page }) => {
    await mockAppsBackend(page);
    await page.route('**/reliant.v1.ChatTraceService/GetChatTrace', notFound);
    await openTrace(page);

    await expect(page.getByTestId('chat-trace-unavailable')).toContainText("isn't available to you");
    await expect(page.getByTestId('chat-trace-report')).toHaveCount(0);
    await expect(page.getByRole('button', { name: /try again|refresh/i })).toHaveCount(0);
    await page.screenshot({ path: test.info().outputPath('trace-outsider.png'), fullPage: true });
  });
});
