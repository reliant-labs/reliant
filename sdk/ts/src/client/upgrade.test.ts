// @vitest-environment node
import { describe, expect, it } from "vitest";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { BinaryWriter } from "@bufbuild/protobuf/wire";
import { ChatService } from "#gen/reliant/v1/chat_pb.js";
import { StreamingService } from "#gen/reliant/v1/streaming_pb.js";
import { createReliantClient } from "./client.js";
import { isUpgradeRequired, UpgradeRequiredError } from "./upgrade.js";

// The server's refusal of a client older than its minimum, as it arrives
// through the SDK's own chain (error normalisation included): the reason
// header, a google.rpc.ErrorInfo detail, or both.

const MESSAGE =
  "this sdk-ts (0.1.0) is older than the minimum this server supports (0.2.0); upgrade: npm install @reliantlabs/sdk@latest";
const NO_HEADER_MESSAGE = "this client sends no Reliant-Client version; upgrade to the latest version";
const FLOOR = {
  client_kind: "sdk-ts",
  client_version: "0.1.0",
  min_version: "0.2.0",
  upgrade_url: "https://reliantlabs.io/docs/sdk",
  upgrade_command: "npm install @reliantlabs/sdk@latest",
};

function errorInfo(reason: string, domain: string, metadata: Record<string, string>) {
  const w = new BinaryWriter();
  w.tag(1, 2).string(reason);
  w.tag(2, 2).string(domain);
  for (const [k, v] of Object.entries(metadata)) {
    w.tag(3, 2).fork().tag(1, 2).string(k).tag(2, 2).string(v).join();
  }
  return { type: "google.rpc.ErrorInfo", value: w.finish() };
}

interface Refusal {
  message?: string;
  header?: boolean;
  detail?: { reason: string; domain: string; metadata: Record<string, string> };
}

function refusal({ message = MESSAGE, header = false, detail }: Refusal): ConnectError {
  const e = new ConnectError(message, Code.FailedPrecondition, header ? { "x-forge-error-reason": "upgrade_required" } : undefined);
  if (detail) e.details.push(errorInfo(detail.reason, detail.domain, detail.metadata));
  return e;
}

function sdkRefusing(err: ConnectError) {
  return createReliantClient({
    baseUrl: "https://api.example.test",
    createTransport: ({ interceptors }) =>
      createRouterTransport(
        (router) => {
          router.rpc(ChatService.method.sendMessage, async () => {
            throw err;
          });
          // eslint-disable-next-line require-yield
          router.rpc(StreamingService.method.streamChatUpdates, async function* () {
            throw err;
          });
        },
        { transport: { interceptors } },
      ),
  });
}

async function unaryError(err: ConnectError): Promise<unknown> {
  return sdkRefusing(err)
    .service(ChatService)
    .sendMessage({ chatId: "c" })
    .then(() => undefined, (e: unknown) => e);
}

async function streamError(err: ConnectError): Promise<unknown> {
  try {
    for await (const _ of sdkRefusing(err).service(StreamingService).streamChatUpdates({ chatId: "c" })) {
      // drain
    }
  } catch (e) {
    return e;
  }
  return undefined;
}

const full = { header: true, detail: { reason: "UPGRADE_REQUIRED", domain: "reliantlabs.io", metadata: FLOOR } };

describe("UpgradeRequiredError", () => {
  it.each([
    ["header and detail", full, FLOOR],
    ["header only", { header: true }, {}],
    ["detail only", { detail: full.detail }, FLOOR],
  ] as const)("maps a refusal with the %s", async (_name, shape, floor) => {
    for (const thrown of [await unaryError(refusal(shape)), await streamError(refusal(shape))]) {
      expect(thrown).toBeInstanceOf(ConnectError);
      expect((thrown as ConnectError).code).toBe(Code.FailedPrecondition);
      expect(isUpgradeRequired(thrown)).toBe(true);
      const u = UpgradeRequiredError.from(thrown)!;
      expect(u).toBeInstanceOf(UpgradeRequiredError);
      expect(u).toBeInstanceOf(Error);
      expect(u.message).toBe(MESSAGE);
      expect(u.cause).toBe(thrown);
      expect({
        client_kind: u.clientKind,
        client_version: u.clientVersion,
        min_version: u.minVersion,
        upgrade_url: u.upgradeUrl,
        upgrade_command: u.upgradeCommand,
      }).toEqual({ client_kind: "", client_version: "", min_version: "", upgrade_url: "", upgrade_command: "", ...floor });
      expect(UpgradeRequiredError.from(u)).toBe(u);
      expect(isUpgradeRequired(u)).toBe(true);
    }
  });

  it("maps the refusal of a client that sent no version: no floor, still an upgrade", async () => {
    const shape = {
      message: NO_HEADER_MESSAGE,
      header: true,
      detail: { reason: "UPGRADE_REQUIRED", domain: "reliantlabs.io", metadata: { client_kind: "", min_version: "" } },
    };
    const thrown = await unaryError(refusal(shape));
    const u = UpgradeRequiredError.from(thrown)!;
    expect(isUpgradeRequired(thrown)).toBe(true);
    expect(u.minVersion).toBe("");
    expect(u.message).toBe(NO_HEADER_MESSAGE);
  });

  it.each([
    ["another FailedPrecondition", new ConnectError("run is not paused", Code.FailedPrecondition)],
    ["another reason", new ConnectError("no", Code.FailedPrecondition, { "x-forge-error-reason": "approval_not_pending" })],
    ["another ErrorInfo", refusal({ detail: { reason: "QUOTA_EXCEEDED", domain: "reliantlabs.io", metadata: {} } })],
    ["another domain", refusal({ detail: { reason: "UPGRADE_REQUIRED", domain: "example.com", metadata: {} } })],
  ])("does not match %s", async (_name, err) => {
    const thrown = await unaryError(err);
    expect(isUpgradeRequired(thrown)).toBe(false);
    expect(UpgradeRequiredError.from(thrown)).toBeUndefined();
  });

  it("does not match what is not a ConnectError, and survives a garbled detail", () => {
    expect(isUpgradeRequired(new Error("upgrade_required"))).toBe(false);
    expect(isUpgradeRequired(undefined)).toBe(false);
    const garbled = new ConnectError("x", Code.FailedPrecondition);
    garbled.details.push({ type: "google.rpc.ErrorInfo", value: new Uint8Array([0x0a, 0x05, 0x61]) });
    expect(isUpgradeRequired(garbled)).toBe(false);
  });
});
