// Copyright (c) 2025 Reliant Labs

/**
 * The shop pack's app layer (app.ts): every key it answers answers over the
 * fixture transport with a schema-valid message that survives the wire, its
 * static JSON loads strictly on the pack clock, its streams stay open until
 * aborted, and the demo pack (index.ts) never reaches it.
 */
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { create, equals, fromBinary, isMessage, toBinary, type DescMessage, type DescMethod, type DescMethodStreaming, type DescMethodUnary, type DescService } from "@bufbuild/protobuf";
import { MethodOptions_IdempotencyLevel, timestampMs } from "@bufbuild/protobuf/wkt";
import { createClient, createRouterTransport, type Transport } from "@connectrpc/connect";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { BillingService } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { GitCredentialService } from "@/gen/controlplane/services/git_credential/v1/git_credential_pb";
import { ObservabilityBillingService } from "@/gen/controlplane/services/observability_billing/v1/observability_billing_pb";
import { DaemonService } from "@/gen/controlplane/services/daemon/v1/daemon_pb";
import { LLMGatewayService } from "@/gen/controlplane/services/llm_gateway/v1/llm_gateway_pb";
import { OrganizationService } from "@/gen/controlplane/services/organization/v1/organization_pb";
import { UserService } from "@/gen/controlplane/services/user/v1/user_pb";
import { ApprovalService } from "@/gen/reliant/v1/approval_pb";
import { AttachmentService, GetAttachmentResponseSchema } from "@/gen/reliant/v1/attachment_pb";
import { BackgroundService } from "@/gen/reliant/v1/background_pb";
import { CatalogService } from "@/gen/reliant/v1/catalog_pb";
import { ChatService, ListMessagesResponseSchema } from "@/gen/reliant/v1/chat_pb";
import { ConnectorService } from "@/gen/reliant/v1/connector_pb";
import { DaemonRegistryService } from "@/gen/reliant/v1/daemon_registry_pb";
import { ForgeService } from "@/gen/reliant/v1/forge_pb";
import { InboxItemKind, InboxService } from "@/gen/reliant/v1/inbox_pb";
import { MCPService } from "@/gen/reliant/v1/mcp_pb";
import { PresetService } from "@/gen/reliant/v1/preset_pb";
import { ListProjectsWithAppsResponseSchema, ProjectService } from "@/gen/reliant/v1/project_pb";
import { ProviderAccountService } from "@/gen/reliant/v1/provider_account_pb";
import { QuestionService } from "@/gen/reliant/v1/question_pb";
import { RunService } from "@/gen/reliant/v1/run_pb";
import { SettingsService } from "@/gen/reliant/v1/settings_pb";
import { ConnectionService } from "@/gen/reliant/v1/connection_pb";
import { SharingService } from "@/gen/reliant/v1/sharing_pb";
import { StreamingService } from "@/gen/reliant/v1/streaming_pb";
import { SystemService } from "@/gen/reliant/v1/system_pb";
import { TriggerService } from "@/gen/reliant/v1/trigger_pb";
import { WorkflowService } from "@/gen/reliant/v1/workflow_pb";
import { WorktreeService } from "@/gen/reliant/v1/worktree_pb";

import checkoutTotal from "../data/attachments/checkout-total.json?raw";
import catalog from "../data/catalog.json?raw";
import cartTotals from "../data/transcripts/cart-totals.json?raw";
import loadTest from "../data/transcripts/load-test.json?raw";
import stripeSdk from "../data/transcripts/stripe-sdk.json?raw";
import subAgents from "../data/transcripts/sub-agents.json?raw";
import taxRates from "../data/transcripts/tax-rates.json?raw";
import { createFixtureTransport } from "../../transport";
import { APP_LAYER_ANSWERS, appLayerScenarioHandlers, createShopAppTransport, shopAppPack } from "../app";
import { FAKE_MCP_HEADER } from "../handlers/reliant/mcp";
import { chatStreamEvents, streamingImpl } from "../handlers/reliant/streaming";
import { SHOP_READ_ONLY_MESSAGE, shopScenario } from "../index";
import { DATA_ANCHOR, decodeFixtureJson, packJson } from "../packJson";
import { HOUR, demoNow, resetDemoNowForTests } from "../shopStory";
import { CHAT_IDS, PROJECT_IDS, WORKTREE_IDS } from "../workspace";

const FIXED_NOW = Date.UTC(2026, 10, 18, 15, 30);

const SERVICES: DescService[] = [
  ApprovalService, AttachmentService, CatalogService, ChatService, DaemonRegistryService, ForgeService, MCPService, ProjectService, QuestionService, RunService,
  SettingsService, StreamingService, TriggerService, WorkflowService, WorktreeService, BillingService, UserService, ObservabilityBillingService,
  BackgroundService, ConnectionService, ConnectorService, InboxService, PresetService, ProviderAccountService, SharingService, SystemService,
  DaemonService, LLMGatewayService, OrganizationService, GitCredentialService,
];
const METHODS = new Map<string, DescMethod>(SERVICES.flatMap((s) => s.methods.map((m) => [`${s.typeName}/${m.name}`, m] as const)));

/** What the app layer answers: phase 2's Appendix A b-rl and b-cp rows, plus the observability usage the visual harness answers and forge's reports the console preview reads. */
const RELIANT_KEYS = [
  "ApprovalService/ListApprovalsByChat", "AttachmentService/GetAttachment", "BackgroundService/ListProcesses", "CatalogService/ListAvailableModels",
  "CatalogService/ListModels", "CatalogService/SearchCatalog", "ChatService/GetChat", "ChatService/GetWorkflowExecutions", "ChatService/ListArchivedChats",
  "ChatService/ListChatProjects", "ChatService/ListChats", "ChatService/ListMessages", "ChatService/ListQueuedAgentMessages", "ConnectorService/ListConnectors",
  "DaemonRegistryService/ListDaemons", "InboxService/ListInbox", "PresetService/GetDefaultPresetsBatch", "PresetService/ListPresetsForWorkflow",
  "ProjectService/ListProjectDaemons", "ProviderAccountService/GetChatProviderAccount", "ProviderAccountService/ListProviderAccounts",
  "SettingsService/GetPreferences", "SettingsService/GetShortcuts", "SharingService/ListSharedChats", "StreamingService/StreamOrgUpdates",
  "SystemService/Version", "WorkflowService/GetWorkflow", "MCPService/ListRecommended", "MCPService/ListServers", "ForgeService/GetEnvShape", "ForgeService/GetEnvStatus", "ForgeService/GetTopology", "ProjectService/GetProject", "ProjectService/ListProjects", "ProjectService/ListProjectsWithApps",
  "QuestionService/ListAsyncQuestions", "QuestionService/ListPendingQuestions", "RunService/LastRunPerWorkflow", "RunService/ListRuns",
  "SettingsService/GetConfigHealth", "SettingsService/GetPrivacySettings", "SettingsService/GetProviderStatuses", "SettingsService/ListSettings",
  "StreamingService/StreamChatUpdates", "StreamingService/StreamUserUpdates", "TriggerService/ListTriggers", "WorkflowService/ListWorkflows",
  "WorktreeService/GetWorktree", "WorktreeService/ListWorktrees",
  // The public demo's Agents pages: Connectors' integrations, a thread's and a run's workflow.
  "ConnectionService/ListIntegrations", "ConnectionService/ListConnections", "ConnectionService/GetConnection",
  "ChatService/GetThreadWorkflowInputs", "ChatService/GetWorkflowNodeExecutions",
].map((k) => `reliant.v1.${k}`);
const CONTROL_PLANE_KEYS = [
  ...["GetCurrentUserBillingEmail", "GetCurrentUserComputeEligibility", "GetCurrentUserComputeSubscription", "GetCurrentUserComputeUsage",
    "GetCurrentUserReliantOverview", "GetCurrentUserSubscription", "GetCurrentUserWalletOverview", "GetProjectCost", "GetStorageUsage",
    "ListCurrentUserInvoices", "ListOrganizationUsage", "ListPlans"].map((m) => `controlplane.v1.BillingService/${m}`),
  "controlplane.v1.UserService/GetCurrentUser",
  "controlplane.v1.OrganizationService/ListMembers",
  "controlplane.v1.LLMGatewayService/GetLLMSpend",
  "controlplane.v1.DaemonService/GetDaemon",
  "controlplane.v1.DaemonService/ListPortAccessRules",
  "observability_billing.v1.ObservabilityBillingService/GetObservabilityUsage",
  "controlplane.v1.GitCredentialService/GetGitCredential",
  "controlplane.v1.OrganizationService/ListGitHubAppInstallations",
  "controlplane.v1.OrganizationService/ListLinkableGitHubAppInstallations",
];

/** The request each key is asked with: one that names something the story has. */
const REQUESTS: Record<string, Record<string, unknown>> = {
  "reliant.v1.ApprovalService/ListApprovalsByChat": { chatId: CHAT_IDS.cartTotals },
  "reliant.v1.AttachmentService/GetAttachment": { id: "att-c1-img" },
  "reliant.v1.ChatService/GetChat": { chatId: CHAT_IDS.cartTotals },
  "reliant.v1.ChatService/GetWorkflowExecutions": { chatId: CHAT_IDS.cartTotals },
  "reliant.v1.ChatService/ListMessages": { chatId: CHAT_IDS.cartTotals },
  "reliant.v1.ForgeService/GetEnvShape": { projectId: PROJECT_IDS.shop, env: "prod" },
  "reliant.v1.ForgeService/GetEnvStatus": { projectId: PROJECT_IDS.shop, env: "prod" },
  "reliant.v1.ForgeService/GetTopology": { projectId: PROJECT_IDS.shop },
  "reliant.v1.ProjectService/GetProject": { projectId: PROJECT_IDS.shop },
  "reliant.v1.QuestionService/ListAsyncQuestions": { chatId: CHAT_IDS.cartTotals },
  "reliant.v1.StreamingService/StreamChatUpdates": { chatId: CHAT_IDS.cartTotals },
  "reliant.v1.WorktreeService/GetWorktree": { worktreeId: WORKTREE_IDS.main },
  "controlplane.v1.BillingService/GetProjectCost": { projectId: "shop" },
  "reliant.v1.ChatService/ListChatProjects": { chatId: CHAT_IDS.cartTotals },
  "reliant.v1.ProviderAccountService/GetChatProviderAccount": { chatId: CHAT_IDS.cartTotals },
  "reliant.v1.WorkflowService/GetWorkflow": { projectId: PROJECT_IDS.shop, name: "builtin://agent" },
  "controlplane.v1.OrganizationService/ListMembers": { orgId: "org-acme" },
  "controlplane.v1.DaemonService/GetDaemon": { daemonId: "shop-d-cloud" },
  "controlplane.v1.DaemonService/ListPortAccessRules": { daemonId: "shop-d-cloud" },
  "reliant.v1.ConnectionService/GetConnection": { id: "shop-conn-github" },
  "reliant.v1.ChatService/GetThreadWorkflowInputs": { chatId: CHAT_IDS.cartTotals, threadId: "th-c1-review" },
  "reliant.v1.ChatService/GetWorkflowNodeExecutions": { chatId: CHAT_IDS.cartTotals, workflowId: "wf-c1-review" },
  "controlplane.v1.OrganizationService/ListGitHubAppInstallations": { orgId: "org-acme" },
  "controlplane.v1.OrganizationService/ListLinkableGitHubAppInstallations": { orgId: "org-acme" },
};

/** The app pack, served as fixture mode serves it (today's read-only classifier, the pack clock). */
const appTransport = (): Transport => createFixtureTransport({ pack: shopAppPack({ now: FIXED_NOW }), now: demoNow, readOnly: true, readOnlyMessage: SHOP_READ_ONLY_MESSAGE });

/** A message survives the wire: binary out and back in is the same message. */
function expectWireSafe(schema: DescMessage, message: unknown) {
  expect(isMessage(message, schema)).toBe(true);
  const back = fromBinary(schema, toBinary(schema, message as never));
  expect(equals(schema, back, message as never)).toBe(true);
}

const pendingFor = (ms: number) => new Promise((resolve) => setTimeout(() => resolve("still open"), ms));

beforeEach(() => resetDemoNowForTests());

describe("the app layer", () => {
  it("answers exactly the app's keys, each naming a real rpc", () => {
    const keys = Object.keys(appLayerScenarioHandlers()).sort();
    expect(keys).toEqual([...RELIANT_KEYS, ...CONTROL_PLANE_KEYS].sort());
    for (const key of keys) expect(METHODS.has(key), key).toBe(true);
    expect(RELIANT_KEYS).toHaveLength(54);
  });

  it.each([...RELIANT_KEYS, ...CONTROL_PLANE_KEYS].filter((k) => METHODS.get(k)?.methodKind === "unary"))("%s answers a schema-valid, wire-safe message", async (key) => {
    const method = METHODS.get(key)!;
    const res = await appTransport().unary(method as DescMethodUnary, undefined, undefined, undefined, create(method.input, REQUESTS[key] as never), undefined);
    expectWireSafe(method.output, res.message);
  });

  it.each(RELIANT_KEYS.filter((k) => METHODS.get(k)?.methodKind === "server_streaming"))("%s yields its frames, then stays open", async (key) => {
    const method = METHODS.get(key)!;
    async function* one() {
      yield create(method.input, REQUESTS[key] as never);
    }
    const res = await appTransport().stream(method as DescMethodStreaming, undefined, undefined, undefined, one(), undefined);
    const it = res.message[Symbol.asyncIterator]();
    const frames = key.endsWith("StreamChatUpdates") ? 2 : 1;
    for (let i = 0; i < frames; i++) {
      const next = await it.next();
      expect(next.done).toBe(false);
      expectWireSafe(method.output, next.value);
    }
    expect(await Promise.race([it.next(), pendingFor(30)])).toBe("still open");
  });

  it("serves the demo pack under it", () => {
    const pack = shopAppPack({ now: FIXED_NOW });
    expect(pack.extends?.map((layer) => layer.name)).toEqual(["shop"]);
    const scenario = pack.scenario;
    const resolved = typeof scenario === "function" ? scenario({ now: demoNow, idPrefix: "", id: (s) => s, data: {} }) : scenario;
    expect(Object.keys(resolved ?? {}).sort()).toEqual(Object.keys(appLayerScenarioHandlers()).sort());
    expect(Object.keys(shopScenario().handlers).length).toBeGreaterThan(0);
  });

  it("answers its named side-effecting and bearer-link reads under readOnly: true, and refuses a write", async () => {
    const strict = createFixtureTransport({ pack: shopAppPack({ now: FIXED_NOW }), now: demoNow, readOnly: true, readOnlyMessage: SHOP_READ_ONLY_MESSAGE });
    for (const key of APP_LAYER_ANSWERS) {
      const method = METHODS.get(key)!;
      const res = await strict.unary(method as DescMethodUnary, undefined, undefined, undefined, create(method.input, REQUESTS[key] as never), undefined);
      expectWireSafe(method.output, res.message);
    }
    await expect(createClient(ChatService, strict).deleteChat({ chatId: CHAT_IDS.cartTotals })).rejects.toThrow(SHOP_READ_ONLY_MESSAGE);
  });

  it("refuses app-layer mutations without forwarding them to real services", async () => {
    const unary = vi.fn<Transport["unary"]>();
    const stream = vi.fn<Transport["stream"]>();
    const transport = createShopAppTransport({ now: FIXED_NOW, passthrough: { fallback: { unary, stream } } });
    // Prove the public demo's app layer is selected, not just an unavailable service.
    const chats = createClient(ChatService, transport);
    expect((await chats.listMessages({ chatId: CHAT_IDS.cartTotals })).messages.length).toBeGreaterThan(0);
    // Exercise writes across the app services, not just a hand-picked delete.
    const mutations = SERVICES.flatMap((service) => service.methods).filter((method) =>
      method.methodKind === "unary" && /^(Create|Update|Delete)/.test(method.name) &&
      method.proto.options?.idempotencyLevel !== MethodOptions_IdempotencyLevel.NO_SIDE_EFFECTS,
    );
    expect(mutations.length).toBeGreaterThan(0);
    for (const method of mutations) {
      await expect(transport.unary(method as DescMethodUnary, undefined, undefined, undefined, create(method.input), undefined)).rejects.toThrow(SHOP_READ_ONLY_MESSAGE);
    }
    expect(unary).not.toHaveBeenCalled();
    expect(stream).not.toHaveBeenCalled();
  });

  it("tells the fixture chat's story: the transcript without the client-only failed send, the stream with it", async () => {
    const client = createClient(ChatService, appTransport());
    const { messages } = await client.listMessages({ chatId: CHAT_IDS.cartTotals });
    expect(messages.some((m) => m.id.startsWith("optimistic-"))).toBe(false);
    const snapshot = chatStreamEvents(CHAT_IDS.cartTotals)[0].event;
    expect(snapshot.case === "snapshot" && snapshot.value.messages.some((m) => m.id.startsWith("optimistic-"))).toBe(true);
    expect(snapshot.case === "snapshot" && snapshot.value.otherUpdates).toHaveLength(2);
    const sub = await client.listMessages({ chatId: CHAT_IDS.cartTotals, threadId: "th-review-1" });
    expect(sub.messages.map((m) => m.thread)).toEqual(["th-review-1"]);
  });

  it("lists in the Inbox what the story already shows: the chat's approval and questions, and the failing automation", async () => {
    const transport = appTransport();
    const inbox = await createClient(InboxService, transport).listInbox({});
    expect(inbox.items.map((i) => i.kind)).toEqual([InboxItemKind.APPROVAL, InboxItemKind.QUESTION, InboxItemKind.QUESTION, InboxItemKind.AUTOMATION_FAILING]);
    expect([inbox.blockingCount, inbox.hasInformational]).toEqual([2, true]);
    // The badge's counts-only read agrees, and docs has nothing waiting.
    const counts = await createClient(InboxService, transport).listInbox({ limit: 0 });
    expect([counts.items.length, counts.blockingCount]).toEqual([0, 2]);
    expect(await createClient(InboxService, transport).listInbox({ projectId: PROJECT_IDS.docs })).toMatchObject({ items: [], blockingCount: 0, otherProjectsCount: 4 });

    const [approval, asyncQuestion, blockingQuestion, automation] = inbox.items;
    const { approvals } = await createClient(ApprovalService, transport).listApprovalsByChat({ chatId: CHAT_IDS.cartTotals });
    expect(approval.payload.case === "approval" && approval.payload.value.approvalId).toBe(approvals[0].id);
    const asked = await createClient(QuestionService, transport).listAsyncQuestions({ chatId: CHAT_IDS.cartTotals });
    expect(asyncQuestion.payload.case === "question" && asyncQuestion.payload.value.questionId).toBe(asked.questions[0].questionId);
    const pending = await createClient(QuestionService, transport).listPendingQuestions({ chatId: CHAT_IDS.rateLimit });
    expect(blockingQuestion.payload.case === "question" && blockingQuestion.payload.value.questionId).toBe(pending.questions[0].questionId);
    const { triggers } = await createClient(TriggerService, transport).listTriggers({});
    const nightly = triggers.find((t) => t.id === automation.triggerId)!;
    expect(automation.payload.case === "automationFailing" && automation.payload.value.health).toEqual(nightly.health);
  });

  it("answers the bearer-link reads with obviously fake links", async () => {
    const billing = createClient(BillingService, appTransport());
    const { invoices } = await billing.listCurrentUserInvoices({});
    expect(invoices.length).toBeGreaterThan(0);
    for (const invoice of invoices) {
      for (const url of [invoice.hostedInvoiceUrl, invoice.pdfUrl]) expect(new URL(url).hostname.endsWith(".invalid"), url).toBe(true);
    }
    const { subscription } = await billing.getCurrentUserComputeSubscription({});
    for (const url of JSON.stringify(subscription ?? {}, (_k, v: unknown) => (typeof v === "bigint" ? String(v) : v)).match(/https?:\/\/[^"]+/g) ?? []) expect(new URL(url).hostname.endsWith(".invalid"), url).toBe(true);
  });

  it("answers MCP ListServers with obviously fake config", async () => {
    const { servers } = await createClient(MCPService, appTransport()).listServers({ projectId: PROJECT_IDS.shop });
    expect(servers.length).toBeGreaterThan(0);
    for (const server of servers) {
      if (server.config?.url) expect(new URL(server.config.url).hostname.endsWith(".invalid"), server.config.url).toBe(true);
      for (const value of Object.values(server.config?.headers ?? {})) expect(value).toContain(FAKE_MCP_HEADER);
      for (const entry of server.config?.env ?? []) expect(entry).toContain(FAKE_MCP_HEADER);
    }
  });

  it("names its read-only exceptions by real rpcs it answers", () => {
    const answered = appLayerScenarioHandlers();
    for (const key of APP_LAYER_ANSWERS) {
      expect(METHODS.has(key), key).toBe(true);
      expect(answered[key], key).toBeDefined();
    }
  });
});

describe("the app layer's streams", () => {
  it("end when the call is aborted, and not before", async () => {
    const controller = new AbortController();
    const stream = streamingImpl.streamUserUpdates(create(StreamingService.methods[0].input as never), { signal: controller.signal });
    expect((await stream.next()).done).toBe(false);
    const after = stream.next();
    expect(await Promise.race([after, pendingFor(20)])).toBe("still open");
    controller.abort(new Error("left the page"));
    await expect(after).rejects.toThrow("left the page");
  });

  it("stay open, then close on abort, as Connect routes (the routes path)", async () => {
    const transport = createRouterTransport((router) => router.service(StreamingService, streamingImpl));
    const controller = new AbortController();
    const frames: string[] = [];
    const read = (async () => {
      for await (const event of createClient(StreamingService, transport).streamChatUpdates({ chatId: CHAT_IDS.cartTotals }, { signal: controller.signal })) frames.push(event.event.case ?? "");
    })();
    await pendingFor(30);
    expect(frames).toEqual(["snapshot", "caughtUp"]);
    controller.abort();
    await expect(read).rejects.toThrow();
  });
});

describe("the app layer's data/*.json", () => {
  const FILES: [string, DescMessage, string][] = [
    ["catalog.json", ListProjectsWithAppsResponseSchema, catalog],
    ["attachments/checkout-total.json", GetAttachmentResponseSchema, checkoutTotal],
    ["transcripts/cart-totals.json", ListMessagesResponseSchema, cartTotals],
    ["transcripts/tax-rates.json", ListMessagesResponseSchema, taxRates],
    ["transcripts/stripe-sdk.json", ListMessagesResponseSchema, stripeSdk],
    ["transcripts/load-test.json", ListMessagesResponseSchema, loadTest],
    ["transcripts/sub-agents.json", ListMessagesResponseSchema, subAgents],
  ];

  it.each(FILES)("%s loads strictly", (_name, schema, text) => {
    expectWireSafe(schema, decodeFixtureJson(schema, text));
    // An unknown key is a load error, not a silently dropped field.
    expect(() => decodeFixtureJson(schema, { ...JSON.parse(text), notAField: true })).toThrow(/notAField/);
  });

  it("moves every instant onto the pack clock, and nothing under the harness's", () => {
    const at = (now: number) => packJson(ListMessagesResponseSchema, cartTotals, now).messages[0].createdAt;
    expect(at(DATA_ANCHOR)).toBe(JSON.parse(cartTotals).messages[0].createdAt);
    expect(Date.parse(at(DATA_ANCHOR + HOUR)) - Date.parse(at(DATA_ANCHOR))).toBe(HOUR);
    const shifted = packJson(GetAttachmentResponseSchema, checkoutTotal, DATA_ANCHOR + HOUR);
    expect(timestampMs(shifted.attachment!.createdAt!)).toBe(Date.parse(JSON.parse(checkoutTotal).attachment.createdAt) + HOUR);
    expect(DATA_ANCHOR).toBe(FIXED_NOW);
  });
});

describe("the packs' boundaries", () => {
  const SRC = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");
  const APP_LAYER = [/\/fixtures\/shop\/app\.ts$/, /\/fixtures\/shop\/workspace\.ts$/, /\/fixtures\/shop\/handlers\/reliant\//, /\/fixtures\/shop\/handlers\/(billing|account|serve)\.ts$/];
  /** Fixture mode's install (the app bundle's), the fake Supabase it serves, and sign-in code. */
  const NEVER_IN_THE_DEMO = [
    /\/fixtures\/install\.ts$/,
    /\/fixtures\/outside\.ts$/,
    /\/fixtures\/previewBoot\.ts$/,
    /\/lib\/fixturesBoot\.ts$/,
    /\/lib\/supabase\.ts$/,
    // lib/auth/ but its demo twins and apiKeyUser.ts, as the guard allows (vite-plugin-demo-bundle.ts).
    /\/lib\/auth\/(?!apiKeyUser\.ts$)(?!.*\.demo\.tsx?$)/,
    /\/store\/authStore\.ts$/,
    /\/api\/authProvider\.ts$/,
  ];

  const exists = (p: string) => {
    try {
      return readFileSync(p) && true;
    } catch {
      return false;
    }
  };

  /**
   * Every module under src/ that `entry` reaches by static or dynamic import.
   * `demo`: as the demo build resolves them, where a module `x.ts` with a
   * sibling `x.demo.ts(x)` is that twin (vite-plugin-demo-bundle.ts demoSubstitutes).
   */
  function reach(entry: string, { demo = false } = {}): Set<string> {
    const seen = new Set<string>();
    const twinOf = (file: string) => {
      if (!demo || /\.demo\.tsx?$/.test(file)) return file;
      return [file.replace(/\.tsx?$/, ".demo.ts"), file.replace(/\.tsx?$/, ".demo.tsx")].find(exists) ?? file;
    };
    const visit = (file: string) => {
      if (seen.has(file)) return;
      seen.add(file);
      const text = readFileSync(file, "utf8");
      for (const [, spec] of text.matchAll(/(?:from|import)\s*\(?\s*["']([^"']+)["']/g)) {
        if (!spec.startsWith(".") && !spec.startsWith("@/")) continue;
        const base = spec.startsWith("@/") ? join(SRC, spec.slice(2)) : resolve(dirname(file), spec);
        const target = [base, `${base}.ts`, `${base}.tsx`, join(base, "index.ts"), join(base, "index.tsx")].find(exists);
        if (target && /\.tsx?$/.test(target)) visit(twinOf(target));
      }
    };
    visit(entry);
    return seen;
  }

  it("the console's pack (index.ts, the forge tour's sample in the app) reaches nothing of the app layer", () => {
    const reached = [...reach(join(SRC, "fixtures/shop/index.ts"))];
    expect(reached.length).toBeGreaterThan(10);
    expect(reached.filter((f) => APP_LAYER.some((re) => re.test(f)))).toEqual([]);
  });

  it("the public demo's transport serves the app layer, and reaches nothing of fixture mode, its fake Supabase or sign-in", () => {
    const reached = [...reach(join(SRC, "components/Forge/Demo/demoTransport.ts"), { demo: true })];
    expect(reached.some((f) => /\/fixtures\/shop\/app\.ts$/.test(f))).toBe(true);
    expect(reached.filter((f) => NEVER_IN_THE_DEMO.some((re) => re.test(f)))).toEqual([]);
  });
});
