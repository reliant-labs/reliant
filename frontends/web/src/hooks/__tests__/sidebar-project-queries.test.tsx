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

import type { SettingRead } from "../../lib/settingsPersistence";

// The server's copy of the setting, and what each read and write does.
let stored: string | undefined;
let failReads = false;
let failWrites = false;
const writeStringSetting = vi.fn(async (_key: string, value: string) => {
  if (failWrites) throw new Error("refused");
  stored = value;
});
const readSettingFromServer = vi.fn(async (): Promise<SettingRead> => {
  if (failReads) return { status: "error" };
  return stored === undefined ? { status: "missing" } : { status: "found", value: stored };
});
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
vi.mock("../../lib/settingsPersistence", () => ({
  readSettingFromServer: () => readSettingFromServer(),
  writeStringSetting: (key: string, value: string) => writeStringSetting(key, value),
}));

import { bindActiveOrgUser, setEffectiveOrg, useActiveOrgStore } from "@/store/activeOrgStore";
import { SIDEBAR_PROJECTS_KEY } from "../../lib/sidebarProjectPrefs";
import { useSidebarProjectPrefs } from "../sidebar-project-queries";

const OTHER_ORG = { "org-b": { pinned: ["p9"] } };

function setup() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );
  return renderHook(() => useSidebarProjectPrefs(), { wrapper });
}

describe("useSidebarProjectPrefs", () => {
  beforeEach(() => {
    stored = JSON.stringify(OTHER_ORG);
    failReads = false;
    failWrites = false;
    writeStringSetting.mockClear();
    readSettingFromServer.mockClear();
    useActiveOrgStore.setState(useActiveOrgStore.getInitialState(), true);
    bindActiveOrgUser("user-1");
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
