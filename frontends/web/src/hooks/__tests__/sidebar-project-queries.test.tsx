/**
 * Sidebar pins, hides and show-all live in one document holding every org's
 * entry. A change is written under the org it was made in, merged into the
 * document as the server holds it, and never over a document that could not
 * be read; a refused write puts back what was there.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { SessionOwner } from "../../lib/auth/sessionOwner";
import type { SettingRead } from "../../lib/settingsPersistence";

// The server's copy of the setting, and what each read and write does.
let stored: string | undefined;
let failReads = false;
let failWrites = false;
// When set, the next read or write waits for the test to settle it.
let holdNextRead: Promise<void> | null = null;
let holdNextWrite: Promise<void> | null = null;
const writeStringSetting = vi.fn(async (_key: string, value: string, _owner?: SessionOwner) => {
  const hold = holdNextWrite;
  holdNextWrite = null;
  if (hold) await hold;
  if (failWrites) throw new Error("refused");
  stored = value;
});
const readSettingFromServer = vi.fn(async (_key: string, owner?: SessionOwner): Promise<SettingRead> => {
  // `stored` is Alice's; any other account has nothing saved.
  if (owner?.userId !== "alice") return { status: "missing" };
  const hold = holdNextRead;
  holdNextRead = null;
  if (hold) await hold;
  if (failReads) return { status: "error" };
  return stored === undefined ? { status: "missing" } : { status: "found", value: stored };
});
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
vi.mock("../../lib/settingsPersistence", () => ({
  readSettingFromServer: (key: string, owner?: SessionOwner) => readSettingFromServer(key, owner),
  writeStringSetting: (key: string, value: string, owner?: SessionOwner) => writeStringSetting(key, value, owner),
}));

import {
  beginSessionTransition,
  currentSessionOwner,
  resetSessionOwnerForTests,
  setSessionOwner,
} from "@/lib/auth/sessionOwner";
import { bindActiveOrgUser, setEffectiveOrg, useActiveOrgStore } from "@/store/activeOrgStore";
import { SIDEBAR_PROJECTS_KEY, type SidebarProjectSettings } from "../../lib/sidebarProjectPrefs";
import { sidebarProjectPrefsKey, useSidebarProjectPrefs } from "../sidebar-project-queries";

const OTHER_ORG = { "org-b": { pinned: ["p9"] } };

function deferred() {
  let release!: () => void;
  const promise = new Promise<void>((resolve) => {
    release = resolve;
  });
  return { promise, release };
}

function setup() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );
  return { qc, ...renderHook(() => useSidebarProjectPrefs(), { wrapper }) };
}

/** Another account signs in: authStore installs its owner and clears the cache. */
function switchAccount(qc: QueryClient, userId: string) {
  act(() => {
    setSessionOwner(userId);
    qc.getQueryCache().clear();
  });
}

const settled = (qc: QueryClient) =>
  qc
    .getMutationCache()
    .getAll()
    .every((m) => m.state.status === "success" || m.state.status === "error");

describe("useSidebarProjectPrefs", () => {
  beforeEach(() => {
    stored = JSON.stringify(OTHER_ORG);
    failReads = false;
    failWrites = false;
    holdNextRead = null;
    holdNextWrite = null;
    writeStringSetting.mockClear();
    readSettingFromServer.mockClear();
    resetSessionOwnerForTests();
    setSessionOwner("alice");
    useActiveOrgStore.setState(useActiveOrgStore.getInitialState(), true);
    bindActiveOrgUser("user-1");
  });

  it("refuses a change made while the page acts for nobody (an identity change in progress)", async () => {
    act(() => setEffectiveOrg("org-a"));
    const { qc, result } = setup();
    await waitFor(() => expect(result.current.writable).toBe(true));
    const before = qc.getQueryData<SidebarProjectSettings>(sidebarProjectPrefsKey);
    readSettingFromServer.mockClear();
    // The still-visible sidebar is clicked after the change began, before it cleared anything.
    act(() => beginSessionTransition());
    act(() => result.current.setPinned("p1", true));
    await waitFor(() => expect(settled(qc)).toBe(true));
    expect(qc.getMutationCache().getAll()).toHaveLength(1);
    expect(readSettingFromServer).not.toHaveBeenCalled();
    expect(writeStringSetting).not.toHaveBeenCalled();
    // Not even the optimistic update was published.
    expect(qc.getQueryData<SidebarProjectSettings>(sidebarProjectPrefsKey)).toBe(before);
  });

  it("drops a change whose re-read answers after the account switched: nothing written or published", async () => {
    act(() => setEffectiveOrg("org-a"));
    const { qc, result } = setup();
    await waitFor(() => expect(result.current.writable).toBe(true));
    const alice = currentSessionOwner();
    const read = deferred();
    holdNextRead = read.promise;
    act(() => result.current.setPinned("p1", true));
    // The re-read went out as Alice's.
    await waitFor(() => expect(readSettingFromServer).toHaveBeenCalledTimes(2));
    expect(readSettingFromServer.mock.calls[1]![1]).toBe(alice);

    switchAccount(qc, "bob");
    read.release();
    await waitFor(() => expect(settled(qc)).toBe(true));
    expect(writeStringSetting).not.toHaveBeenCalled();
    // Nothing of Alice's change reached the cache Bob now reads.
    const data = qc.getQueryData<SidebarProjectSettings>(sidebarProjectPrefsKey);
    expect(data?.["org-a"]).toBeUndefined();
    expect(JSON.parse(stored!)).toEqual(OTHER_ORG);
  });

  it("sends nothing for a change queued behind another when the account switches, and publishes neither", async () => {
    act(() => setEffectiveOrg("org-a"));
    const { qc, result } = setup();
    await waitFor(() => expect(result.current.writable).toBe(true));
    const alice = currentSessionOwner();
    const write = deferred();
    holdNextWrite = write.promise;
    act(() => result.current.setPinned("p1", true));
    // The first change is sending; the second waits behind it.
    await waitFor(() => expect(writeStringSetting).toHaveBeenCalledTimes(1));
    act(() => result.current.setHidden("p2", true));

    switchAccount(qc, "bob");
    write.release();
    await waitFor(() => expect(qc.getMutationCache().getAll()).toHaveLength(2));
    await waitFor(() => expect(settled(qc)).toBe(true));
    // Only the first, sent as Alice's before the switch, was written; the
    // queued one neither re-read nor wrote.
    expect(writeStringSetting).toHaveBeenCalledTimes(1);
    expect(writeStringSetting.mock.calls[0]![2]).toBe(alice);
    expect(readSettingFromServer.mock.calls.filter(([, owner]) => owner === alice)).toHaveLength(2);
    expect(JSON.parse(stored!)).toEqual({ "org-a": { pinned: ["p1"] }, ...OTHER_ORG });
    // Neither result was published into the cache Bob now reads.
    expect(qc.getQueryData<SidebarProjectSettings>(sidebarProjectPrefsKey)?.["org-a"]).toBeUndefined();
  });

  it("writes a pin under the tab's org, keeping other orgs' entries", async () => {
    act(() => setEffectiveOrg("org-a"));
    const { result } = setup();
    await waitFor(() => expect(result.current.writable).toBe(true));
    act(() => result.current.setPinned("p1", true));
    await waitFor(() => expect(writeStringSetting).toHaveBeenCalledTimes(1));
    const [key, value] = writeStringSetting.mock.calls[0]!;
    expect(key).toBe(SIDEBAR_PROJECTS_KEY);
    expect(JSON.parse(value)).toEqual({ "org-a": { pinned: ["p1"] }, ...OTHER_ORG });
    expect(result.current.prefs.pinned.has("p1")).toBe(true);
  });

  it("merges into the document as stored when it writes, not as first read", async () => {
    act(() => setEffectiveOrg("org-a"));
    const { result } = setup();
    await waitFor(() => expect(result.current.writable).toBe(true));
    // Another tab saved a choice in a third org after this tab read.
    stored = JSON.stringify({ ...OTHER_ORG, "org-c": { show_all: true } });
    act(() => result.current.setHidden("p2", true));
    await waitFor(() => expect(writeStringSetting).toHaveBeenCalledTimes(1));
    expect(JSON.parse(writeStringSetting.mock.calls[0]![1])).toEqual({
      "org-a": { hidden: ["p2"] },
      ...OTHER_ORG,
      "org-c": { show_all: true },
    });
  });

  it("accepts no change after a failed read, and offers a retry that keeps other orgs' entries", async () => {
    failReads = true;
    act(() => setEffectiveOrg("org-a"));
    const { result } = setup();
    await waitFor(() => expect(result.current.loadFailed).toBe(true));
    expect(result.current.writable).toBe(false);
    act(() => result.current.setShowAll(true));
    expect(result.current.prefs.showAll).toBe(false);
    expect(writeStringSetting).not.toHaveBeenCalled();

    failReads = false;
    act(() => result.current.retry());
    await waitFor(() => expect(result.current.writable).toBe(true));
    act(() => result.current.setShowAll(true));
    await waitFor(() => expect(writeStringSetting).toHaveBeenCalledTimes(1));
    expect(JSON.parse(writeStringSetting.mock.calls[0]![1])).toEqual({ "org-a": { show_all: true }, ...OTHER_ORG });
  });

  it("puts back the prior value when the write is refused, other orgs' entries kept", async () => {
    stored = JSON.stringify({ ...OTHER_ORG, "org-a": { pinned: ["p1"] } });
    failWrites = true;
    act(() => setEffectiveOrg("org-a"));
    const { result } = setup();
    await waitFor(() => expect(result.current.writable).toBe(true));
    act(() => result.current.setPinned("p1", false));
    await waitFor(() => expect(writeStringSetting).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(result.current.prefs.pinned.has("p1")).toBe(true));
    // The server's document is untouched, and the hook reads it again.
    expect(JSON.parse(stored!)).toEqual({ ...OTHER_ORG, "org-a": { pinned: ["p1"] } });
    act(() => setEffectiveOrg("org-b"));
    expect(result.current.prefs.pinned.has("p9")).toBe(true);
  });

  it("shows the defaults and writes nothing while the tab's org is unknown", async () => {
    const { result } = setup();
    await waitFor(() => expect(readSettingFromServer).toHaveBeenCalled());
    expect(result.current.writable).toBe(false);
    act(() => result.current.setHidden("p1", true));
    expect(result.current.prefs.hidden.size).toBe(0);
    expect(writeStringSetting).not.toHaveBeenCalled();
  });
});
