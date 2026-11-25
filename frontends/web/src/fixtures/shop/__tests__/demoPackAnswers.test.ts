// Copyright (c) 2025 Reliant Labs

/**
 * DEMO_PACK_ANSWERS (../index.ts): the reads the public demo's pack answers
 * although their protos do not declare NO_SIDE_EFFECTS. Owner-approved
 * (2026-11-24) on four conditions, which these tests hold:
 *
 *   1. it is an allow-list of control-plane#3523's seven data-browser reads,
 *      each with a reason (unwiring it from shopPack, one line, keeps every
 *      test here green);
 *   2. no machine-waking read is ever in it: the FileSystemService reads and
 *      the WorktreeService git reads, each resolved by exact name from the
 *      generated descriptors, so a renamed rpc fails here instead of slipping
 *      past;
 *   3. PreviewObject's fixture never returns anything shaped like a real
 *      presigned GCS/S3 URL: same-origin, or a `.invalid` host;
 *   4. no MCP read is in it (reliant#3466 keeps those refused).
 */
import { create, type DescMethod, type DescService } from "@bufbuild/protobuf";
import { Code, ConnectError, createClient, type Transport } from "@connectrpc/connect";
import { afterEach, describe, expect, it } from "vitest";

import { AppDatabaseService } from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { StorageBrowserService } from "@/gen/controlplane/services/storage_browser/v1/storage_browser_pb";
import { FileSystemService } from "@/gen/reliant/v1/filesystem_pb";
import { MCPService } from "@/gen/reliant/v1/mcp_pb";
import { WorktreeService } from "@/gen/reliant/v1/worktree_pb";

import { createFixtureTransport, fixturePackAnswers } from "../../transport";
import { SAMPLE_IMAGE_URL, storageScenarioHandlers } from "../handlers/storage";
import { createShopTransport, DEMO_PACK_ANSWERS, SHOP_READ_ONLY_MESSAGE, shopPack, shopScenario } from "../index";
import { demoNow, resetDemoNowForTests } from "../shopStory";
import { isDeclaredRead } from "./reads";

const keyOf = (method: DescMethod) => `${method.parent.typeName}/${method.name}`;

/** Resolve `names` against `service`'s generated descriptor; a name it does not declare fails the test. */
function methods(service: DescService, names: readonly string[]): DescMethod[] {
  return names.map((name) => {
    const method = service.methods.find((m) => m.name === name);
    if (!method) throw new Error(`${service.typeName} declares no rpc ${name}: update this list`);
    return method;
  });
}

/** control-plane#3523's seven: their only side effect is a server-side audit row, or a minted URL. */
const SEVEN = [
  ...methods(StorageBrowserService, ["PreviewObject", "ListObjects", "GetObjectMetadata"]),
  ...methods(AppDatabaseService, ["BrowseTable", "DescribeDatabase", "QueryDatabase", "QueryDatabasePanel"]),
];
const APPROVED = new Set(SEVEN.map(keyOf));

/**
 * The machine-waking reads (the read-only audit's "wakes" rows): a request
 * that finds the machine asleep wakes it. The demo never answers one.
 */
const MACHINE_WAKING = [
  ...methods(FileSystemService, ["GetFileTree", "GetFileContent", "GetFileMetadata", "GetFilePreviewInfo", "GetFilePreview", "SearchFiles", "ListDirectory"]),
  ...methods(WorktreeService, ["GetWorktreeChanges", "GetWorktreeGitStatus", "GetWorktreeCommits", "ListWorktreeRepoStatuses", "GetWorktreePR"]),
].map(keyOf);

const answered = DEMO_PACK_ANSWERS.map((answer) => keyOf(answer.method));

afterEach(() => resetDemoNowForTests());

describe("DEMO_PACK_ANSWERS", () => {
  it("lists only control-plane#3523's seven data-browser reads, each once", () => {
    for (const key of answered) expect(APPROVED.has(key), `${key} is not one of the seven approved reads`).toBe(true);
    expect(new Set(answered).size).toBe(answered.length);
  });

  it("gives every entry a reason", () => {
    for (const answer of DEMO_PACK_ANSWERS) expect(answer.reason.trim().length, keyOf(answer.method)).toBeGreaterThan(20);
  });

  it("never answers a machine-waking read, nor anything of FileSystemService or MCPService", () => {
    expect(MACHINE_WAKING).toHaveLength(12);
    for (const key of MACHINE_WAKING) expect(answered, key).not.toContain(key);
    for (const answer of DEMO_PACK_ANSWERS) {
      expect(answer.method.parent.typeName, keyOf(answer.method)).not.toBe(FileSystemService.typeName);
      expect(answer.method.parent.typeName, keyOf(answer.method)).not.toBe(MCPService.typeName);
    }
  });

  it("is all the demo pack answers beyond NO_SIDE_EFFECTS, each from the story, not a default", () => {
    const packAnswers = fixturePackAnswers(shopPack());
    for (const key of packAnswers) expect(answered, `the demo pack answers ${key}, which DEMO_PACK_ANSWERS does not list`).toContain(key);
    const handlers = shopScenario().handlers as Record<string, unknown>;
    for (const key of answered) expect(handlers[key], key).toBeDefined();
  });
});

describe("PreviewObject's fixture", () => {
  const PROD = { environmentId: "env-prod", bucket: "uploads", confirmEnvId: "env-prod", reason: "demo test" };
  const PRESIGNED = /storage\.googleapis\.com|amazonaws\.com|X-Goog-|X-Amz-|Signature=|GoogleAccessId/i;
  const KEY = `${StorageBrowserService.typeName}/`;

  it("returns an obviously fake image URL: same-origin, never a presigned GCS/S3 link", async () => {
    expect(SAMPLE_IMAGE_URL.startsWith("/") && !SAMPLE_IMAGE_URL.startsWith("//")).toBe(true);
    // The handlers themselves, past any read-only guard: what the pack would answer.
    const handlers = storageScenarioHandlers() as Record<string, (req: unknown) => unknown>;
    const list = (await handlers[`${KEY}ListObjects`](create(StorageBrowserService.method.listObjects.input, PROD))) as { objects: { key: string }[] };
    let images = 0;
    for (const { key } of list.objects) {
      const preview = (await handlers[`${KEY}PreviewObject`](create(StorageBrowserService.method.previewObject.input, { ...PROD, key }))) as { body: { case?: string; value?: { url?: string } } };
      if (preview.body.case !== "image") continue;
      images++;
      const url = preview.body.value?.url ?? "";
      const parsed = new URL(url, "https://demo.example");
      const sameOrigin = parsed.origin === "https://demo.example";
      expect(sameOrigin || parsed.hostname.endsWith(".invalid"), `${key}: ${url} is neither same-origin nor .invalid`).toBe(true);
      expect(url, key).not.toMatch(PRESIGNED);
    }
    expect(images, "the sample bucket has an image to preview").toBeGreaterThan(0);
  });
});

describe("the demo transport, read-only from the proto", () => {
  const code = (p: Promise<unknown>) => p.then((): Code | undefined => undefined, (e: unknown) => ConnectError.from(e).code);
  const ask = (transport: Transport, method: DescMethod) =>
    transport.unary(method as never, undefined, undefined, undefined, create(method.input) as never, undefined);

  it("answers each of the seven exactly when its proto declares it a read or the pack lists it", async () => {
    const transport = createShopTransport();
    const listed = new Set(fixturePackAnswers(shopPack()));
    for (const method of SEVEN) {
      // A handler may refuse the empty request as a server would (NotFound, ...); only the guard says PermissionDenied.
      const refused = (await code(ask(transport, method))) === Code.PermissionDenied;
      expect(refused, keyOf(method)).toBe(!(isDeclaredRead(method) || listed.has(keyOf(method))));
    }
  });

  it("refuses the machine-waking reads", async () => {
    const transport = createFixtureTransport({ pack: shopPack(), now: demoNow, readOnly: true, readOnlyMessage: SHOP_READ_ONLY_MESSAGE });
    await expect(createClient(FileSystemService, transport).getFileTree({})).rejects.toThrow(SHOP_READ_ONLY_MESSAGE);
    await expect(createClient(WorktreeService, transport).getWorktreeGitStatus({})).rejects.toThrow(SHOP_READ_ONLY_MESSAGE);
  });

  it("refuses the MCP reads reliant#3466 unmarked (the demo pack never answers one)", async () => {
    const mcp = createClient(MCPService, createShopTransport());
    await expect(mcp.listServers({})).rejects.toThrow(SHOP_READ_ONLY_MESSAGE);
    await expect(mcp.getServer({})).rejects.toThrow(SHOP_READ_ONLY_MESSAGE);
  });
});
