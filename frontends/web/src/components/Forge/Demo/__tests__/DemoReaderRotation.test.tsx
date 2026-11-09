// Copyright (c) 2025 Reliant Labs
import { beforeEach, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { DemoBrowserProviders } from "../DemoBrowserProviders";
import { useReaderCacheCleanup } from "../../AuditedAccess/useReaderCacheCleanup";
import { DataBrowser } from "../../Data/DataBrowser";
import { StorageBrowser } from "../../Storage/StorageBrowser";
import { createDataFixtureApi } from "../../Data/__fixtures__/fixtureDataApi";
import { createStorageFixtureApi } from "../../Storage/__fixtures__/fixtureStorageApi";
import { getReaderSession, setReaderPrincipal } from "../../AuditedAccess/readerSession";

const seam = vi.hoisted(() => ({ apis: {} as ReturnType<typeof import("../demoData/browserApis").createDemoBrowserApis> }));
vi.mock("../demoData/browserApis", () => ({ createDemoBrowserApis: () => seam.apis }));
vi.mock("@tanstack/react-router", async (original) => ({ ...await original<typeof import("@tanstack/react-router")>(), useNavigate: () => vi.fn() }));
beforeEach(() => setReaderPrincipal("production-a", true));

function AdditionalCleanup({ namespace }: { namespace: "data-browser" | "storage-browser" }) {
  useReaderCacheCleanup(namespace);
  return null;
}

it.each([
  ["databases", false], ["describe", false], ["storage", false],
  ["databases", true], ["storage", true],
] as const)("keeps deferred synthetic %s reads during auth rotation (same principal: %s)", async (stage, samePrincipal) => {
  const data = createDataFixtureApi({ onlyShop: true });
  const storage = createStorageFixtureApi();
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
  seam.apis = {
    data: {
      ...data,
      listBrowsableDatabases: async (...args) => { if (stage === "databases") await defer(args[1]?.signal); return data.listBrowsableDatabases(...args); },
      describeDatabase: async (...args) => { if (stage === "describe") await defer(args[1]?.signal); return data.describeDatabase(...args); },
    },
    storage: { ...storage, listObjects: async (...args) => { await defer(args[1]?.signal); return storage.listObjects(...args); } },
    identity: {} as typeof seam.apis.identity,
  };
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
      : <DataBrowser env={{ id: "env-staging", name: "staging" }} search={{ db: "shop", table: "public.users" }} onNavigate={() => {}} href={() => "#"} />}
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
