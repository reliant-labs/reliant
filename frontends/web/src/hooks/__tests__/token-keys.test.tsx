/**
 * Daemon tokens are a generated TokenService read (hooks/token-queries). The
 * page used to fetch them into component state and re-fetch by hand after each
 * write; now each generated write invalidates `tokenServiceKeys.all`, so a
 * mounted list refetches by itself. These pin that create and revoke do.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { TokenKind } from "@/gen/reliant/v1/token_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";
import { useDaemonTokens } from "../token-queries";
import { useCreateToken, useRevokeToken } from "../token-service-hooks_gen";

const listTokens = vi.fn();
const createToken = vi.fn(async () => ({ token: "rlat_new", info: { id: "tok-2" } }));
const revokeToken = vi.fn(async () => ({}));

let client: QueryClient;
function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  listTokens.mockReset().mockResolvedValue({ tokens: [{ id: "tok-1", name: "build-box", kind: TokenKind.DAEMON }] });
  createToken.mockClear();
  revokeToken.mockClear();
  setTransport(mockTransport(serviceHandlers("TokenService", { listTokens, createToken, revokeToken })));
});

afterEach(() => {
  setTransport(null);
  client.clear();
});

function mountListAndWrites() {
  return renderHook(
    () => ({ list: useDaemonTokens(), create: useCreateToken(), revoke: useRevokeToken() }),
    { wrapper },
  );
}

describe("daemon token keys", () => {
  it("lists DAEMON tokens only, mapped to TokenInfo", async () => {
    const { result } = mountListAndWrites();
    await waitFor(() => expect(result.current.list.data?.map((t) => t.id)).toEqual(["tok-1"]));
    expect(listTokens).toHaveBeenCalledTimes(1);
    expect(listTokens.mock.calls[0][0]).toMatchObject({ kind: TokenKind.DAEMON });
  });

  it("a create refetches the mounted list", async () => {
    const { result } = mountListAndWrites();
    await waitFor(() => expect(result.current.list.isSuccess).toBe(true));

    listTokens.mockResolvedValue({
      tokens: [
        { id: "tok-1", name: "build-box", kind: TokenKind.DAEMON },
        { id: "tok-2", name: "laptop", kind: TokenKind.DAEMON },
      ],
    });
    await act(async () => {
      await result.current.create.mutateAsync({ name: "laptop", kind: TokenKind.DAEMON });
    });

    await waitFor(() => expect(result.current.list.data?.map((t) => t.id)).toEqual(["tok-1", "tok-2"]));
    expect(listTokens).toHaveBeenCalledTimes(2);
  });

  it("a revoke refetches the mounted list", async () => {
    const { result } = mountListAndWrites();
    await waitFor(() => expect(result.current.list.isSuccess).toBe(true));

    listTokens.mockResolvedValue({ tokens: [] });
    await act(async () => {
      await result.current.revoke.mutateAsync({ id: "tok-1" });
    });

    await waitFor(() => expect(result.current.list.data).toEqual([]));
    expect(revokeToken).toHaveBeenCalledTimes(1);
    expect(listTokens).toHaveBeenCalledTimes(2);
  });
});
