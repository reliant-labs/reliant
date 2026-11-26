// Copyright (c) 2025 Reliant Labs

/**
 * WorkflowService, RunService, TriggerService and PresetService: the
 * Workflows list (the three built-ins and three of Alex's own, two with
 * automations switched on, one of them failing) and the Runs list (finished,
 * live, needs-input and failed runs, from chats, schedules and an agent), all
 * in shop. Alex has saved no presets.
 */
import type { MessageInitShape } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import { ChatActivity, WorkflowState, WorkflowStopReason } from "@/gen/reliant/v1/chat_pb";
import { PresetService } from "@/gen/reliant/v1/preset_pb";
import { RunDisplayState, RunOutcome, RunService, type RunSchema } from "@/gen/reliant/v1/run_pb";
import { TriggerHealthStatus, TriggerOverlapPolicy, TriggerService, type TriggerSchema } from "@/gen/reliant/v1/trigger_pb";
import { WorkflowDraftStatus, WorkflowService } from "@/gen/reliant/v1/workflow_pb";

import { DAY, HOUR, MINUTE, demoNow } from "../../shopStory";
import { DAEMON_IDS, DAEMON_NAMES, PROJECT_IDS, WORKTREE_IDS, isoAgo, isoAhead } from "../../workspace";
import { serve } from "../serve";

export const TRIGGER_IDS = { nightly: "shop-trg-nightly", weekly: "shop-trg-weekly" } as const;

const workflow = (name: string, title: string, description: string, source: "builtin" | "user", steps: number) => ({
  name, filename: name, title, description, stepCount: steps, source,
  nodes: [], edges: [], inputs: {}, outputs: {}, isHidden: false, hasPresetGroups: false, needsMachine: [], automationOnly: false,
  status: WorkflowDraftStatus.COMPLETE, validationErrors: [], triggers: [], permissions: [], updatedAt: isoAgo(3 * HOUR),
});

const workflows = () => [
  workflow("agent", "Agent", "A single agent with every tool.", "builtin", 1),
  workflow("get-it-right", "Get It Right", "Implement, review and verify until it is right.", "builtin", 3),
  workflow("parallel-compete", "Best of Three", "Three attempts in parallel; keep the best.", "builtin", 4),
  workflow("nightly-ci-triage", "Nightly CI triage", "Read last night\u2019s failing CI jobs, group them by cause and open one issue per cause.", "user", 3),
  workflow("release-notes", "Draft release notes", "Summarize the merged pull requests since the last tag into release notes.", "user", 2),
  workflow("dependency-bump", "Weekly dependency bump", "Bump patch versions, run the tests and open a pull request.", "user", 4),
];

type TriggerInit = MessageInitShape<typeof TriggerSchema>;
const trigger = (id: string, name: string, workflowName: string, cron: string, extra: Pick<TriggerInit, "nextFireAt" | "health"> = {}): TriggerInit => ({
  id, name, projectId: PROJECT_IDS.shop, worktreeId: WORKTREE_IDS.main, enabled: true, workflow: workflowName, presets: {}, params: {}, message: "",
  createdAt: isoAgo(20 * DAY), updatedAt: isoAgo(DAY), nextFireAt: isoAhead(5 * HOUR), changes: [],
  daemonId: DAEMON_IDS.cloud, noMachine: false, machinePoolId: "", machinePoolName: "", projectName: "shop", daemonName: DAEMON_NAMES.cloud,
  machineState: "connected", notifyOnComplete: false, filter: "",
  health: { status: TriggerHealthStatus.HEALTHY, consecutiveFailures: 0, consecutiveSkips: 0, lastFailureDetail: "", setupIssues: [] },
  source: { case: "schedule", value: { cron: [cron], timezone: "UTC", overlap: TriggerOverlapPolicy.SKIP } },
  ...extra,
});

/**
 * Nightly CI triage has failed its last two nights: GitHub refuses the token
 * it reads CI with. The Inbox lists it (inbox.ts), and its last run and chat
 * show the failure.
 */
export const NIGHTLY_FAILURE = "gh: HTTP 401: Bad credentials (the GitHub token for acme/shop expired)";
export const nightlyHealth = () => ({
  status: TriggerHealthStatus.FAILING, consecutiveFailures: 2, consecutiveSkips: 0, lastFailureDetail: NIGHTLY_FAILURE, setupIssues: [],
});

const triggers = () => [
  trigger(TRIGGER_IDS.nightly, "Nightly CI triage", "nightly-ci-triage", "0 6 * * *", { health: nightlyHealth() }),
  trigger(TRIGGER_IDS.weekly, "Weekly dependency bump", "dependency-bump", "0 9 * * 1", { nextFireAt: isoAhead(4 * DAY) }),
];

interface RunInit {
  id: string;
  title: string;
  workflow: string;
  ago: number;
  took?: number;
  display: RunDisplayState;
  outcome?: RunOutcome;
  launch?: string;
  trigger?: string;
  state?: WorkflowState;
  failure?: string;
}

type RunMessageInit = MessageInitShape<typeof RunSchema>;
function run(r: RunInit): RunMessageInit {
  const startedMs = demoNow() - r.ago;
  return {
    id: r.id, sessionId: r.id, workflowName: r.workflow, thread: r.id, parentId: "", spawnedByNodeId: "", loopIteration: 0n,
    state: r.state ?? WorkflowState.STOPPED, stopReason: WorkflowStopReason.COMPLETED, outcome: "",
    createdAtMs: BigInt(startedMs), completedAtMs: r.took ? BigInt(startedMs + r.took) : 0n,
    title: r.title, projectId: PROJECT_IDS.shop, launchKind: r.launch ?? "chat.start", triggerId: r.trigger ?? "",
    triggerName: r.trigger === TRIGGER_IDS.weekly ? "Weekly dependency bump" : r.trigger ? "Nightly CI triage" : "",
    daemonId: DAEMON_IDS.cloud, activity: ChatActivity.IDLE, displayState: r.display, archived: false, failureDetail: r.failure ?? "",
    runOutcome: r.outcome ?? RunOutcome.SUCCEEDED,
  };
}

const runs = () => [
  run({ id: "shop-run-0001", title: "Refactor cart totals and fix multi-quantity discounts", workflow: "builtin://get-it-right", ago: 18 * MINUTE, display: RunDisplayState.RUNNING, state: WorkflowState.ACTIVE, outcome: RunOutcome.UNSPECIFIED }),
  run({ id: "shop-run-0002", title: "Add rate limiting to /checkout", workflow: "builtin://agent", ago: 52 * MINUTE, display: RunDisplayState.NEEDS_INPUT, state: WorkflowState.ACTIVE, outcome: RunOutcome.UNSPECIFIED }),
  run({ id: "shop-run-0003", title: "Draft release notes for v1.8.4", workflow: "release-notes", ago: 2 * HOUR, took: 7 * MINUTE, display: RunDisplayState.COMPLETED }),
  run({ id: "shop-run-0004", title: "Retry Stripe webhooks with idempotency keys", workflow: "builtin://agent", ago: 3 * HOUR, took: 21 * MINUTE, display: RunDisplayState.FAILED, outcome: RunOutcome.FAILED }),
  run({ id: "shop-run-0005", title: "Nightly: triage failing CI", workflow: "nightly-ci-triage", ago: 9 * HOUR, took: 4 * MINUTE, display: RunDisplayState.FAILED, outcome: RunOutcome.FAILED, launch: "schedule", trigger: TRIGGER_IDS.nightly, failure: NIGHTLY_FAILURE }),
  run({ id: "shop-run-0006", title: "Load-test the limiter", workflow: "builtin://parallel-compete", ago: 11 * HOUR, took: 13 * MINUTE, display: RunDisplayState.COMPLETED }),
  run({ id: "shop-run-0007", title: "Weekly dependency bump", workflow: "dependency-bump", ago: 15 * HOUR, took: 9 * MINUTE, display: RunDisplayState.COMPLETED, launch: "schedule", trigger: TRIGGER_IDS.weekly }),
  run({ id: "shop-run-0008", title: "Migrate order emails to MJML templates", workflow: "builtin://get-it-right", ago: 20 * HOUR, took: 38 * MINUTE, display: RunDisplayState.COMPLETED }),
];

export function workflowHandlers() {
  return {
    ...serve(WorkflowService, {
      listWorkflows: () => {
        const list = workflows();
        return { workflows: list, total: list.length, invalidWorkflows: [] };
      },
      // A listed workflow's definition (the composer reads the selected one's inputs): none takes any.
      getWorkflow: (req) => {
        const name = req.name.replace(/^builtin:\/\//, "");
        const w = workflows().find((x) => x.name === name);
        if (!w) throw new ConnectError("workflow not found", Code.NotFound);
        return {
          workflow: { name: w.name, title: w.title, description: w.description }, source: w.source, version: w.source === "builtin" ? 0n : 3n,
          yamlDefinition: `name: ${w.name}\ntitle: ${w.title}\ndescription: ${w.description}\n`, status: WorkflowDraftStatus.COMPLETE, validationErrors: [],
        };
      },
    }),
    // Alex has saved no presets: every workflow runs on its own defaults.
    ...serve(PresetService, {
      listPresetsForWorkflow: () => ({ presets: [], invalidPresets: [] }),
      getDefaultPresetsBatch: () => ({ presetsByWorkflow: {} }),
    }),
    ...serve(TriggerService, {
      listTriggers: () => ({ triggers: triggers() }),
    }),
    ...serve(RunService, {
      listRuns: () => {
        const list = runs();
        return { runs: list, total: list.length, nextPageToken: "", unlaunchedEvents: [] };
      },
      // Each workflow's latest finished run.
      lastRunPerWorkflow: () => {
        const list = runs();
        return { runs: [list[2], list[3], list[4], list[5], list[7]] };
      },
    }),
  };
}
