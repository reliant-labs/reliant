/**
 * Chat fixtures: models, a sidebar's worth of chats (two projects, branched
 * chats, a long branch name, running / needs-input / failed / unread /
 * automation rows, an untitled chat), and one long agent run that uses every
 * tool renderer the timeline has: grep, view, edit, a failed and a long
 * shell call, skill, load_tool, a plan, web fetch, a generic MCP tool, and a
 * running sub-agent. Ported from the November review's
 * fixtures-chat-sidebars/20-chat-sidebars.js.
 *
 * Chat content arrives on reliant.v1.StreamingService/StreamChatUpdates, a
 * binary-proto stream: the snapshot below is encoded with the app's schemas.
 */
import type { Fixtures } from '../rpc'
import { DAY, FIXED_NOW, HOUR, IDS, MIN, USER, WORKTREES, iso } from './base'

export const CHAT_IDS = {
  long: 'c0ffee00-2222-4aaa-8bbb-00000000000a',
}

const model = (id: string, name: string, provider: string, tags: string[], reason: boolean) => ({
  id, name, provider, capabilities: ['text', 'tools'], contextWindow: '200000', defaultMaxTokens: '16000', canReason: reason,
  supportsAttachments: true, driverId: provider, tags, supportsTools: true, supportsCaching: true,
  supportedThinkingLevels: reason ? ['low', 'medium', 'high'] : [], supportsTemperature: !reason,
  defaultCompactionThreshold: '120000', maxCompactionThreshold: '180000',
})
const MODELS = [
  model('claude-opus-5-5', 'Claude Opus 5.5', 'anthropic', ['smart'], true),
  model('claude-sonnet-5', 'Claude Sonnet 5', 'anthropic', ['default'], true),
  model('claude-haiku-4-5', 'Claude Haiku 4.5', 'anthropic', ['fast'], false),
  model('gpt-5', 'GPT-5', 'openai', [], true),
]

const WORKFLOWS = [
  { name: 'agent', title: 'Agent', description: 'A single agent with every tool.' },
  { name: 'get-it-right', title: 'Get It Right', description: 'Implement, review and verify until it is right.' },
  { name: 'parallel-compete', title: 'Best of Three', description: 'Three attempts in parallel; keep the best.' },
].map((w) => ({ ...w, filename: w.name, source: 'builtin', status: 2, inputs: {} }))

type Chat = Record<string, unknown>
const chat = (o: Chat & { ago: number }): Chat => {
  const { ago, ...rest } = o
  const c: Chat = {
    userId: USER.id, projectId: IDS.project, worktreeId: WORKTREES.main.id, worktree: WORKTREES.main,
    workflowName: 'builtin://agent', state: 'CHAT_STATE_IDLE', selectedPresets: {}, needsRecovery: false,
    activity: 'CHAT_ACTIVITY_IDLE', unread: false, activeDaemonId: IDS.cloudDaemon,
    workflowState: 'WORKFLOW_STATE_STOPPED', workflowStopReason: 'WORKFLOW_STOP_REASON_COMPLETED',
    launchKind: 'chat.start', noMachine: false, ...rest,
  }
  c.createdAt = iso(ago + HOUR)
  c.updatedAt = c.lastActive = c.lastMessageAt = iso(ago)
  c.workflowId = c.workflowId ?? `wf-${String(c.id)}`
  return c
}

const CHATS = [
  chat({ id: CHAT_IDS.long, title: 'Refactor cart totals and fix multi-quantity discounts', activity: 'CHAT_ACTIVITY_RUNNING',
    workflowState: 'WORKFLOW_STATE_ACTIVE', workflowStopReason: undefined, ago: 5000, workflowName: 'builtin://get-it-right' }),
  chat({ id: 'c0ffee00-2222-4aaa-8bbb-00000000000b', title: 'Add rate limiting to /checkout', worktreeId: WORKTREES.rate.id, worktree: WORKTREES.rate,
    activity: 'CHAT_ACTIVITY_AWAITING_INPUT', workflowState: 'WORKFLOW_STATE_ACTIVE', ago: 2 * MIN }),
  chat({ id: 'c0ffee00-2222-4aaa-8bbb-00000000000c', title: 'Load-test the limiter (spawned from rate limiting)', worktreeId: WORKTREES.rate.id,
    worktree: WORKTREES.rate, unread: true, ago: 40 * MIN }),
  chat({ id: 'c0ffee00-2222-4aaa-8bbb-00000000000d', title: 'Retry Stripe webhooks with idempotency keys', worktreeId: WORKTREES.pay.id,
    worktree: WORKTREES.pay, activity: 'CHAT_ACTIVITY_ERROR', workflowStopReason: 'WORKFLOW_STOP_REASON_FAILED', ago: 3 * HOUR }),
  chat({ id: 'c0ffee00-2222-4aaa-8bbb-00000000000e', title: 'Nightly: triage failing CI', launchKind: 'trigger.schedule', triggerId: 'trg-nightly', ago: 7 * HOUR }),
  chat({ id: 'c0ffee00-2222-4aaa-8bbb-00000000000f', title: 'Migrate order confirmation emails from inline HTML strings to MJML templates with snapshot tests', ago: 26 * HOUR }),
  chat({ id: 'c0ffee00-2222-4aaa-8bbb-000000000010', title: '', ago: 3 * DAY }),
  chat({ id: 'c0ffee00-2222-4aaa-8bbb-000000000011', title: 'Write the webhooks guide', projectId: IDS.docsProject, worktreeId: WORKTREES.docsMain.id,
    worktree: WORKTREES.docsMain, ago: 5 * HOUR }),
]

// ── The long agent run ────────────────────────────────────────────────
type Block = Record<string, unknown>
const text = (content: string): Block => ({ type: 'CONTENT_BLOCK_TYPE_TEXT', content })
const tool = (
  id: string,
  name: string,
  input: unknown,
  result: string,
  opt: { error?: boolean; running?: boolean; child?: string } = {},
): Block[] => {
  const call: Block = {
    type: 'CONTENT_BLOCK_TYPE_TOOL_CALL', toolCallId: id, toolName: name, input: JSON.stringify(input),
    toolCallStatus: opt.running ? 'TOOL_CALL_STATUS_EXECUTING' : 'TOOL_CALL_STATUS_COMPLETED',
    startedAt: iso(60_000), ...(opt.running ? {} : { completedAt: iso(55_000) }),
    ...(opt.child ? { childWorkflowId: opt.child } : {}),
  }
  if (opt.running) return [call]
  return [call, { type: 'CONTENT_BLOCK_TYPE_TOOL_RESULT', toolCallId: id, toolName: name, content: result, isError: Boolean(opt.error) }]
}

const SHELL_OUT = Array.from({ length: 120 }, (_, i) =>
  `  ✓ cart/totals ${(i + 1) % 7 === 0 ? 'percent' : 'flat'} discount case #${i + 1} (${((i + 1) % 5) + 1} ms)`,
).join('\n')

const USER_PROMPT = [
  'Discount codes are wrong when a cart line has quantity > 1: SAVE10 on 3 mugs only takes 10% off one of them.',
  '',
  'Requirements:',
  '1. Percent codes apply to the whole line.',
  '2. Flat codes apply once per unit, capped at the line price.',
  '3. Keep the public totals API unchanged.',
  '4. Add tests for 1, 2, 3 and 10 units, and for a flat code larger than the unit price.',
  '',
  'Then open a PR against main and ask me before merging.',
].join('\n')

const FIX_REPLY = [
  'Found it. `applyDiscount` receives the **unit** price, so the discount is taken once per line instead of once per unit.',
  '',
  '| Code | Qty | Expected | Actual |',
  '|---|---|---|---|',
  '| SAVE10 (10%) | 3 | $27.00 | $29.00 |',
  '| FLAT5 ($5) | 2 | $10.00 | $15.00 |',
  '',
  'Here is the fix:',
  '',
  '```ts',
  'export function lineTotal(item: CartItem, discount?: Discount): Money {',
  '  const gross = item.price * item.quantity;',
  '  return gross - applyDiscount(gross, item.quantity, discount);',
  '}',
  '```',
].join('\n')

function messages(chatId: string) {
  let seq = 0
  const t0 = FIXED_NOW - 18 * MIN
  const msg = (role: string, blocks: Block[][] | Block[], extra: Record<string, unknown> = {}) => {
    seq += 1
    const at = new Date(t0 + seq * 40_000).toISOString()
    const assistant = role === 'MESSAGE_ROLE_ASSISTANT'
    return {
      id: `lm-${seq}`, chatId, seq: String(seq), thread: chatId, role,
      ...(assistant ? { model: 'claude-sonnet-5', agent: 'implement' } : {}),
      streamingState: 'STREAMING_STATE_COMPLETE',
      contentBlocks: (blocks.flat() as Block[]).map((b, i) => ({ ...b, id: `lb-${seq}-${i}`, index: i })),
      attachments: [], createdAt: at, updatedAt: at, sequenceNumber: String(seq), ...extra,
    }
  }
  return [
    msg('MESSAGE_ROLE_USER', [text(USER_PROMPT)]),
    msg('MESSAGE_ROLE_ASSISTANT', [
      [text("I'll load the repo's testing conventions, then find where line totals are computed.")],
      tool('k1', 'skill', { action: 'load', path: 'testing' }, '# Testing\n\nUse vitest. Table-driven cases live beside the module.'),
      tool('l1', 'load_tool', { name: 'code_context' }, 'Loaded code_context.'),
      tool('r1', 'grep', { pattern: 'lineTotal', path: 'src' },
        'src/cart/totals.ts:12:export function lineTotal(\nsrc/cart/totals.ts:31:  const lines = cart.items.map((i) => lineTotal(i, cart.discount))'),
      tool('r2', 'view', { path: 'src/cart/totals.ts' },
        '1  import { Discount } from "./discount";\n2  \n12 export function lineTotal(item, discount) {\n13   return item.price * item.quantity - applyDiscount(item.price, discount);\n14 }'),
      tool('w1', 'fetch', { url: 'https://example.com/docs/discounts', format: 'markdown' }, '# Discounts\n\nA percent code applies to the line total.'),
      tool('p1', 'create_plan', {
        title: 'Fix multi-quantity discounts', description: 'Apply codes per line, cap flat codes.', complexity: 'simple',
        tasks: [{ title: 'Pass the line gross to applyDiscount' }, { title: 'Cap flat codes at the line price' }, { title: 'Add unit tests' }],
      }, 'Created plan with 3 tasks.'),
    ]),
    msg('MESSAGE_ROLE_ASSISTANT', [
      [text(FIX_REPLY)],
      tool('e1', 'edit', {
        path: 'src/cart/totals.ts',
        old_string: '  return item.price * item.quantity - applyDiscount(item.price, discount);',
        new_string: '  const gross = item.price * item.quantity;\n  return gross - applyDiscount(gross, item.quantity, discount);',
      }, 'Edited src/cart/totals.ts (+2 -1)'),
      tool('e2', 'edit', {
        path: 'src/cart/discount.ts', old_string: 'export function applyDiscount(price, d) {',
        new_string: 'export function applyDiscount(gross: Money, quantity: number, d?: Discount): Money {',
      }, 'Edited src/cart/discount.ts (+1 -1)'),
    ]),
    msg('MESSAGE_ROLE_ASSISTANT', [
      tool('s1', 'shell', { command: 'npm test -- src/cart', description: 'Run cart tests' },
        'FAIL src/cart/discount.test.ts\n  ● flat code larger than unit price\n    expected 0, received -2.5', { error: true }),
      [text('One failure: a flat code larger than the unit price goes negative. Capping it at the line price.')],
      tool('e3', 'edit', { path: 'src/cart/discount.ts', old_string: 'return d.amount * quantity;', new_string: 'return Math.min(d.amount * quantity, gross);' },
        'Edited src/cart/discount.ts (+1 -1)'),
      tool('s2', 'shell', { command: 'npm test -- src/cart --verbose', description: 'Run cart tests (verbose)' },
        ` PASS  src/cart/totals.test.ts\n${SHELL_OUT}\n\nTests:       120 passed, 120 total`),
      tool('g1', 'mcp__github__pr_list', { repo: 'acme/shopfront', state: 'open' }, '[{"number": 481, "title": "Bump vitest"}]'),
    ]),
    msg('MESSAGE_ROLE_ASSISTANT', [
      [text('Tests pass. I am spawning a reviewer on the change while I write the PR description.')],
      tool('sp1', 'spawn', { title: 'Review discount fix', preset: 'researcher', prompt: 'Review the diff in src/cart for correctness and missing tests.' },
        '', { running: true, child: 'th-review-1' }),
      tool('s3', 'shell', { command: 'gh pr create --fill --base main', description: 'Open the PR' }, '', { running: true }),
    ], { streamingState: 'STREAMING_STATE_STREAMING' }),
  ]
}

export const chatFixtures: Fixtures = {
  'reliant.v1.CatalogService/ListModels': {
    models: MODELS, total: MODELS.length,
    tiers: [
      { tag: 'smart', modelId: 'claude-opus-5-5', thinkingLevel: 'high' },
      { tag: 'default', modelId: 'claude-sonnet-5', thinkingLevel: 'medium' },
      { tag: 'fast', modelId: 'claude-haiku-4-5', thinkingLevel: '' },
    ],
  },
  'reliant.v1.CatalogService/ListAvailableModels': {
    models: MODELS.map((m) => ({
      id: m.id, displayName: m.name, provider: m.provider, family: m.provider, apiModel: m.id, contextWindow: m.contextWindow,
      defaultMaxTokens: m.defaultMaxTokens, canReason: m.canReason, supportsAttachments: true, supportsTools: true,
      supportsCaching: true, tags: m.tags, enabled: true,
    })),
  },
  'reliant.v1.WorkflowService/ListWorkflows': { workflows: WORKFLOWS },
  'reliant.v1.ChatService/ListChats': {
    chats: CHATS, total: CHATS.length, lastUserUpdateSequence: '10', mainWorktrees: [WORKTREES.main, WORKTREES.docsMain],
  },
  'reliant.v1.ChatService/ListArchivedChats': { chats: [], total: 0 },
  'reliant.v1.ChatService/GetChat': (req) => {
    const id = req.chatId ?? req.id
    const found = CHATS.find((c) => c.id === id)
    return found ? { chat: found } : { __error: { code: 'not_found', message: 'chat not found' } }
  },
  'reliant.v1.ChatService/ListMessages': (req) => {
    const m = !req.chatId || req.chatId === CHAT_IDS.long ? messages(CHAT_IDS.long) : []
    return { messages: m, total: m.length, count: m.length, hasMore: false, oldestSeq: '1' }
  },
  'reliant.v1.StreamingService/StreamUserUpdates': { __type: 'reliant.v1.UserStreamEvent', __stream: [{ sync: { lastSequence: '10' } }] },
  'reliant.v1.StreamingService/StreamChatUpdates': (req) => {
    const m = req.chatId && req.chatId !== CHAT_IDS.long ? [] : messages(CHAT_IDS.long)
    return {
      __type: 'reliant.v1.ChatStreamEvent',
      __stream: [
        { snapshot: { messages: m, otherUpdates: [], latestSequence: String(m.length), total: m.length, hasMore: false,
          oldestSeq: m.length ? '1' : '0', threadTokenCount: '92400', compactionThreshold: '120000', activeWorkflows: [] } },
        { caughtUp: { latestSequence: String(m.length) } },
      ],
    }
  },
}
