// @vitest-environment node
import { describe, expect, it } from "vitest";
import { Code, ConnectError, createRouterTransport, type Interceptor } from "@connectrpc/connect";
import { ChatService } from "#gen/reliant/v1/chat_pb.js";
import { StreamingService } from "#gen/reliant/v1/streaming_pb.js";
import type { AuthTokenProvider } from "./auth.js";
import { createReliantClient } from "./client.js";
import { FAILED_SEND_PREFIX } from "./send-ids.js";
import { resolveTransport, sdkInterceptors, serviceClient, type ReliantTransportOptions } from "./transport.js";
import { SDK_VERSION } from "./version.js";

// The SDK's interceptor chain on its normal path: `baseUrl` + `createTransport`,
// so resolveTransport composes normalise -> auth -> trace before the host's own
// stages. The wire is Connect's in-memory router, which runs that chain exactly
// as a real transport would and lets the handler read the request headers.
// (ChatSession's other tests pass `transport` directly, which skips the chain.)

const CHAT = "chat-1";

interface Wire {
  /** The Authorization header of each request that reached the server. */
  auth: Array<string | null>;
}

function client(
  options: Omit<ReliantTransportOptions, "baseUrl" | "createTransport" | "transport">,
  answer: () => Promise<{ messageId: string }> = async () => ({ messageId: "m-1" }),
) {
  const wire: Wire = { auth: [] };
  const createTransport: ReliantTransportOptions["createTransport"] = ({ interceptors }) =>
    createRouterTransport(
      (router) =>
        router.rpc(ChatService.method.sendMessage, async (_req, ctx) => {
          wire.auth.push(ctx.requestHeader.get("authorization"));
          return answer();
        }),
      { transport: { interceptors } },
    );
  const sdk = createReliantClient({ ...options, baseUrl: "https://api.example.test", createTransport });
  const session = sdk.chat({ chatId: CHAT, sleep: async () => {} });
  return { wire, session };
}

function provider(getToken: () => Promise<string | null>): AuthTokenProvider {
  return { getToken, hasSession: async () => true, refresh: async () => null };
}

describe("the SDK transport chain", () => {
  it("attaches the provider's current bearer token to every call", async () => {
    let token = "tok-1";
    const { wire, session } = client({ auth: provider(async () => token) });

    await session.send("one");
    token = "tok-2";
    await session.send("two");

    expect(wire.auth).toEqual(["Bearer tok-1", "Bearer tok-2"]);
  });

  it("sends no credential when there is no provider, or the provider has no token", async () => {
    const none = client({});
    await none.session.send("hi");
    expect(none.wire.auth).toEqual([null]);

    const signedOut = client({ auth: provider(async () => null) });
    await signedOut.session.send("hi");
    expect(signedOut.wire.auth).toEqual([null]);
  });

  it("makes no wire call when the token provider fails", async () => {
    const { wire, session } = client({
      auth: provider(async () => {
        throw new Error("keychain locked");
      }),
    });

    const error = await session.send("hi", { retries: 2 }).catch((e: unknown) => e);

    expect(error).toBeInstanceOf(ConnectError);
    expect(wire.auth).toEqual([]);
  });

  it.each([Code.Unauthenticated, Code.PermissionDenied])(
    "keeps a denial (%s) through normalisation, and does not retry it",
    async (code) => {
      const { wire, session } = client({ auth: provider(async () => "tok") }, async () => {
        throw new ConnectError("denied", code);
      });

      const error = await session.send("hi", { retries: 3 }).catch((e: unknown) => e);

      expect(error).toBeInstanceOf(ConnectError);
      expect((error as ConnectError).code).toBe(code);
      expect(wire.auth).toEqual(["Bearer tok"]);
      expect(session.getState().messages.some((m) => m.id.startsWith(FAILED_SEND_PREFIX))).toBe(true);
    },
  );

  it("runs host interceptors after the SDK's, so they see the credential", async () => {
    const seen: Array<string | null> = [];
    const host: Interceptor = (next) => async (req) => {
      seen.push(req.header.get("authorization"));
      return next(req);
    };
    const { wire, session } = client({ auth: provider(async () => "tok"), interceptors: [host] });

    await session.send("hi");

    expect(seen).toEqual(["Bearer tok"]);
    expect(wire.auth).toEqual(["Bearer tok"]);
  });

  it("uses a supplied transport as is, without the chain", () => {
    const transport = createRouterTransport(() => {});
    expect(resolveTransport({ transport, baseUrl: "https://ignored.test" })).toBe(transport);
    expect(() => resolveTransport({ baseUrl: "https://x.test" })).toThrow(/createTransport/);
  });
});

// Every call on a transport the SDK builds names this SDK in Reliant-Client:
// the server refuses a reliant.v1 call that names no client once it enforces
// its minimum, so a missed path is a broken client. These pin each way a call
// leaves the SDK, and that a host's own transport keeps its own identity.
describe("Reliant-Client", () => {
  const ID = `sdk-ts/${SDK_VERSION}`;

  function wire(interceptors: Interceptor[] = []) {
    const seen: Array<{ procedure: string; client: string | null }> = [];
    const transport = createRouterTransport(
      (router) => {
        router.rpc(ChatService.method.sendMessage, async (_req, ctx) => {
          seen.push({ procedure: "sendMessage", client: ctx.requestHeader.get("reliant-client") });
          return { messageId: "m-1" };
        });
        router.rpc(StreamingService.method.streamChatUpdates, async function* (_req, ctx) {
          seen.push({ procedure: "streamChatUpdates", client: ctx.requestHeader.get("reliant-client") });
        });
      },
      { transport: { interceptors } },
    );
    return { seen, transport };
  }

  async function callBoth(sdk: ReturnType<typeof createReliantClient>) {
    await sdk.service(ChatService).sendMessage({ chatId: CHAT });
    for await (const _ of sdk.service(StreamingService).streamChatUpdates({ chatId: CHAT })) {
      // drain
    }
  }

  it("is sent on unary and streaming calls of a transport the SDK builds", async () => {
    let w = wire();
    const sdk = createReliantClient({
      baseUrl: "https://api.example.test",
      createTransport: ({ interceptors }) => {
        w = wire(interceptors);
        return w.transport;
      },
    });
    await callBoth(sdk);
    expect(w.seen).toEqual([
      { procedure: "sendMessage", client: ID },
      { procedure: "streamChatUpdates", client: ID },
    ]);
  });

  it("is sent on unary and streaming calls when createTransport drops the chain", async () => {
    const w = wire();
    const sdk = createReliantClient({ baseUrl: "https://api.example.test", createTransport: () => w.transport });
    await callBoth(sdk);
    expect(w.seen.map((s) => s.client)).toEqual([ID, ID]);
  });

  it("is sent through the client's exposed transport, and by a host chain built on sdkInterceptors()", async () => {
    let w = wire();
    const sdk = createReliantClient({
      baseUrl: "https://api.example.test",
      createTransport: ({ interceptors }) => {
        w = wire(interceptors);
        return w.transport;
      },
    });
    await serviceClient(sdk.transport, ChatService).sendMessage({ chatId: CHAT });
    const host = wire(sdkInterceptors());
    await serviceClient(host.transport, ChatService).sendMessage({ chatId: CHAT });
    expect([...w.seen, ...host.seen].map((s) => s.client)).toEqual([ID, ID]);
  });

  it("leaves a host's supplied transport, and its identity, to the host", async () => {
    const bare = wire();
    await callBoth(createReliantClient({ transport: bare.transport }));
    expect(bare.seen.map((s) => s.client)).toEqual([null, null]);

    const web: Interceptor = (next) => async (req) => {
      req.header.set("Reliant-Client", "web/1.9.2");
      return next(req);
    };
    const host = wire([web]);
    await callBoth(createReliantClient({ transport: host.transport }));
    expect(host.seen.map((s) => s.client)).toEqual(["web/1.9.2", "web/1.9.2"]);
  });

  it("yields to a host interceptor or a caller's own header on a transport the SDK builds", async () => {
    const cli: Interceptor = (next) => async (req) => {
      req.header.set("Reliant-Client", "cli/1.9.2");
      return next(req);
    };
    let w = wire();
    const sdk = createReliantClient({
      baseUrl: "https://api.example.test",
      interceptors: [cli],
      createTransport: ({ interceptors }) => {
        w = wire(interceptors);
        return w.transport;
      },
    });
    await callBoth(sdk);
    expect(w.seen.map((s) => s.client)).toEqual(["cli/1.9.2", "cli/1.9.2"]);

    let own = wire();
    const plain = createReliantClient({
      baseUrl: "https://api.example.test",
      createTransport: ({ interceptors }) => {
        own = wire(interceptors);
        return own.transport;
      },
    });
    await plain.service(ChatService).sendMessage({ chatId: CHAT }, { headers: { "Reliant-Client": "daemon/1.9.2" } });
    expect(own.seen.map((s) => s.client)).toEqual(["daemon/1.9.2"]);
  });
});
