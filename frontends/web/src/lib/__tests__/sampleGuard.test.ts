// Copyright (c) 2026 Reliant Labs

/**
 * The live transport's backstop (lib/sampleGuard.ts): a request that names
 * the forge tour's sample app, or carries one of its ids anywhere in an id
 * field, is refused PermissionDenied and never sent. The live routes run it
 * ahead of their interceptor chain (lib/connect.ts transportFor): the second
 * half of this file sends through the REAL control-plane transport and looks
 * at what reaches fetch.
 */

import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createClient } from "@connectrpc/connect";

import { ActivityService, ListActivityRequestSchema } from "@/gen/controlplane/services/activity/v1/activity_pb";
import { DeployService, GetDeployLiveViewRequestSchema } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { ChatService, SearchChatsRequestSchema } from "@/gen/reliant/v1/chat_pb";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.example.test", hasControlPlane: true }));
const logged = vi.hoisted(() => ({ warn: [] as unknown[][] }));
vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), error: vi.fn(), debug: vi.fn(), warn: (...args: unknown[]) => logged.warn.push(args) },
}));

import { carriesSample, SAMPLE_REFUSED_MESSAGE } from "../sampleGuard";

describe("carriesSample", () => {
  it("finds the sample app's name in any field, at any depth", () => {
    expect(carriesSample(GetDeployLiveViewRequestSchema, create(GetDeployLiveViewRequestSchema, { project: "sample_shop" }))).toBe(true);
    expect(carriesSample(ListActivityRequestSchema, create(ListActivityRequestSchema, { scope: { orgId: "org-1", projectId: "sample_shop" } }))).toBe(true);
    // A plain init object is read as the message it describes.
    expect(carriesSample(SearchChatsRequestSchema, { query: "sample_shop" })).toBe(true);
  });

  it("finds a sample id in an id field, repeated ones included", () => {
    expect(carriesSample(ListActivityRequestSchema, create(ListActivityRequestSchema, { scope: { orgId: "org-1", envIds: ["env-1", "sample_env-prod"] } }))).toBe(true);
    expect(carriesSample(ListActivityRequestSchema, create(ListActivityRequestSchema, { targetId: "sample_rel-v1.8.4" }))).toBe(true);
  });

  it("lets a real app's request, and free text that merely starts with the prefix, through", () => {
    expect(carriesSample(GetDeployLiveViewRequestSchema, create(GetDeployLiveViewRequestSchema, { project: "shop" }))).toBe(false);
    expect(carriesSample(ListActivityRequestSchema, create(ListActivityRequestSchema, { scope: { orgId: "org-1", projectId: "shopfront", envIds: ["env-prod"] } }))).toBe(false);
    expect(carriesSample(SearchChatsRequestSchema, create(SearchChatsRequestSchema, { query: "sample_rate is wrong" }))).toBe(false);
  });
});

describe("the live transport", () => {
  let fetches: string[];

  beforeAll(async () => {
    // The first import of connect.ts is cold; pay it under a hook budget (see connect.routing.test.ts).
    await import("../connect");
  }, 60_000);

  beforeEach(async () => {
    fetches = [];
    logged.warn.length = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        fetches.push(String(input));
        return new Response("{}", { status: 200, headers: { "content-type": "application/json" } });
      }),
    );
    const { setAuthTokenProvider } = await import("@/api/authProvider");
    setAuthTokenProvider({ getToken: async () => "test-token", hasSession: async () => true, refresh: async () => null });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  async function refusal(call: () => Promise<unknown>): Promise<ConnectError> {
    const error = await call().then(
      () => null,
      (e: unknown) => ConnectError.from(e),
    );
    expect(error, "the request was refused").not.toBeNull();
    return error!;
  }

  it("refuses a request that names the sample app, sends nothing, and logs it", async () => {
    const { transportFor } = await import("../connect");
    const deploy = createClient(DeployService, transportFor("control-plane"));
    const error = await refusal(() => deploy.getLiveView({ project: "sample_shop" }));
    expect(error.code).toBe(Code.PermissionDenied);
    expect(error.rawMessage).toBe(SAMPLE_REFUSED_MESSAGE);
    expect(fetches).toEqual([]);
    expect(logged.warn).toEqual([[expect.stringContaining("sample guard"), { procedure: "controlplane.v1.DeployService/GetLiveView" }]]);
  });

  it("refuses a request that carries a sample id on any live route", async () => {
    const { transportFor } = await import("../connect");
    const activity = createClient(ActivityService, transportFor("control-plane"));
    expect((await refusal(() => activity.listActivity({ scope: { orgId: "org-1", envIds: ["sample_env-prod"] } }))).code).toBe(Code.PermissionDenied);
    const chats = createClient(ChatService, transportFor("reliant-api"));
    expect((await refusal(() => chats.searchChats({ projectId: "sample_p-1", query: "" }))).code).toBe(Code.PermissionDenied);
    expect(fetches).toEqual([]);
  });

  it("sends a real app's request as before", async () => {
    const { transportFor } = await import("../connect");
    const deploy = createClient(DeployService, transportFor("control-plane"));
    await deploy.getLiveView({ project: "shop" });
    expect(fetches).toEqual([expect.stringContaining("/controlplane.v1.DeployService/GetLiveView")]);
  });
});
