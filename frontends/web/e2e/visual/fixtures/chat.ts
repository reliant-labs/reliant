/**
 * Chat fixtures: models, a sidebar's worth of chats (two projects, branched
 * chats, a long branch name, running / needs-input / failed / unread /
 * automation rows, an untitled chat), one short chat, and THE fixture chat
 * (`CHAT_IDS.long`) that every chat surface and every chat restyle PR reads.
 * Ported from the November review's fixtures-chat-sidebars/20-chat-sidebars.js.
 *
 * The fixture chat is three turns of a get-it-right run, chosen so one chat
 * shows every state the transcript draws:
 *
 *   turn 1  a long finished turn with every tool category (skill, load_tool,
 *           grep, view, web fetch, a plan, edits, shell, an MCP tool), a
 *           failed shell call it recovers from, thinking blocks, two
 *           sub-agents (one done, one running), and a long answer with a
 *           long code block; then the review and verify node threads
 *   turn 2  a user message with an image and a file attachment; a final
 *           failed tool call, then a final provider error
 *   turn 3  a pending question (ask_user), a provider error that is
 *           retrying, a shell call waiting for approval, and a failed send
 *
 * Threads: Main, `review` and `verify` (graph nodes, so they are tabs), and
 * the two sub-agents (spawns, so they fold into their cards).
 *
 * Chat content arrives on reliant.v1.StreamingService/StreamChatUpdates, a
 * binary-proto stream: the snapshot below is encoded with the app's schemas.
 */
import type { Fixtures } from '../rpc'
import { DAY, FIXED_NOW, HOUR, IDS, MIN, USER, WORKTREES, iso } from './base'

export const CHAT_IDS = {
  long: 'c0ffee00-2222-4aaa-8bbb-00000000000a',
  /** A finished turn of a dozen tool calls, with thinking and redacted thinking. */
  tools: 'c0ffee00-2222-4aaa-8bbb-000000000012',
  /** A turn that stopped on a failed tool call and a provider error. */
  failure: 'c0ffee00-2222-4aaa-8bbb-000000000013',
}
export const CHAT_TITLES = {
  tools: 'Move tax rates behind the pricing config',
  failure: 'Upgrade the Stripe SDK to v18',
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
  chat({ id: CHAT_IDS.tools, title: CHAT_TITLES.tools, ago: 12 * MIN }),
  chat({ id: CHAT_IDS.failure, title: CHAT_TITLES.failure, activity: 'CHAT_ACTIVITY_ERROR', workflowStopReason: 'WORKFLOW_STOP_REASON_FAILED', ago: 95 * MIN }),
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
  /**
   * error: the result is an error; failed: and the call ended failed (a
   * final failure); running: no result yet; pending: queued, waiting for an
   * approval; blockId: the call block's id (an approval's entityId).
   */
  opt: { error?: boolean; failed?: boolean; running?: boolean; pending?: boolean; child?: string; blockId?: string } = {},
): Block[] => {
  const status = opt.pending ? 'TOOL_CALL_STATUS_PENDING'
    : opt.running ? 'TOOL_CALL_STATUS_EXECUTING'
      : opt.failed ? 'TOOL_CALL_STATUS_FAILED' : 'TOOL_CALL_STATUS_COMPLETED'
  const call: Block = {
    ...(opt.blockId ? { id: opt.blockId } : {}),
    type: 'CONTENT_BLOCK_TYPE_TOOL_CALL', toolCallId: id, toolName: name, input: JSON.stringify(input),
    toolCallStatus: status,
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

/** The fixture chat's threads (thread id = workflow id for a child). */
const MAIN = CHAT_IDS.long
const WF_MAIN = `wf-${MAIN}`
const TH = {
  review: 'th-c1-review',
  verify: 'th-c1-verify',
  reviewer: 'th-review-1',
  tester: 'th-c1-tests',
}

/** A small fixed PNG (96×64): the "checkout screenshot" attachment. */
const SCREENSHOT_PNG = 'iVBORw0KGgoAAAANSUhEUgAAAGAAAABACAIAAABqVuVZAAAAkklEQVR42u3XMRGAMBBFwdTRRBEJSEESVRREEAKYFCjAQUomR3bmKdjm7qe7PxqUEAACBAhQKKCt7MsGCBAgQIAAAVobSIAAAQIEKC7QWdvkAQIECBAgQM48IECAAAGyxf79BAACBAgQoFhAAgQIECBAgAABmqN8XJ8FCBAgQIAAAQIECBAgQICMVUCAAAGiMOwFLTIuMBCRM4oAAAAASUVORK5CYII='

const ATTACHMENTS = [
  { id: 'att-c1-img', filename: 'checkout-total.png', size: '203', mimeType: 'image/png', url: '/api/attachments/att-c1-img' },
  { id: 'att-c1-file', filename: 'bundle-discounts.md', size: '1830', mimeType: 'text/markdown', url: '/api/attachments/att-c1-file' },
]

const LONG_ANSWER = [
  'Multi-quantity discounts are fixed, and the public totals API is unchanged.',
  '',
  '## What changed',
  '',
  '- `applyDiscount` now takes the line **gross**, not the unit price, so `SAVE10` on 3 mugs takes 10% off all three.',
  '- Flat codes apply once per unit and are capped at the line price, so a code never makes a line negative.',
  '- The cases you listed are in src/cart/discount.test.ts: 1, 2, 3 and 10 units, plus a flat code larger than the unit price.',
  '',
  '> The cap is the one behaviour change a customer can see: a $5 code on a $3 item now takes $3, not $5.',
  '',
  'The whole module after the change, for review (the PR is [#482](https://github.com/acme/shopfront/pull/482)):',
  '',
  '```ts',
  ...Array.from({ length: 44 }, (_, i) =>
    i === 0 ? "import type { CartItem, Discount, Money } from './types';"
      : i === 2 ? 'export function applyDiscount(gross: Money, quantity: number, d?: Discount): Money {'
        : i === 3 ? '  if (!d) return 0;'
          : i === 4 ? "  if (d.kind === 'percent') return Math.round(gross * d.rate);"
            : i === 5 ? '  return Math.min(d.amount * quantity, gross);'
              : i === 6 ? '}'
                : i % 6 === 1 ? '' : `// case ${i}: ${i % 2 ? 'percent' : 'flat'} code on ${i % 7 + 1} units stays within the line total`),
  '```',
].join('\n')

const SPEC_SNIPPET = '# Bundle discounts\n\nA flat code on a bundle applies once per bundle, not once per item in it.'

let updateSeq = 100
/** A non-message update of the snapshot: `dataJson` is the SDK event, snake_case. */
const update = (updateType: string, entityId: string, data: Record<string, unknown>, at: string, chatId = CHAT_IDS.long) => ({
  updateType, sequenceNumber: String(++updateSeq), entityId, chatId, dataJson: JSON.stringify(data), createdAt: at,
})

const T0 = FIXED_NOW - 18 * MIN
/** The time of the fixture chat's nth message (40s apart), plus `plus` ms. */
const atSeq = (seq: number, plus = 0) => new Date(T0 + seq * 40_000 + plus).toISOString()

function messages(chatId: string) {
  let seq = 0
  const msg = (role: string, blocks: Block[][] | Block[], extra: Record<string, unknown> = {}) => {
    seq += 1
    const at = atSeq(seq)
    const assistant = role === 'MESSAGE_ROLE_ASSISTANT'
    return {
      id: `lm-${seq}`, chatId, seq: String(seq), thread: chatId, role,
      ...(assistant ? { model: 'claude-sonnet-5', agent: 'implement' } : {}),
      streamingState: 'STREAMING_STATE_COMPLETE',
      contentBlocks: (blocks.flat() as Block[]).map((b, i) => ({ id: `lb-${seq}-${i}`, ...b, index: i })),
      attachments: [], createdAt: at, updatedAt: at, sequenceNumber: String(seq), ...extra,
    }
  }
  const thinking = (content: string): Block => ({ type: 'CONTENT_BLOCK_TYPE_THINKING', content })
  return [
    // ── Turn 1: a long finished turn ──
    msg('MESSAGE_ROLE_USER', [text(USER_PROMPT)]),
    msg('MESSAGE_ROLE_ASSISTANT', [
      [thinking('The bug is probably where the line total is computed; check how the discount is applied per line.')],
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
      // A failure the turn recovers from: the next run of the same tests passes.
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
      [thinking('A second pair of eyes on the cap, and more edge cases, can run while I write the summary.')],
      [text('Tests pass. I am spawning a reviewer and a tester while I write the summary.')],
      tool('sp1', 'spawn', { title: 'Review discount fix', preset: 'researcher', prompt: 'Review the diff in src/cart for correctness and missing tests.' },
        'The diff is correct. One suggestion: name the cap in a comment.', { child: TH.reviewer }),
      tool('sp2', 'spawn', { title: 'Write edge-case tests', preset: 'researcher', prompt: 'Add tests for bundles and zero-priced items.' },
        '', { running: true, child: TH.tester }),
    ]),
    msg('MESSAGE_ROLE_ASSISTANT', [text(LONG_ANSWER)]),
    // The review and verify nodes, each on its own thread.
    msg('MESSAGE_ROLE_ASSISTANT', [text('Reviewed the change: the cap is right and the tests cover every case in the request. No blockers.')],
      { thread: TH.review, agent: 'review', workflowId: 'wf-c1-review' }),
    msg('MESSAGE_ROLE_ASSISTANT', [
      [text('Verifying on a clean checkout.')],
      tool('v1', 'shell', { command: 'npm ci && npm test', description: 'Verify on a clean checkout' }, 'Tests:       124 passed, 124 total'),
      [text('Verified: 124 tests pass on a clean checkout.')],
    ], { thread: TH.verify, agent: 'verify', workflowId: 'wf-c1-verify' }),

    // ── Turn 2: attachments, a final failed tool call, a final provider error ──
    msg('MESSAGE_ROLE_USER', [text('Here is the checkout screenshot and the spec for bundles. A flat code on a bundle should apply once per bundle.')],
      { attachments: ATTACHMENTS }),
    msg('MESSAGE_ROLE_ASSISTANT', [
      [thinking('Bundles are a new line kind; check the types before changing the discount.')],
      [text('Reading the spec, then checking the types.')],
      tool('r3', 'view', { path: 'bundle-discounts.md' }, SPEC_SNIPPET),
      tool('s4', 'shell', { command: 'npm run typecheck', description: 'Typecheck' },
        "src/cart/bundle.ts:8:3 - error TS2339: Property 'units' does not exist on type 'Bundle'.", { error: true, failed: true }),
    ]),

    // ── Turn 3: a question, a retrying provider error, an approval, a failed send ──
    msg('MESSAGE_ROLE_USER', [text('Try again. Ask me before you push anything.')]),
    msg('MESSAGE_ROLE_ASSISTANT', [
      [text('Added `units` to `Bundle` and the typecheck passes. One decision is yours.')],
      tool('ask1', 'ask_user', { questions: [{
        question: 'A flat code larger than a bundle: cap it at the bundle price, or reject the code?',
        options: [{ label: 'Cap at the bundle price', description: 'The total never goes negative' }, { label: 'Reject the code', description: 'Checkout shows an error' }],
      }] }, 'Question posted; continuing with the cap until you answer.'),
    ]),
    msg('MESSAGE_ROLE_ASSISTANT', [
      [text('The branch is ready. Pushing it needs your approval.')],
      tool('sh-push', 'shell', { command: 'git push origin fix/per-unit-discounts', description: 'Push the branch' }, '',
        { running: true, pending: true, blockId: 'blk-c1-approve' }),
    ], { streamingState: 'STREAMING_STATE_COMPLETE' }),
    // A send that failed for good (after a reload: "Not sent" and Remove).
    msg('MESSAGE_ROLE_USER', [text('Also add a CHANGELOG entry for the cap.')], { id: 'optimistic-failed-c1-1' }),
  ]
}

/** Errors the fixture chat's turns hit, placed by time between its messages. */
const ERROR_UPDATES = [
  update('CHAT_UPDATE_TYPE_ERROR', 'err-c1-final', {
    id: 'err-c1-final', chat_id: MAIN, activity_type: 'V2_CallLLM', activity_id: 'act-c1-9',
    error_message: 'anthropic: 500 api_error: Internal server error', error_summary: 'The AI provider failed',
    timestamp: atSeq(10, 20_000), attempt_number: 5, max_attempts: 5, is_retrying: false, workflow_id: WF_MAIN, thread: MAIN,
  }, atSeq(10, 20_000)),
  update('CHAT_UPDATE_TYPE_ERROR', 'err-c1-retry', {
    id: 'err-c1-retry', chat_id: MAIN, activity_type: 'V2_CallLLM', activity_id: 'act-c1-12',
    error_message: 'anthropic: 529 overloaded_error: Overloaded', error_summary: "The provider didn't respond",
    timestamp: atSeq(12, 20_000), attempt_number: 2, max_attempts: 5, is_retrying: true, workflow_id: WF_MAIN, thread: MAIN,
  }, atSeq(12, 20_000)),
]

const workflow = (id: string, thread: string, o: Record<string, unknown> = {}) => ({
  id, workflowName: 'builtin://agent', thread, createdAt: atSeq(1), messageCount: 0, children: [], steps: [],
  origin: 'main', state: 'WORKFLOW_STATE_STOPPED', stopReason: 'WORKFLOW_STOP_REASON_COMPLETED', ...o,
})
const RUNNING = { state: 'WORKFLOW_STATE_ACTIVE', stopReason: 'WORKFLOW_STOP_REASON_UNSPECIFIED' }

const WORKFLOW_TREE = [
  workflow(WF_MAIN, MAIN, {
    workflowName: 'builtin://get-it-right', ...RUNNING,
    children: [
      workflow('wf-c1-review', TH.review, { origin: 'node', originNodeId: 'review', spawnedByNodeId: 'review', threadTitle: 'review', parentId: WF_MAIN, parentThread: MAIN }),
      workflow('wf-c1-verify', TH.verify, { origin: 'node', originNodeId: 'verify', spawnedByNodeId: 'verify', threadTitle: 'verify', parentId: WF_MAIN, parentThread: MAIN }),
      workflow(TH.reviewer, TH.reviewer, { origin: 'spawn', parentId: WF_MAIN, parentThread: MAIN, threadTitle: 'Review discount fix', spawnedByNodeId: 'implement' }),
      workflow(TH.tester, TH.tester, { origin: 'spawn', parentId: WF_MAIN, parentThread: MAIN, threadTitle: 'Write edge-case tests', spawnedByNodeId: 'implement', ...RUNNING }),
    ],
  }),
]

/** What a sub-agent's card previews: its own thread's messages. */
function threadMessages(thread: string) {
  const at = atSeq(5, 10_000)
  const body = thread === TH.reviewer
    ? 'The diff is correct. One suggestion: name the cap in a comment so the next reader knows it is deliberate.'
    : 'Writing tests for bundles and zero-priced items.'
  return [{
    id: `${thread}-m1`, chatId: MAIN, seq: '1', thread, role: 'MESSAGE_ROLE_ASSISTANT', model: 'claude-sonnet-5', agent: 'researcher',
    streamingState: thread === TH.tester ? 'STREAMING_STATE_STREAMING' : 'STREAMING_STATE_COMPLETE',
    contentBlocks: [{ id: `${thread}-b1`, index: 0, type: 'CONTENT_BLOCK_TYPE_TEXT', content: body }],
    attachments: [], createdAt: at, updatedAt: at, sequenceNumber: '1',
  }]
}

// ── The short chat: one question, a little work, a short answer ──
const SHORT_CHAT_ID = 'c0ffee00-2222-4aaa-8bbb-00000000000c'
function shortMessages() {
  const t = FIXED_NOW - 41 * MIN
  const at = (n: number) => new Date(t + n * 30_000).toISOString()
  const base = (id: string, role: string, n: number, blocks: Block[]) => ({
    id, chatId: SHORT_CHAT_ID, seq: String(n), thread: SHORT_CHAT_ID, role, streamingState: 'STREAMING_STATE_COMPLETE',
    ...(role === 'MESSAGE_ROLE_ASSISTANT' ? { model: 'claude-sonnet-5', agent: 'agent' } : {}),
    contentBlocks: blocks.map((b, i) => ({ id: `${id}-b${i}`, ...b, index: i })), attachments: [],
    createdAt: at(n), updatedAt: at(n), sequenceNumber: String(n),
  })
  return [
    base('sm-1', 'MESSAGE_ROLE_USER', 1, [text('Load-test the new /checkout limiter at 50 requests a second for a minute. Where does it start rejecting?')]),
    base('sm-2', 'MESSAGE_ROLE_ASSISTANT', 2, [
      ...tool('sk1', 'shell', { command: 'npx autocannon -r 50 -d 60 http://localhost:4000/checkout', description: 'Load-test /checkout' },
        '2xx: 2,400  429: 600  p99 latency: 41 ms'),
      text('It starts rejecting at about **40 requests a second** per client, which is the configured burst. Above that:\n\n- 20% of requests get `429 Too Many Requests`, each with a `Retry-After` header.\n- p99 latency stays at 41 ms, so the limiter adds no measurable cost.'),
    ]),
  ]
}

// ── Scripted chats: a dozen tool calls, and a turn that stopped on a failure ──
type Turn = { role: 'user' | 'assistant'; blocks: Block[] | Block[][] }
const scriptedTime = (agoMs: number, n: number, plus = 0) => new Date(FIXED_NOW - agoMs + n * 40_000 + plus).toISOString()
function scripted(chatId: string, agoMs: number, turns: Turn[]) {
  return turns.map((turn, i) => {
    const n = i + 1
    const id = `sc-${chatId.slice(-2)}-m${n}`
    const assistant = turn.role === 'assistant'
    return {
      id, chatId, seq: String(n), thread: chatId, role: assistant ? 'MESSAGE_ROLE_ASSISTANT' : 'MESSAGE_ROLE_USER',
      streamingState: 'STREAMING_STATE_COMPLETE', ...(assistant ? { model: 'claude-sonnet-5', agent: 'agent' } : {}),
      // Tool calls run in the seconds after their message starts, not at the fixture's default instant.
      contentBlocks: (turn.blocks.flat() as Block[]).map((b, j) => ({
        id: `${id}-b${j}`, ...b, index: j,
        ...(b.startedAt ? { startedAt: scriptedTime(agoMs, n, j * 2_000), completedAt: scriptedTime(agoMs, n, j * 2_000 + 1_500) } : {}),
      })), attachments: [],
      createdAt: scriptedTime(agoMs, n), updatedAt: scriptedTime(agoMs, n), sequenceNumber: String(n),
    }
  })
}
const thinkingBlock = (content: string): Block => ({ type: 'CONTENT_BLOCK_TYPE_THINKING', content })
/** Withheld reasoning: an opaque payload with no readable text. */
const redactedThinkingBlock = (): Block => ({
  type: 'CONTENT_BLOCK_TYPE_REDACTED_THINKING',
  content: 'EuYBCkQYAiJAq7mJm0nq0q4P3Zx9m1Yw0tYq3r0cE2k8mV1nT5bL6sZpQd4hF7gU9wJ2oXaK8eN3yRiB0cVtMlHxGfDsAqPz1C4uIhE=',
})

const TOOLS_TURNS: Turn[] = [
  { role: 'user', blocks: [text('Tax rates are hard-coded in three places in checkout. Move them behind the pricing config and keep the totals identical.')] },
  { role: 'assistant', blocks: [
    [thinkingBlock('The rates are probably constants near the totals code. Find every reader before moving any of them.')],
    [text('I will start by finding every place that reads a tax rate.')],
    tool('t1', 'grep', { pattern: 'TAX_RATE', path: 'src' },
      'src/cart/totals.ts:4:const TAX_RATE = 0.0825\nsrc/checkout/summary.tsx:11:const TAX_RATE = 0.0825\nsrc/invoices/render.ts:7:const TAX_RATE = 0.0825'),
    tool('t2', 'grep', { pattern: 'taxRate', path: 'src' },
      'src/cart/tax.ts:3:export function taxRate(region: string): number {\nsrc/cart/totals.ts:21:  const tax = gross * taxRate(cart.region)'),
    tool('t3', 'glob', { pattern: 'src/**/pricing*.ts' }, 'src/pricing/config.ts\nsrc/pricing/config.test.ts'),
    tool('t4', 'view', { path: 'src/pricing/config.ts' }, "1  export const pricing = {\n2    currency: 'USD',\n3    rounding: 'half-up',\n4  } as const"),
    tool('t5', 'view', { path: 'src/cart/tax.ts' }, '1  import { TAX_RATE } from "./totals";\n2  \n3  export function taxRate(region: string): number {\n4    return region === "CA" ? 0.0725 : TAX_RATE;\n5  }'),
    tool('t6', 'view', { path: 'src/cart/totals.ts' }, '1  import { taxRate } from "./tax";\n4  const TAX_RATE = 0.0825;\n21   const tax = gross * taxRate(cart.region);'),
    tool('t7', 'view', { path: 'src/checkout/summary.tsx' }, '11 const TAX_RATE = 0.0825;\n34 <Row label="Tax" value={format(subtotal * TAX_RATE)} />'),
    tool('t8', 'view', { path: 'src/invoices/render.ts' }, '7  const TAX_RATE = 0.0825;\n18 const tax = Math.round(net * TAX_RATE);'),
    tool('t9', 'grep', { pattern: 'from \\"./totals\\"', path: 'src' }, 'src/cart/tax.ts:1:import { TAX_RATE } from "./totals";'),
  ] },
  { role: 'assistant', blocks: [
    [redactedThinkingBlock()],
    [text('Three call sites, and the config has no place for rates yet. I will add one and point all three at it.')],
    tool('t10', 'edit', {
      path: 'src/pricing/config.ts', old_string: "  rounding: 'half-up',\n} as const",
      new_string: "  rounding: 'half-up',\n  taxRates: { default: 0.0825, CA: 0.0725 },\n} as const",
    }, 'Edited src/pricing/config.ts (+1 -0)'),
    tool('t11', 'edit', {
      path: 'src/cart/tax.ts', old_string: 'import { TAX_RATE } from "./totals";\n\nexport function taxRate(region: string): number {\n  return region === "CA" ? 0.0725 : TAX_RATE;\n}',
      new_string: 'import { pricing } from "../pricing/config";\n\nexport function taxRate(region: string): number {\n  const rates: Record<string, number> = pricing.taxRates;\n  return rates[region] ?? pricing.taxRates.default;\n}',
    }, 'Edited src/cart/tax.ts (+4 -4)'),
    tool('t12', 'edit', { path: 'src/cart/totals.ts', old_string: 'const TAX_RATE = 0.0825;\n', new_string: '' }, 'Edited src/cart/totals.ts (+0 -1)'),
    tool('t13', 'edit', {
      path: 'src/checkout/summary.tsx', old_string: 'format(subtotal * TAX_RATE)', new_string: 'format(subtotal * taxRate(region))',
    }, 'Edited src/checkout/summary.tsx (+1 -1)'),
  ] },
  { role: 'assistant', blocks: [
    [text('All three now read the config. Running the checks.')],
    tool('t14', 'shell', { command: 'npm run typecheck', description: 'Typecheck' }, '> tsc --noEmit\n\nFound 0 errors.'),
    tool('t15', 'shell', { command: 'npm test -- src/cart src/checkout src/invoices', description: 'Run the cart, checkout and invoice tests' },
      ' PASS  src/cart/totals.test.ts\n PASS  src/checkout/summary.test.tsx\n PASS  src/invoices/render.test.ts\n\nTests:       86 passed, 86 total'),
    tool('t16', 'grep', { pattern: 'TAX_RATE', path: 'src' }, 'No matches found.'),
    [text('Tax rates now live in `pricing.taxRates`, and checkout, the cart and invoices all read them from there.\n\n- Totals are identical: the 86 cart, checkout and invoice tests pass unchanged.\n- California keeps its 7.25% rate; every other region falls back to 8.25%.\n- Nothing outside `src/pricing/config.ts` defines a rate any more.')],
  ] },
]

const FAIL_TURNS: Turn[] = [
  { role: 'user', blocks: [text('Upgrade the Stripe SDK to v18 and fix whatever breaks.')] },
  { role: 'assistant', blocks: [
    [text('Checking the current version and where the SDK is used, then upgrading.')],
    tool('f1', 'view', { path: 'package.json' }, '12   "stripe": "^16.2.0",'),
    tool('f2', 'grep', { pattern: "from 'stripe'", path: 'src' },
      "src/payments/client.ts:1:import Stripe from 'stripe'\nsrc/payments/webhook.ts:2:import Stripe from 'stripe'"),
    tool('f3', 'shell', { command: 'npm install stripe@18', description: 'Upgrade stripe' }, 'added 1 package, changed 1 package in 6s'),
    tool('f4', 'shell', { command: 'npm run typecheck', description: 'Typecheck' },
      "src/payments/webhook.ts:41:30 - error TS2345: Argument of type 'Stripe.Event' is not assignable to parameter of type 'Stripe.WebhookEndpoint'.\nsrc/payments/client.ts:9:3 - error TS2353: Object literal may only specify known properties, and 'apiVersion' does not exist in type 'StripeConfig'.",
      { error: true, failed: true }),
  ] },
]

const FAIL_ERRORS = [
  update('CHAT_UPDATE_TYPE_ERROR', 'err-fail-final', {
    id: 'err-fail-final', chat_id: CHAT_IDS.failure, activity_type: 'V2_CallLLM', activity_id: 'act-fail-3',
    error_message: 'anthropic: 500 api_error: Internal server error', error_summary: 'The AI provider failed',
    timestamp: scriptedTime(95 * MIN, 2, 20_000), attempt_number: 5, max_attempts: 5, is_retrying: false, workflow_id: `wf-${CHAT_IDS.failure}`, thread: CHAT_IDS.failure,
  }, scriptedTime(95 * MIN, 2, 20_000), CHAT_IDS.failure),
]

/** Every chat but the fixture chat: its messages and the errors its stream carries. */
const SCRIPTED: Record<string, { messages: () => Record<string, unknown>[]; errors: Record<string, unknown>[] }> = {
  [SHORT_CHAT_ID]: { messages: shortMessages, errors: [] },
  [CHAT_IDS.tools]: { messages: () => scripted(CHAT_IDS.tools, 12 * MIN, TOOLS_TURNS), errors: [] },
  [CHAT_IDS.failure]: { messages: () => scripted(CHAT_IDS.failure, 95 * MIN, FAIL_TURNS), errors: FAIL_ERRORS },
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
    const thread = req.threadId as string | undefined
    const script = SCRIPTED[String(req.chatId)]
    const m = thread && thread !== MAIN && !SCRIPTED[thread]
      ? threadMessages(thread)
      : script ? script.messages()
        // A failed send lives in the client alone: the server never lists it.
        : !req.chatId || req.chatId === MAIN ? messages(MAIN).filter((x) => !x.id.startsWith('optimistic-')) : []
    return { messages: m, total: m.length, count: m.length, hasMore: false, oldestSeq: '1' }
  },
  'reliant.v1.ChatService/GetWorkflowExecutions': (req) => ({ allRootWorkflows: req.chatId === MAIN ? WORKFLOW_TREE : [] }),
  'reliant.v1.ApprovalService/ListApprovalsByChat': (req) => req.chatId === MAIN
    ? { approvals: [{
      id: 'appr-c1-push', chatId: MAIN, approvalType: 'APPROVAL_TYPE_TOOL_CALL', entityId: 'blk-c1-approve',
      status: 'APPROVAL_STATUS_PENDING', title: 'Run shell command', description: 'git push origin fix/per-unit-discounts',
      createdAt: atSeq(13), messageId: 'lm-13', workflowId: WF_MAIN,
    }], total: 1 }
    : { approvals: [], total: 0 },
  'reliant.v1.QuestionService/ListAsyncQuestions': (req) => ({ questions: req.chatId === MAIN ? [{
    questionId: 'q-c1-cap', chatId: MAIN, workflowId: WF_MAIN, stepId: 'implement', status: 'pending', createdAt: atSeq(12),
    metadata: JSON.stringify({ type: 'ask_user', questions: [{
      question: 'A flat code larger than a bundle: cap it at the bundle price, or reject the code?',
      options: [{ label: 'Cap at the bundle price', description: 'The total never goes negative' }, { label: 'Reject the code', description: 'Checkout shows an error' }],
    }] }),
    threadId: MAIN, async: true, toolCallId: 'ask1',
  }] : [] }),
  'reliant.v1.QuestionService/ListPendingQuestions': { questions: [] },
  'reliant.v1.AttachmentService/GetAttachment': (req) => req.attachmentId === 'att-c1-img'
    ? { attachment: { id: 'att-c1-img', userId: USER.id, filename: 'checkout-total.png', size: '203', mimeType: 'image/png', filePath: '',
      createdAt: atSeq(9), updatedAt: atSeq(9), url: '/api/attachments/att-c1-img', attachmentType: 'image' }, content: SCREENSHOT_PNG }
    : { __error: { code: 'not_found', message: 'attachment not found' } },
  'reliant.v1.StreamingService/StreamUserUpdates': { __type: 'reliant.v1.UserStreamEvent', __stream: [{ sync: { lastSequence: '10' } }] },
  'reliant.v1.StreamingService/StreamChatUpdates': chatStream({ failedSend: true }),
}

/**
 * The chat stream's snapshot. `failedSend: false` leaves out the failed send,
 * which lives in the client alone: on the mobile chat screen whether it
 * survives depends on the order the snapshot and the message list land in,
 * which the harness does not control, so the mobile capture leaves it out.
 */
function chatStream({ failedSend }: { failedSend: boolean }) {
  return (req: Record<string, unknown>) => {
    const script = SCRIPTED[String(req.chatId)]
    const all = script ? script.messages() : req.chatId && req.chatId !== MAIN ? [] : messages(MAIN)
    const m = failedSend ? all : all.filter((x) => !x.id.startsWith('optimistic-'))
    const other = script ? script.errors : ERROR_UPDATES
    const latest = String(Math.max(m.length, updateSeq))
    return {
      __type: 'reliant.v1.ChatStreamEvent',
      __stream: [
        { snapshot: { messages: m, otherUpdates: other, latestSequence: latest, total: m.length, hasMore: false,
          oldestSeq: m.length ? '1' : '0', threadTokenCount: '92400', compactionThreshold: '120000', activeWorkflows: [] } },
        { caughtUp: { latestSequence: latest } },
      ],
    }
  }
}

/** The fixture chat without its failed send (see chatStream). */
export const chatWithoutFailedSend: Fixtures = {
  'reliant.v1.StreamingService/StreamChatUpdates': chatStream({ failedSend: false }),
}
