// Copyright (c) 2025 Reliant Labs
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import type { Transport } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { DemoBrowserProviders } from "../DemoBrowserProviders";
import { useReaderCacheCleanup } from "../../AuditedAccess/useReaderCacheCleanup";
import { DataBrowser } from "../../Data/DataBrowser";
import { StorageBrowser } from "../../Storage/StorageBrowser";
import { createShopTransport } from "@/fixtures/shop";
import { installShellTransport } from "@/lib/connect";
import { resetShellForTests } from "@/lib/shell";
import { getReaderSession, setReaderPrincipal } from "../../AuditedAccess/readerSession";

// The demo host: the browsers' real clients over the shell transport, the shop pack's (DemoBrowserProviders).
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://demo.invalid", hasControlPlane: true }));
vi.mock("@tanstack/react-router", async (original) => ({ ...await original<typeof import("@tanstack/react-router")>(), useNavigate: () => vi.fn() }));
beforeEach(() => {
  resetShellForTests("demo");
  setReaderPrincipal("production-a", true);
});
afterEach(() => {
  installShellTransport(null);
  resetShellForTests();
});

/** The shop transport, holding `method`'s calls until the test releases them. */
function deferring(method: string, defer: (signal?: AbortSignal) => Promise<void>): Transport {
  const shop = createShopTransport();
  return {
    async unary(m, signal, ...rest) {
      if (m.name === method) await defer(signal);
      return shop.unary(m, signal, ...rest);
    },
    stream: (...args) => shop.stream(...args),
  };
}

function AdditionalCleanup({ namespace }: { namespace: "data-browser" | "storage-browser" }) {
  useReaderCacheCleanup(namespace);
  return null;
}

it.each([
  ["databases", false], ["describe", false], ["storage", false],
  ["databases", true], ["storage", true],
] as const)("keeps deferred synthetic %s reads during auth rotation (same principal: %s)", async (stage, samePrincipal) => {
  const namespace = stage === "storage" ? "storage-browser" : "data-browser";
  let release!: () => void;
  const pending = new Promise<void>((resolve) => { release = resolve; });
  const started = vi.fn();
  const aborted = vi.fn();
  async function defer(signal?: AbortSignal) {
    started();
    signal?.addEventListener("abort", aborted);
    await pending;
  }
  installShellTransport(deferring({ databases: "ListBrowsableDatabases", describe: "DescribeDatabase", storage: "ListObjects" }[stage], defer));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const departing = getReaderSession();
  const productionKey = [namespace, departing.principalId, departing.epoch, "retained-production"];
  const differentPrincipalKey = [namespace, "another-principal", departing.epoch, "not-the-departing-reader"];
  client.setQueryData(productionKey, "private production result");
  client.setQueryData(differentPrincipalKey, "unrelated reader result");
  const removal = vi.fn();
  const unsubscribe = client.getQueryCache().subscribe((event) => {
    if (event.type !== "removed") return;
    removal(event.query.queryKey);
    // Seed B between the first and second cleanup listeners, not after both.
    if (event.query.queryKey[3] === "retained-production") {
      const incoming = getReaderSession();
      client.setQueryData([namespace, incoming.principalId, incoming.epoch, "between-listeners"], "new reader result");
    }
  });
  const view = render(<QueryClientProvider client={client}><AdditionalCleanup namespace={namespace} /><DemoBrowserProviders>
    {stage === "storage" ? <StorageBrowser env={{ id: "env-staging", name: "staging" }} search={{ bucket: "uploads" }} onNavigate={() => {}} href={() => "#"} />
      : <DataBrowser env={{ id: "env-staging", name: "staging" }} search={{ db: "shop", table: "public.customers" }} onNavigate={() => {}} href={() => "#"} />}
  </DemoBrowserProviders></QueryClientProvider>);
  await waitFor(() => expect(started).toHaveBeenCalledTimes(1));
  const fixtureQuery = client.getQueryCache().findAll().find((query) => query.queryKey[0] === namespace && query.state.fetchStatus === "fetching")!;
  expect(fixtureQuery.queryKey[1]).toBe("synthetic-console-demo");
  expect(fixtureQuery.queryKey[2]).not.toBe(departing.epoch);
  act(() => setReaderPrincipal(samePrincipal ? "production-a" : "production-b", samePrincipal));
  const incoming = getReaderSession();
  expect(client.getQueryData([namespace, incoming.principalId, incoming.epoch, "between-listeners"])).toBe("new reader result");
  const incomingKey = [namespace, incoming.principalId, incoming.epoch, "new-current-reader"];
  client.setQueryData(incomingKey, "new reader result");
  expect(client.getQueryData(incomingKey)).toBe("new reader result");
  expect(aborted).not.toHaveBeenCalled();
  expect(client.getQueryData(productionKey)).toBeUndefined();
  expect(client.getQueryData(differentPrincipalKey)).toBe("unrelated reader result");
  expect(removal).not.toHaveBeenCalledWith(fixtureQuery.queryKey);
  await act(async () => release());
  if (stage === "storage") await screen.findByTestId("storage-table");
  else await waitFor(() => expect(screen.getByTestId("data-grid")).toHaveAttribute("data-state", "ready"));
  expect(aborted).not.toHaveBeenCalled();
  // Unmount must unregister cleanup: later auth changes cannot mutate this client.
  view.unmount();
  const b = getReaderSession();
  const afterUnmount = [namespace, b.principalId, b.epoch, "after-unmount"];
  client.setQueryData(afterUnmount, "preserved after subscription cleanup");
  act(() => setReaderPrincipal("production-c"));
  expect(client.getQueryData(afterUnmount)).toBe("preserved after subscription cleanup");
  unsubscribe();
});
