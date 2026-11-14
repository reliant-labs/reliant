/**
 * The model picker lists what a chat of the active org may run on
 * (CatalogService ListModels / ListAvailableModels org_id): the org's accounts
 * and seats, narrowed by its policy. So the list names the active org, and a
 * change of it (a switch, a reconcile onto the default, another tab) re-lists,
 * never showing the org that was left.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";

const fetchModelList = vi.hoisted(() => vi.fn());
const waitForConfig = vi.hoisted(() => vi.fn(async () => {}));
vi.mock("../../hooks/catalog-models", () => ({ fetchModelList }));
vi.mock("../../lib/configReady", () => ({ waitForConfig }));

import { useActiveOrgStore } from "../activeOrgStore";
import { useGlobalDataStore } from "../globalDataStore";

function modelsOf(org: string | null) {
  return { models: [{ id: `m@${org ?? "own"}`, name: "m", provider: "p", capabilities: [], tags: [] }], total: 1, tiers: {} };
}

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

beforeEach(() => {
  waitForConfig.mockReset();
  waitForConfig.mockImplementation(async () => {});
  fetchModelList.mockReset();
  fetchModelList.mockImplementation(async (org: string | null) => modelsOf(org));
  useGlobalDataStore.getState().reset();
  useActiveOrgStore.setState({ userId: "u1", chosenId: null, effectiveId: "acme" });
});

describe("globalDataStore and the active org", () => {
  it("lists the active org's models", async () => {
    await useGlobalDataStore.getState().prefetch();

    expect(fetchModelList).toHaveBeenCalledWith("acme");
    expect(useGlobalDataStore.getState().models.map((m) => m.id)).toEqual(["m@acme"]);
    expect(useGlobalDataStore.getState().modelsOrgId).toBe("acme");
  });

  it("re-lists for the org switched to, and drops the one left at once", async () => {
    await useGlobalDataStore.getState().prefetch();
    const next = deferred<ReturnType<typeof modelsOf>>();
    fetchModelList.mockImplementationOnce(() => next.promise);

    useActiveOrgStore.setState({ effectiveId: "globex" });

    expect(fetchModelList).toHaveBeenLastCalledWith("globex");
    expect(useGlobalDataStore.getState().models, "acme's models are not shown for globex").toEqual([]);
    next.resolve(modelsOf("globex"));
    await vi.waitFor(() => expect(useGlobalDataStore.getState().models.map((m) => m.id)).toEqual(["m@globex"]));
    expect(useGlobalDataStore.getState().modelsOrgId).toBe("globex");
  });

  it("drops an answer for the org that was left while it was in flight", async () => {
    const slow = deferred<ReturnType<typeof modelsOf>>();
    fetchModelList.mockImplementationOnce(() => slow.promise);
    const prefetch = useGlobalDataStore.getState().prefetch();
    await vi.waitFor(() => expect(fetchModelList).toHaveBeenCalledWith("acme"));

    useActiveOrgStore.setState({ effectiveId: "globex" });
    await vi.waitFor(() => expect(useGlobalDataStore.getState().models.map((m) => m.id)).toEqual(["m@globex"]));

    slow.resolve(modelsOf("acme"));
    await prefetch;
    expect(useGlobalDataStore.getState().models.map((m) => m.id)).toEqual(["m@globex"]);
  });

  it("lists the org switched to while the config was still loading, never the one left", async () => {
    const config = deferred<void>();
    waitForConfig.mockImplementationOnce(() => config.promise);
    const prefetch = useGlobalDataStore.getState().prefetch();

    useActiveOrgStore.setState({ effectiveId: "globex" });
    await vi.waitFor(() => expect(useGlobalDataStore.getState().models.map((m) => m.id)).toEqual(["m@globex"]));

    config.resolve();
    await prefetch;
    expect(fetchModelList, "the prefetch that was replaced asks for nothing").not.toHaveBeenCalledWith("acme");
    const state = useGlobalDataStore.getState();
    expect(state.models.map((m) => m.id)).toEqual(["m@globex"]);
    expect(state.modelsOrgId).toBe("globex");
    expect(state.modelsLoading || state.isPrefetching, "no loading flag is left set").toBe(false);
  });

  it("does nothing before the first load, which reads the org itself", () => {
    useActiveOrgStore.setState({ effectiveId: "globex" });
    expect(fetchModelList).not.toHaveBeenCalled();
  });

  it("does not re-list when the org did not change", async () => {
    await useGlobalDataStore.getState().prefetch();
    fetchModelList.mockClear();

    useGlobalDataStore.getState().followActiveOrg("acme");

    expect(fetchModelList).not.toHaveBeenCalled();
  });
});
