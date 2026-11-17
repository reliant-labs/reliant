/**
 * Sidebar pins, hides and show-all are written under the org the change was
 * made in, and not at all while the tab's org is unknown.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const upsertStringSetting = vi.fn(async (_key: string, _value: string) => undefined);
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
vi.mock("../../lib/settingsPersistence", () => ({
  readSetting: async () => ({ status: "found", value: JSON.stringify({ "org-b": { pinned: ["p9"] } }) }),
  upsertStringSetting: (key: string, value: string) => upsertStringSetting(key, value),
}));

import { bindActiveOrgUser, setEffectiveOrg, useActiveOrgStore } from "@/store/activeOrgStore";
import { SIDEBAR_PROJECTS_KEY } from "../../lib/sidebarProjectPrefs";
import { useSidebarProjectPrefs } from "../sidebar-project-queries";

function setup() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );
  return renderHook(() => useSidebarProjectPrefs(), { wrapper });
}

describe("useSidebarProjectPrefs", () => {
  beforeEach(() => {
    upsertStringSetting.mockClear();
    useActiveOrgStore.setState(useActiveOrgStore.getInitialState(), true);
    bindActiveOrgUser("user-1");
  });

  it("writes a pin under the tab's org, keeping other orgs' entries", async () => {
    act(() => setEffectiveOrg("org-b"));
    const { result } = setup();
    // The read has landed: the other org's entry is in the cache to keep.
    await waitFor(() => expect(result.current.prefs.pinned.has("p9")).toBe(true));
    act(() => setEffectiveOrg("org-a"));
    expect(result.current.prefs.pinned.size).toBe(0);
    act(() => result.current.setPinned("p1", true));
    await waitFor(() => expect(upsertStringSetting).toHaveBeenCalled());
    const [key, value] = upsertStringSetting.mock.calls.at(-1)!;
    expect(key).toBe(SIDEBAR_PROJECTS_KEY);
    expect(JSON.parse(value)).toEqual({ "org-a": { pinned: ["p1"] }, "org-b": { pinned: ["p9"] } });
    expect(result.current.prefs.pinned.has("p1")).toBe(true);
  });

  it("shows the defaults and writes nothing while the tab's org is unknown", async () => {
    const { result } = setup();
    act(() => result.current.setHidden("p1", true));
    expect(result.current.prefs.hidden.size).toBe(0);
    expect(upsertStringSetting).not.toHaveBeenCalled();
  });
});
