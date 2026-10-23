import type { Page, Route } from '@playwright/test';

// A signed-in, onboarded user with one project, three chats and a full inbox,
// against no backend: every Connect RPC is answered here. Used by the mobile
// specs (mobile.spec.ts), which need the app past AuthGuard, onboarding and
// project selection before any `/m/*` screen renders.
//
// Same approach as onboarding.spec.ts: broad `{}` fallbacks first, specific
// RPCs after (the LAST matching route wins), and the API-key login path so no
// Supabase session is needed.

export const PROJECT = {
  id: 'proj_mobile',
  userId: 'user_mobile',
  name: 'my-project',
  path: '/home/workspace/projects/my-project',
  isGitRepo: true,
  defaultBranch: 'main',
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-20T00:00:00Z',
  lastActive: '2026-10-20T00:00:00Z',
  isForge: true,
  forgeProjectName: 'my-project',
};

const minutesAgo = (n: number) => new Date(Date.now() - n * 60_000).toISOString();

export const CHATS = [
  { id: 'chat_a', title: 'Fix flaky login test', activity: 2 },
  { id: 'chat_b', title: 'Add billing export', activity: 0 },
  { id: 'chat_c', title: 'Investigate slow builds on the cloud machine', activity: 0 },
].map((c, i) => ({
  id: c.id,
  userId: 'user_mobile',
  title: c.title,
  projectId: PROJECT.id,
  state: 1,
  createdAt: minutesAgo(600 + i),
  updatedAt: minutesAgo(10 + i),
  lastActive: minutesAgo(10 + i),
  lastMessageAt: minutesAgo(10 + i),
  activity: c.activity,
}));

const LONG_LINE = 'FAIL src/auth/login.test.ts > login > retries on a 503 from the identity provider '.repeat(3);

/** A user turn, then an assistant turn with a shell call and a file read: the timeline's commonest tool cards. */
export const MESSAGES = [
  {
    id: 'msg_1',
    seq: '1',
    thread: 'main',
    role: 1,
    createdAt: minutesAgo(20),
    updatedAt: minutesAgo(20),
    contentBlocks: [{ id: 'b1', index: 0, type: 1, content: 'The login test fails about one run in five. Find out why.' }],
  },
  {
    id: 'msg_2',
    seq: '2',
    thread: 'main',
    role: 2,
    createdAt: minutesAgo(19),
    updatedAt: minutesAgo(19),
    contentBlocks: [
      { id: 'b2', index: 0, type: 1, content: 'Running the test in a loop to reproduce it first.' },
      {
        id: 'b3',
        index: 1,
        type: 2,
        toolName: 'run_command',
        toolCallId: 'tc_1',
        input: JSON.stringify({ command: 'for i in $(seq 1 20); do npm test -- src/auth/login.test.ts --reporter=dot || break; done' }),
        toolCallStatus: 3,
        matchedResult: { toolCallId: 'tc_1', type: 'tool_result', content: `${LONG_LINE}\n`.repeat(6) },
      },
      {
        id: 'b4',
        index: 2,
        type: 2,
        toolName: 'view',
        toolCallId: 'tc_2',
        input: JSON.stringify({ file_path: 'src/auth/providers/identity/retryingIdentityProviderClient.ts' }),
        toolCallStatus: 3,
        matchedResult: { toolCallId: 'tc_2', type: 'tool_result', content: 'export const RETRIES = 3;\n'.repeat(12) },
      },
    ],
  },
];

const ASK_USER_METADATA = JSON.stringify({
  type: 'ask_user',
  questions: [
    {
      question: 'Which database should the export read from?',
      options: [{ label: 'Primary' }, { label: 'Read replica' }],
    },
  ],
});

export const INBOX_ITEMS = [
  {
    kind: 1, // APPROVAL
    itemId: 'approval:ap_1',
    chatId: 'chat_a',
    runId: 'chat_a',
    projectId: PROJECT.id,
    projectName: PROJECT.name,
    chatTitle: 'Fix flaky login test',
    waitingSince: minutesAgo(4),
    approval: { approvalId: 'ap_1', title: 'run_command: npm test -- login' },
  },
  {
    kind: 2, // QUESTION — an async ask_user (the agent kept working)
    itemId: 'question:q_1',
    chatId: 'chat_b',
    runId: 'chat_b',
    projectId: PROJECT.id,
    projectName: PROJECT.name,
    chatTitle: 'Add billing export',
    waitingSince: minutesAgo(12),
    question: {
      questionId: 'q_1',
      threadId: 'chat_b',
      prompt: 'Which database should the export read from?',
      metadata: ASK_USER_METADATA,
      async: true,
    },
  },
  {
    kind: 3, // WAITING_FOR_MACHINE
    itemId: 'waiting_for_machine:chat_c',
    chatId: 'chat_c',
    runId: 'chat_c',
    projectId: PROJECT.id,
    projectName: PROJECT.name,
    chatTitle: 'Investigate slow builds on the cloud machine',
    waitingSince: minutesAgo(90),
    waitingForMachine: { daemonId: 'daemon_1', daemonName: 'cloud-small' },
  },
  {
    kind: 4, // AUTOMATION_FAILING
    itemId: 'automation_failing:ev_1',
    triggerId: 'trig_1',
    triggerName: 'Nightly dependency bump',
    projectId: PROJECT.id,
    projectName: PROJECT.name,
    waitingSince: minutesAgo(300),
    automationFailing: {
      health: { consecutiveFailures: 3, lastFailureDetail: 'go mod tidy failed: module not found' },
      lastRunChatId: 'chat_c',
    },
  },
];

export interface MobileMockOptions {
  /** Inbox items ListInbox answers with. Default INBOX_ITEMS. */
  inbox?: unknown[];
}

export interface MobileMock {
  /** Every ResolveQuestion request body, in order. */
  answered: Record<string, unknown>[];
}

const json = (route: Route, body: unknown) =>
  route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });

/** A Connect server-stream that ends at once, cleanly: an end-stream envelope with `{}`. */
const emptyStream = (route: Route) =>
  route.fulfill({
    status: 200,
    contentType: 'application/connect+proto',
    body: Buffer.from([0x02, 0, 0, 0, 2, 0x7b, 0x7d]),
  });

export async function mockMobileBackend(page: Page, options: MobileMockOptions = {}): Promise<MobileMock> {
  const mock: MobileMock = { answered: [] };
  let inbox = [...(options.inbox ?? INBOX_ITEMS)];

  await page.route('**/reliant.v1.**', (route) => json(route, {}));
  await page.route('**/controlplane.v1.**', (route) => json(route, {}));
  await page.route('**/reliant.v1.StreamingService/**', emptyStream);
  await page.route('**/health', (route) => json(route, { auth_mode: 'apikey' }));

  await page.route('**/controlplane.v1.UserService/GetCurrentUser', (route) =>
    json(route, { user: { id: 'user_mobile', email: 'dev@example.com', onboardingCompleted: true } }),
  );
  await page.route('**/reliant.v1.ProjectService/ListProjects', (route) => json(route, { projects: [PROJECT] }));
  await page.route('**/reliant.v1.ProjectService/GetProject', (route) => json(route, { project: PROJECT }));
  await page.route('**/reliant.v1.ChatService/ListChats', (route) =>
    json(route, { chats: CHATS, totalCount: CHATS.length, lastSequence: '1' }),
  );
  await page.route('**/reliant.v1.ChatService/GetChat', (route) => {
    const { chatId } = route.request().postDataJSON() as { chatId?: string };
    return json(route, { chat: CHATS.find((c) => c.id === chatId) ?? CHATS[0] });
  });
  await page.route('**/reliant.v1.ChatService/ListMessages', (route) => {
    const { chatId } = route.request().postDataJSON() as { chatId?: string };
    const id = chatId ?? 'chat_a';
    const messages = MESSAGES.map((m) => ({ ...m, chatId: id, thread: id }));
    return json(route, { messages, total: messages.length, count: messages.length, hasMore: false, oldestSeq: '1' });
  });
  await page.route('**/reliant.v1.InboxService/ListInbox', (route) => {
    const blocking = inbox.filter((item) => [1, 2, 3].includes((item as { kind: number }).kind)).length;
    return json(route, { items: inbox, blockingCount: blocking, hasInformational: inbox.length > blocking });
  });
  await page.route('**/reliant.v1.InboxService/DismissInboxItem', async (route) => {
    const { itemIds = [] } = route.request().postDataJSON() as { itemIds?: string[] };
    inbox = inbox.filter((item) => !itemIds.includes((item as { itemId: string }).itemId));
    return json(route, {});
  });
  await page.route('**/reliant.v1.QuestionService/ResolveQuestion', async (route) => {
    mock.answered.push(route.request().postDataJSON() as Record<string, unknown>);
    inbox = inbox.filter((item) => (item as { kind: number }).kind !== 2);
    return json(route, {});
  });

  await page.addInitScript(() => {
    localStorage.setItem('reliant-api-key', 'test-key-123');
    // A build with no control plane configured (CI's, and a bare `vite`)
    // reads onboarding from here rather than GetCurrentUser.
    localStorage.setItem('reliant-local-onboarding-completed', 'true');
  });
  return mock;
}

/** A phone: 390×844, touch, coarse pointer — what the mobile redirect keys on. */
export const PHONE = {
  viewport: { width: 390, height: 844 },
  isMobile: true,
  hasTouch: true,
  deviceScaleFactor: 2,
} as const;
