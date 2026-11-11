// @vitest-environment node
import { describe, expect, it } from "vitest";
import { Code, ConnectError, createRouterTransport, type Interceptor } from "@connectrpc/connect";
import { ChatService } from "#gen/reliant/v1/chat_pb.js";
import type { AuthTokenProvider } from "./auth.js";
import { createReliantClient } from "./client.js";
import { FAILED_SEND_PREFIX } from "./send-ids.js";
import { resolveTransport, type ReliantTransportOptions } from "./transport.js";

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
