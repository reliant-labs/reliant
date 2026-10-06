import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Breadcrumb, ErrorEvent, TransactionEvent } from "@sentry/react";

// What the browser hands Sentry is pinned here through the real initSentry():
// the options it passes to Sentry.init are the privacy boundary, so the test
// drives them rather than a helper in isolation.

vi.mock("@sentry/react", () => ({
  init: vi.fn(),
  setUser: vi.fn(),
  browserTracingIntegration: vi.fn(() => ({ name: "BrowserTracing" })),
  replayIntegration: vi.fn((options: unknown) => ({ name: "Replay", options })),
  replayCanvasIntegration: vi.fn(() => ({ name: "ReplayCanvas" })),
}));
vi.mock("../constants", () => ({ isDev: false }));
vi.mock("../../store/privacyStore", () => ({
  getPrivacySettings: () => ({ crashReportingEnabled: true, analyticsEnabled: true }),
}));

import * as Sentry from "@sentry/react";
import { initSentry, setSentryUser } from "../sentry";

type InitOptions = {
  integrations: Array<{ name: string; options?: Record<string, unknown> }>;
  beforeBreadcrumb?: (b: Breadcrumb) => Breadcrumb | null;
  beforeSend: (e: ErrorEvent, hint: object) => ErrorEvent | null;
  beforeSendTransaction?: (e: TransactionEvent, hint: object) => TransactionEvent | null;
};

const PROMPT = "Please refactor the payment reconciliation module";
const TOOL_OUTPUT = "STRIPE_SECRET=sk_live_toolOutputLeak";
const FILE_LINE = "func chargeCustomer(amount int) error";
const TOKEN = "rlat_0123456789abcdefghij";
const OAUTH_CODE = "OAUTHCODE123";
const EMAIL = "founder@example.com";
const CHAT_ID = "3f2a8c1e-9b7d-4e2f-8a6b-1c2d3e4f5a6b";

const FORBIDDEN = [PROMPT, TOOL_OUTPUT, "sk_live_toolOutputLeak", FILE_LINE, TOKEN, OAUTH_CODE, EMAIL];

async function initOptions(): Promise<InitOptions> {
  await initSentry();
  const init = vi.mocked(Sentry.init);
  expect(init).toHaveBeenCalledTimes(1);
  return init.mock.calls[0][0] as unknown as InitOptions;
}

function expectNoUserContent(payload: unknown) {
  const wire = JSON.stringify(payload);
  for (const value of FORBIDDEN) {
    expect(wire).not.toContain(value);
  }
}

describe("Sentry privacy boundary (web)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.stubEnv("VITE_SENTRY_ENABLED", "true");
    vi.stubEnv("VITE_SENTRY_DSN", "https://public@o0.ingest.sentry.io/0");
    vi.stubEnv("VITE_SENTRY_REPLAY_CANVAS", "true");
  });
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("session replay masks all text and inputs and blocks media", async () => {
    const { integrations } = await initOptions();
    const replay = integrations.find((i) => i.name === "Replay");
    expect(replay?.options).toMatchObject({
      maskAllText: true,
      maskAllInputs: true,
      blockAllMedia: true,
    });
  });

  it("replay recordings keep URL paths but not query strings", async () => {
    const { integrations } = await initOptions();
    const replay = integrations.find((i) => i.name === "Replay");
    const beforeAddRecordingEvent = replay?.options?.beforeAddRecordingEvent as (e: unknown) => unknown;
    expect(beforeAddRecordingEvent).toBeTypeOf("function");
    const recorded = beforeAddRecordingEvent({
      type: 5,
      timestamp: 1,
      data: { tag: "performanceSpan", payload: { op: "navigation.navigate", description: `https://app.reliantlabs.io/auth/callback?code=${OAUTH_CODE}` } },
    });
    expect(JSON.stringify(recorded)).not.toContain(OAUTH_CODE);
    expect(recorded).toMatchObject({ data: { payload: { description: "https://app.reliantlabs.io/auth/callback" } } });
  });

  it("never records canvases, which text masking cannot reach (the terminal draws to one)", async () => {
    const { integrations } = await initOptions();
    expect(integrations.map((i) => i.name)).not.toContain("ReplayCanvas");
  });

  it("console breadcrumbs keep level and category but drop the logged arguments", async () => {
    const { beforeBreadcrumb } = await initOptions();
    expect(beforeBreadcrumb).toBeTypeOf("function");
    const crumb = beforeBreadcrumb!({
      category: "console",
      level: "log",
      type: "debug",
      timestamp: 1700000000,
      message: `[ChatStore] sending ${PROMPT}`,
      data: { arguments: ["[ChatStore] sending", { content: PROMPT, output: TOOL_OUTPUT }], logger: "console" },
    });
    expect(crumb).toEqual({ category: "console", level: "log", type: "debug", timestamp: 1700000000 });
  });

  it("network and navigation breadcrumbs lose query strings", async () => {
    const { beforeBreadcrumb } = await initOptions();
    const fetchCrumb = beforeBreadcrumb!({
      category: "fetch",
      data: { method: "POST", url: `https://api.reliantapi.com/auth/callback?code=${OAUTH_CODE}`, status_code: 200, request_body_size: 512 },
    });
    expect(fetchCrumb?.data).toEqual({
      method: "POST",
      url: "https://api.reliantapi.com/auth/callback",
      status_code: 200,
      request_body_size: 512,
    });
    const nav = beforeBreadcrumb!({ category: "navigation", data: { from: `/auth?code=${OAUTH_CODE}`, to: `/chat/${CHAT_ID}` } });
    expect(nav?.data).toEqual({ from: "/auth", to: `/chat/${CHAT_ID}` });
  });

  it("error events keep type, stack and identifiers but no user content", async () => {
    const { beforeSend } = await initOptions();
    const event: ErrorEvent = {
      type: undefined,
      message: `rpc failed\n${TOOL_OUTPUT}`,
      exception: {
        values: [{
          type: "ConnectError",
          value: `[internal] executing tool bash: exit status 1: ${TOOL_OUTPUT}\n${FILE_LINE}`,
          stacktrace: { frames: [{ function: "sendMessage", filename: "app:///assets/index.js", lineno: 10, vars: { body: PROMPT } }] },
          mechanism: { type: "onunhandledrejection", handled: false, data: { reason: PROMPT } },
        }],
      },
      extra: { chatId: CHAT_ID, durationMs: 1200, error: `boom\n${TOOL_OUTPUT}`, content: PROMPT, serverName: FILE_LINE },
      tags: { grpc_service: "reliant.v1.ChatService", grpc_method: "SendMessage", prompt_preview: "refactor" },
      contexts: {
        browser: { name: "Chrome", version: "130" },
        funnel: { step: "connect_provider", note: PROMPT },
      },
      request: {
        url: `https://app.reliantlabs.io/auth/callback?code=${OAUTH_CODE}#access_token=${TOKEN}`,
        headers: { "User-Agent": "Mozilla/5.0", Authorization: `Bearer ${TOKEN}`, Referer: `https://x/?code=${OAUTH_CODE}` },
        cookies: { sb: TOKEN },
        data: { content: PROMPT },
        query_string: `code=${OAUTH_CODE}`,
      },
      breadcrumbs: [{ category: "console", level: "info", message: PROMPT, data: { arguments: [PROMPT] } }],
      user: { id: "user-8d7f", email: EMAIL, username: "alice", ip_address: "203.0.113.9" },
    };

    const sent = beforeSend(event, {});
    expect(sent).not.toBeNull();
    expectNoUserContent(sent);

    const ex = sent!.exception!.values![0];
    expect(ex.type).toBe("ConnectError");
    expect(ex.value).toMatch(/^\[internal\] executing tool bash: exit status 1: /);
    expect(ex.stacktrace!.frames![0]).toMatchObject({ function: "sendMessage", lineno: 10 });
    expect(ex.mechanism).toMatchObject({ type: "onunhandledrejection", handled: false });
    expect(sent!.extra).toMatchObject({ chatId: CHAT_ID, durationMs: 1200 });
    expect(sent!.tags).toMatchObject({ grpc_service: "reliant.v1.ChatService", grpc_method: "SendMessage" });
    expect(sent!.tags).not.toHaveProperty("prompt_preview");
    expect(sent!.contexts!.browser).toEqual({ name: "Chrome", version: "130" });
    expect(sent!.contexts!.funnel).toEqual({ step: "connect_provider" });
    expect(sent!.request).toEqual({
      url: "https://app.reliantlabs.io/auth/callback",
      headers: { "User-Agent": "Mozilla/5.0" },
    });
    expect(sent!.user).toEqual({ id: "user-8d7f" });
  });

  it("transactions are scrubbed too", async () => {
    const { beforeSendTransaction } = await initOptions();
    expect(beforeSendTransaction).toBeTypeOf("function");
    const txn = {
      type: "transaction",
      transaction: `/chat/${CHAT_ID}`,
      request: { url: `https://app.reliantlabs.io/chat?code=${OAUTH_CODE}` },
      spans: [{
        span_id: "a", trace_id: "b", start_timestamp: 1, data: { "http.query": `?code=${OAUTH_CODE}`, "http.method": "POST", "sentry.op": "http.client" },
        description: `POST https://api.reliantapi.com/x?token=${TOKEN}`,
      }],
    } as unknown as TransactionEvent;
    const sent = beforeSendTransaction!(txn, {});
    expectNoUserContent(sent);
    expect(sent!.spans![0].description).toBe("POST https://api.reliantapi.com/x");
    expect(sent!.spans![0].data).toEqual({ "http.method": "POST", "sentry.op": "http.client" });
  });

  it("identifies users by id only", () => {
    setSentryUser({ id: "user-8d7f", email: EMAIL });
    expect(Sentry.setUser).toHaveBeenCalledWith({ id: "user-8d7f" });
  });
});
