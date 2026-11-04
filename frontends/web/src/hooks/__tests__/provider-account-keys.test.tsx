/**
 * ProviderAccountService reads and writes share the generated key scope, so
 * neither the Settings list nor a chat's account chip is left stale by a
 * write:
 *
 *  - an account action refetches the mounted list (it is a generated
 *    mutation, invalidating providerAccountServiceKeys.all);
 *  - the imperative single-credential writes (api.settings.updateProvider /
 *    deleteProvider) invalidate the same scope;
 *  - providerAccountServiceKeys.all reaches the chat's account read too.
 */

import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";

import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { useAuthStore } from "@/store/authStore";
import { accountToProto } from "@/test/providerAccountTransport";
import type { ProviderAccount } from "@/types/providerAccount";
import {
  addProviderApiKey,
  disconnectProvider,
  useProviderAccountActions,
  useProviderAccounts,
} from "../provider-account-queries";
import { providerAccountServiceKeys } from "../provider-account-service-hooks_gen";
import { useChatAccountUsage } from "../useChatAccountUsage";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

const account = (id: string, label: string): ProviderAccount => ({
  id,
  provider: "claude",
  providerDisplayName: "Claude subscription",
  authKind: "oauth",
  label,
  priority: 0,
  status: "available",
  windows: [],
  usageSource: "headers",
  createdAt: "2026-10-01T00:00:00Z",
});

let server: ProviderAccount[];
let listCalls: number;
let chatCalls: number;

beforeEach(() => {
  server = [account("a1", "alice")];
  listCalls = 0;
  chatCalls = 0;
  useAuthStore.setState({ session: { access_token: "t" } as never });
  setTransport(
    mockTransport({
      "ProviderAccountService.ListProviderAccounts": () => {
        listCalls++;
        return { accounts: server.map(accountToProto) };
      },
      "ProviderAccountService.GetChatProviderAccount": () => {
        chatCalls++;
        return { account: accountToProto(server[0]) };
      },
      "ProviderAccountService.UpdateProviderAccount": (req) => {
        const r = req as { accountId: string; label?: string };
        server = server.map((a) => (a.id === r.accountId && r.label ? { ...a, label: r.label } : a));
        return { account: accountToProto(server.find((a) => a.id === r.accountId)!) };
      },
      "ProviderAccountService.AddProviderAPIKey": (req) => {
        const r = req as { provider: string };
        server = [...server, { ...account("a2", "key"), provider: r.provider, authKind: "api_key" }];
        return { created: true };
      },
      "ProviderAccountService.DeleteProviderAccount": (req) => {
        server = server.filter((a) => a.id !== (req as { accountId: string }).accountId);
        return {};
      },
    }),
  );
});

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
  useAuthStore.setState({ session: null });
});

const labels = (r: ReturnType<typeof useProviderAccounts>) =>
  r?.kind === "ready" ? r.accounts.map((a) => a.label) : undefined;

describe("ProviderAccountService keys", () => {
  it("renaming refetches the mounted list", async () => {
    const list = renderHook(() => useProviderAccounts(), { wrapper });
    await waitFor(() => expect(labels(list.result.current)).toEqual(["alice"]));
    expect(listCalls).toBe(1);

    const actions = renderHook(() => useProviderAccountActions(), { wrapper });
    const [alice] = list.result.current?.kind === "ready" ? list.result.current.accounts : [];
    await act(() => actions.result.current.rename(alice, "Personal"));

    await waitFor(() => expect(listCalls).toBe(2));
    expect(labels(list.result.current)).toEqual(["Personal"]);
  });

  it("an API key added from the single-credential view refetches the list", async () => {
    const list = renderHook(() => useProviderAccounts(), { wrapper });
    await waitFor(() => expect(listCalls).toBe(1));

    await act(() => addProviderApiKey("anthropic", "sk-ant"));
    await waitFor(() => expect(labels(list.result.current)).toEqual(["alice", "key"]));
  });

  it("disconnecting a provider refetches the list", async () => {
    const list = renderHook(() => useProviderAccounts(), { wrapper });
    await waitFor(() => expect(labels(list.result.current)).toEqual(["alice"]));

    await act(() => disconnectProvider("claude"));
    await waitFor(() => expect(labels(list.result.current)).toEqual([]));
  });

  it("providerAccountServiceKeys.all reaches the list and the chat's account", async () => {
    const usage = renderHook(() => useChatAccountUsage("chat-1"), { wrapper });
    await waitFor(() => expect(usage.result.current.active?.id).toBe("a1"));
    expect([listCalls, chatCalls]).toEqual([1, 1]);

    await queryClient.invalidateQueries({ queryKey: providerAccountServiceKeys.all });
    await waitFor(() => expect([listCalls, chatCalls]).toEqual([2, 2]));
  });
});
