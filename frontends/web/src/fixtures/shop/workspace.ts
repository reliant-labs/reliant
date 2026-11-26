// Copyright (c) 2025 Reliant Labs

/**
 * The reliant side of the shop story: who is signed in, the machines, the
 * projects, the worktrees and the chats the real app shows over the app layer
 * (app.ts). The forge console's side (releases, environments, the incident)
 * is shopStory.ts; this file names the same world from the product's side,
 * so the project is `shop` (forge project FIXTURE_PROJECT), its repository
 * DEMO_REPO and its org DEMO_ORG.
 *
 * The signed-in person is Alex Demo, the screenshot harness's user. Alex is
 * not in DEMO_CAST (the team the console's audit, audience and org members
 * show): adding Alex there would change what the public demo renders.
 *
 * Every time is relative to the pack clock (demoNow), read only when a
 * handler answers, never at import: shopPack({ now }) pins the clock after
 * this module loads.
 */

import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";

import { ChatActivity, ChatSchema, ChatState, WorkflowState, WorkflowStopReason, type Chat } from "@/gen/reliant/v1/chat_pb";
import { DaemonInfoSchema, DaemonLifecyclePhase, DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { ProjectSchema, type Project } from "@/gen/reliant/v1/project_pb";
import { WorktreeSchema, WorktreeStatus, WorktreeSummarySchema, type Worktree, type WorktreeSummary } from "@/gen/reliant/v1/worktree_pb";

import { DAY, DEMO_REPO, HOUR, MINUTE, demoNow } from "./shopStory";
import { APP_USER } from "./user";

// ── Clock ────────────────────────────────────────────────────────────────────

/** The pack clock minus `ago` ms, as the RFC 3339 string reliant's protos carry. */
export const isoAgo = (ago = 0) => new Date(demoNow() - ago).toISOString();
/** The pack clock plus `ahead` ms. */
export const isoAhead = (ahead: number) => new Date(demoNow() + ahead).toISOString();
/** The pack clock minus `ago` ms, as a google.protobuf.Timestamp. */
export const tsAgo = (ago = 0) => timestampFromMs(demoNow() - ago);

// ── People and places ────────────────────────────────────────────────────────

/** The signed-in person (user.ts). */
export { APP_USER };

/** shop's repository, as a git remote. */
export const SHOP_REMOTE = `https://${DEMO_REPO}.git`;

export const PROJECT_IDS = { shop: "shop-proj-shop", docs: "shop-proj-docs" } as const;

/** The reliant release the server runs (SystemService/Version) and the cloud machine's daemon reports. */
export const RELIANT_RELEASE = { version: "v1.31.0", commit: "4e1b7c2a9d3f5e6081b2c3d4e5f60718293a4b5c" } as const;

/** Alex's two machines: a cloud machine that is up, and a laptop that is asleep. */
export const DAEMON_IDS = { cloud: "shop-d-cloud", laptop: "shop-d-laptop" } as const;
export const DAEMON_NAMES = { cloud: "cloud-machine", laptop: "alex-macbook" } as const;

export const WORKTREE_IDS = {
  main: "shop-wt-main",
  rateLimit: "shop-wt-rate-limit",
  stripeWebhooks: "shop-wt-stripe-webhooks",
  docsMain: "shop-wt-docs-main",
} as const;

/** The chats: the sidebar's rows, and the transcripts data/transcripts/ holds. */
export const CHAT_IDS = {
  /** THE fixture chat: three turns of a get-it-right run that show every state the transcript draws. */
  cartTotals: "shop-chat-cart-totals",
  rateLimit: "shop-chat-rate-limit",
  /** The short chat: one question, a little work, a short answer. */
  loadTest: "shop-chat-load-test",
  stripeWebhooks: "shop-chat-stripe-webhooks",
  nightlyCi: "shop-chat-nightly-ci",
  mjmlEmails: "shop-chat-mjml-emails",
  untitled: "shop-chat-untitled",
  /** A finished turn of a dozen tool calls, with thinking and redacted thinking. */
  taxRates: "shop-chat-tax-rates",
  /** A turn that stopped on a failed tool call and a provider error. */
  stripeSdk: "shop-chat-stripe-sdk",
  webhooksGuide: "shop-chat-webhooks-guide",
} as const;

export const CHAT_TITLES = {
  cartTotals: "Refactor cart totals and fix multi-quantity discounts",
  rateLimit: "Add rate limiting to /checkout",
  taxRates: "Move tax rates behind the pricing config",
  stripeSdk: "Upgrade the Stripe SDK to v18",
} as const;

/** A chat's root workflow id. */
export const workflowIdOf = (chatId: string) => `wf-${chatId}`;

// ── Projects ─────────────────────────────────────────────────────────────────

export function shopProject(): Project {
  return create(ProjectSchema, {
    id: PROJECT_IDS.shop, userId: APP_USER.id, name: "shop", path: "/home/alex/code/shop",
    description: "Storefront API and admin", isGitRepo: true, defaultBranch: "main",
    createdAt: isoAgo(20 * DAY), updatedAt: isoAgo(2 * HOUR), lastActive: isoAgo(5 * MINUTE),
    remoteUrl: SHOP_REMOTE, isForge: true, forgeProjectName: "shop",
  });
}

export function docsProject(): Project {
  return create(ProjectSchema, {
    id: PROJECT_IDS.docs, userId: APP_USER.id, name: "docs", path: "/home/alex/code/docs", description: "Docs",
    isGitRepo: true, defaultBranch: "main", createdAt: isoAgo(9 * DAY), updatedAt: isoAgo(DAY),
    lastActive: isoAgo(3 * HOUR), remoteUrl: "https://github.com/acme/docs.git", isForge: false,
  });
}

/** The projects ListProjects answers, in the org. */
export const projects = (): Project[] => [shopProject(), docsProject()];

// ── Machines ─────────────────────────────────────────────────────────────────

export function daemons(): DaemonInfo[] {
  return [
    create(DaemonInfoSchema, {
      daemonId: DAEMON_IDS.cloud, userId: APP_USER.id, hostname: DAEMON_NAMES.cloud, platform: "linux/amd64",
      status: DaemonStatus.ACTIVE, connectedAt: tsAgo(3 * HOUR), lastHeartbeat: tsAgo(5000),
      daemonType: "cloud", lifecyclePhase: DaemonLifecyclePhase.READY, size: "m", createdAt: tsAgo(12 * DAY),
      // The build its daemon reported on attach (the Agent sandboxes page shows it).
      version: RELIANT_RELEASE.version, commit: RELIANT_RELEASE.commit,
    }),
    create(DaemonInfoSchema, {
      daemonId: DAEMON_IDS.laptop, userId: APP_USER.id, hostname: DAEMON_NAMES.laptop, platform: "darwin/arm64",
      status: DaemonStatus.SUSPENDED, connectedAt: tsAgo(9 * HOUR), lastHeartbeat: tsAgo(40 * MINUTE),
      daemonType: "self_hosted", lifecyclePhase: DaemonLifecyclePhase.SUSPENDED, size: "", createdAt: tsAgo(20 * DAY),
      lastStatusMessage: "Offline since 40 minutes ago",
    }),
  ];
}

// ── Worktrees ────────────────────────────────────────────────────────────────

interface WorktreeInit {
  id: string;
  name: string;
  branch: string;
  isMain: boolean;
  projectId?: string;
}

const WORKTREE_INITS: Record<keyof typeof WORKTREE_IDS, WorktreeInit> = {
  main: { id: WORKTREE_IDS.main, name: "shop", branch: "main", isMain: true },
  rateLimit: { id: WORKTREE_IDS.rateLimit, name: "rate-limit-checkout", branch: "feat/Checkout-RateLimit-per-IP-and-per-API-key", isMain: false },
  stripeWebhooks: { id: WORKTREE_IDS.stripeWebhooks, name: "stripe-webhooks", branch: "fix/stripe-webhook-retries", isMain: false },
  docsMain: { id: WORKTREE_IDS.docsMain, name: "docs", branch: "main", isMain: true, projectId: PROJECT_IDS.docs },
};

const worktreePath = (w: WorktreeInit) => (w.isMain ? `/home/alex/code/${w.name}` : `/home/alex/.reliant/worktrees/${w.id}`);

export function worktree(key: keyof typeof WORKTREE_IDS): Worktree {
  const w = WORKTREE_INITS[key];
  return create(WorktreeSchema, {
    id: w.id, name: w.name, branch: w.branch, baseBranch: "main", projectId: w.projectId ?? PROJECT_IDS.shop, isMain: w.isMain,
    status: WorktreeStatus.ACTIVE, path: worktreePath(w), createdAt: isoAgo(2 * DAY), updatedAt: isoAgo(2 * HOUR), lastActive: isoAgo(2 * HOUR),
  });
}

/** The summary a chat row carries. */
export function worktreeSummary(key: keyof typeof WORKTREE_IDS): WorktreeSummary {
  const w = WORKTREE_INITS[key];
  return create(WorktreeSummarySchema, { id: w.id, name: w.name, branch: w.branch, path: worktreePath(w), isMain: w.isMain, status: WorktreeStatus.ACTIVE, projectId: w.projectId ?? PROJECT_IDS.shop });
}

export const worktrees = (): Worktree[] => (Object.keys(WORKTREE_IDS) as (keyof typeof WORKTREE_IDS)[]).map(worktree);

// ── Chats ────────────────────────────────────────────────────────────────────

interface ChatInit {
  id: string;
  title: string;
  ago: number;
  worktree?: keyof typeof WORKTREE_IDS;
  projectId?: string;
  activity?: ChatActivity;
  workflowState?: WorkflowState;
  workflowStopReason?: WorkflowStopReason;
  workflowName?: string;
  launchKind?: string;
  triggerId?: string;
  unread?: boolean;
  appName?: string;
}

export function chat(o: ChatInit): Chat {
  const at = isoAgo(o.ago);
  return create(ChatSchema, {
    id: o.id, title: o.title, userId: APP_USER.id, projectId: o.projectId ?? PROJECT_IDS.shop,
    worktreeId: WORKTREE_IDS[o.worktree ?? "main"], worktree: worktreeSummary(o.worktree ?? "main"),
    workflowName: o.workflowName ?? "builtin://agent", state: ChatState.IDLE, needsRecovery: false,
    activity: o.activity ?? ChatActivity.IDLE, unread: o.unread ?? false, activeDaemonId: DAEMON_IDS.cloud,
    workflowState: o.workflowState ?? WorkflowState.STOPPED, workflowStopReason: o.workflowStopReason ?? WorkflowStopReason.COMPLETED,
    launchKind: o.launchKind ?? "chat.start", noMachine: false, workflowId: workflowIdOf(o.id),
    createdAt: isoAgo(o.ago + HOUR), updatedAt: at, lastActive: at, lastMessageAt: at,
    ...(o.triggerId ? { triggerId: o.triggerId } : {}),
    ...(o.appName ? { appName: o.appName } : {}),
  });
}

/**
 * A sidebar's worth of chats: two projects, branched chats, a long branch
 * name, running / needs-input / failed / unread / automation rows, an
 * untitled chat, and two started from the shop app (so they carry its tag).
 */
export function chats(): Chat[] {
  return [
    chat({ id: CHAT_IDS.cartTotals, title: CHAT_TITLES.cartTotals, ago: 5000, activity: ChatActivity.RUNNING, workflowState: WorkflowState.ACTIVE, workflowStopReason: WorkflowStopReason.UNSPECIFIED, workflowName: "builtin://get-it-right", appName: "shop" }),
    chat({ id: CHAT_IDS.rateLimit, title: CHAT_TITLES.rateLimit, worktree: "rateLimit", activity: ChatActivity.AWAITING_INPUT, workflowState: WorkflowState.ACTIVE, ago: 2 * MINUTE }),
    chat({ id: CHAT_IDS.loadTest, title: "Load-test the limiter (spawned from rate limiting)", worktree: "rateLimit", unread: true, ago: 40 * MINUTE }),
    chat({ id: CHAT_IDS.stripeWebhooks, title: "Retry Stripe webhooks with idempotency keys", worktree: "stripeWebhooks", activity: ChatActivity.ERROR, workflowStopReason: WorkflowStopReason.FAILED, ago: 3 * HOUR, appName: "shop" }),
    // Last night's firing failed (the automation is failing: handlers/reliant/workflows.ts).
    chat({ id: CHAT_IDS.nightlyCi, title: "Nightly: triage failing CI", launchKind: "trigger.schedule", triggerId: "shop-trg-nightly", activity: ChatActivity.ERROR, workflowStopReason: WorkflowStopReason.FAILED, ago: 7 * HOUR }),
    chat({ id: CHAT_IDS.mjmlEmails, title: "Migrate order confirmation emails from inline HTML strings to MJML templates with snapshot tests", ago: 26 * HOUR }),
    chat({ id: CHAT_IDS.untitled, title: "", ago: 3 * DAY }),
    chat({ id: CHAT_IDS.taxRates, title: CHAT_TITLES.taxRates, ago: 12 * MINUTE }),
    chat({ id: CHAT_IDS.stripeSdk, title: CHAT_TITLES.stripeSdk, activity: ChatActivity.ERROR, workflowStopReason: WorkflowStopReason.FAILED, ago: 95 * MINUTE }),
    chat({ id: CHAT_IDS.webhooksGuide, title: "Write the webhooks guide", projectId: PROJECT_IDS.docs, worktree: "docsMain", ago: 5 * HOUR }),
  ];
}
