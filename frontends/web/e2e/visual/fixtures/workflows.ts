/**
 * Workflows and Runs fixtures (a surface's `harness.fixtures`, not in the
 * shared table: the sidebar and the composer read the same RPCs and must keep
 * their own data): the Workflows list (built-in and your own
 * workflows, two with automations switched on), and the Runs list (finished,
 * live, needs-input and failed runs, from chats, schedules and an agent).
 */
import type { Fixtures } from '../rpc'
import { HOUR, IDS, MIN, WORKTREES, iso, isoAhead } from './base'

const workflow = (name: string, title: string, description: string, source: 'builtin' | 'user', steps: number) => ({
  name, filename: name, title, description, stepCount: steps, source,
  nodes: [], edges: [], inputs: {}, outputs: {}, isHidden: false, hasPresetGroups: false, needsMachine: [], automationOnly: false,
  status: 'WORKFLOW_DRAFT_STATUS_COMPLETE', validationErrors: [], triggers: [], permissions: [], updatedAt: iso(3 * HOUR),
})

const WORKFLOWS = [
  workflow('agent', 'Agent', 'A single agent with every tool.', 'builtin', 1),
  workflow('get-it-right', 'Get It Right', 'Implement, review and verify until it is right.', 'builtin', 3),
  workflow('parallel-compete', 'Best of Three', 'Three attempts in parallel; keep the best.', 'builtin', 4),
  workflow('nightly-ci-triage', 'Nightly CI triage', 'Read last night\u2019s failing CI jobs, group them by cause and open one issue per cause.', 'user', 3),
  workflow('release-notes', 'Draft release notes', 'Summarize the merged pull requests since the last tag into release notes.', 'user', 2),
  workflow('dependency-bump', 'Weekly dependency bump', 'Bump patch versions, run the tests and open a pull request.', 'user', 4),
]

const trigger = (id: string, name: string, workflow: string, cron: string, extra: Record<string, unknown> = {}) => ({
  id, name, projectId: IDS.project, worktreeId: WORKTREES.main.id, enabled: true, workflow, presets: {}, params: {}, message: '',
  createdAt: iso(20 * 24 * HOUR), updatedAt: iso(24 * HOUR), nextFireAt: isoAhead(5 * HOUR), changes: [],
  daemonId: IDS.cloudDaemon, noMachine: false, machinePoolId: '', machinePoolName: '', projectName: 'shopfront', daemonName: 'cloud-machine',
  machineState: 'connected', notifyOnComplete: false, filter: '',
  health: { status: 'TRIGGER_HEALTH_STATUS_HEALTHY', consecutiveFailures: 0, consecutiveSkips: 0, lastFailureDetail: '', setupIssues: [] },
  source: { case: 'schedule', value: { cron: [cron], timezone: 'UTC', overlap: 'TRIGGER_OVERLAP_POLICY_SKIP' } },
  schedule: { cron: [cron], timezone: 'UTC', overlap: 'TRIGGER_OVERLAP_POLICY_SKIP' },
  ...extra,
})

const TRIGGERS = [
  trigger('trg-nightly', 'Nightly CI triage', 'nightly-ci-triage', '0 6 * * *'),
  trigger('trg-weekly', 'Weekly dependency bump', 'dependency-bump', '0 9 * * 1', { nextFireAt: isoAhead(4 * 24 * HOUR) }),
]

type RunInit = { id: string; title: string; workflow: string; ago: number; took?: number; display: string; outcome?: string; launch?: string; trigger?: string; state?: string }
const run = (r: RunInit) => ({
  id: r.id, sessionId: r.id, workflowName: r.workflow, thread: r.id, parentId: '', spawnedByNodeId: '', loopIteration: '0',
  state: r.state ?? 'WORKFLOW_STATE_STOPPED', stopReason: 'WORKFLOW_STOP_REASON_COMPLETED', outcome: '',
  createdAtMs: String(Date.parse(iso(r.ago))), completedAtMs: r.took ? String(Date.parse(iso(r.ago)) + r.took) : '0',
  title: r.title, projectId: IDS.project, launchKind: r.launch ?? 'chat.start', triggerId: r.trigger ?? '', triggerName: r.trigger === 'trg-weekly' ? 'Weekly dependency bump' : r.trigger ? 'Nightly CI triage' : '',
  daemonId: IDS.cloudDaemon, activity: 'CHAT_ACTIVITY_IDLE', displayState: r.display, archived: false, failureDetail: '',
  runOutcome: r.outcome ?? 'RUN_OUTCOME_SUCCEEDED',
})

const RUNS = [
  run({ id: 'run-0001', title: 'Refactor cart totals and fix multi-quantity discounts', workflow: 'builtin://get-it-right', ago: 18 * MIN,
    display: 'RUN_DISPLAY_STATE_RUNNING', state: 'WORKFLOW_STATE_ACTIVE', outcome: 'RUN_OUTCOME_UNSPECIFIED' }),
  run({ id: 'run-0002', title: 'Add rate limiting to /checkout', workflow: 'builtin://agent', ago: 52 * MIN,
    display: 'RUN_DISPLAY_STATE_NEEDS_INPUT', state: 'WORKFLOW_STATE_ACTIVE', outcome: 'RUN_OUTCOME_UNSPECIFIED' }),
  run({ id: 'run-0003', title: 'Draft release notes for v2.14', workflow: 'release-notes', ago: 2 * HOUR, took: 7 * MIN, display: 'RUN_DISPLAY_STATE_COMPLETED' }),
  run({ id: 'run-0004', title: 'Retry Stripe webhooks with idempotency keys', workflow: 'builtin://agent', ago: 3 * HOUR, took: 21 * MIN,
    display: 'RUN_DISPLAY_STATE_FAILED', outcome: 'RUN_OUTCOME_FAILED' }),
  run({ id: 'run-0005', title: 'Nightly: triage failing CI', workflow: 'nightly-ci-triage', ago: 9 * HOUR, took: 4 * MIN, display: 'RUN_DISPLAY_STATE_COMPLETED',
    launch: 'schedule', trigger: 'trg-nightly' }),
  run({ id: 'run-0006', title: 'Load-test the limiter', workflow: 'builtin://parallel-compete', ago: 11 * HOUR, took: 13 * MIN, display: 'RUN_DISPLAY_STATE_COMPLETED' }),
  run({ id: 'run-0007', title: 'Weekly dependency bump', workflow: 'dependency-bump', ago: 15 * HOUR, took: 9 * MIN, display: 'RUN_DISPLAY_STATE_COMPLETED',
    launch: 'schedule', trigger: 'trg-weekly' }),
  run({ id: 'run-0008', title: 'Migrate order emails to MJML templates', workflow: 'builtin://get-it-right', ago: 20 * HOUR, took: 38 * MIN, display: 'RUN_DISPLAY_STATE_COMPLETED' }),
]

export const workflowFixtures: Fixtures = {
  'reliant.v1.WorkflowService/ListWorkflows': { workflows: WORKFLOWS, total: WORKFLOWS.length, invalidWorkflows: [] },
  'reliant.v1.TriggerService/ListTriggers': { triggers: TRIGGERS },
  'reliant.v1.RunService/ListRuns': { runs: RUNS, total: RUNS.length, nextPageToken: '', unlaunchedEvents: [] },
  'reliant.v1.RunService/LastRunPerWorkflow': {
    runs: [RUNS[2], RUNS[3], RUNS[4], RUNS[5], RUNS[7]].filter(Boolean),
  },
}

