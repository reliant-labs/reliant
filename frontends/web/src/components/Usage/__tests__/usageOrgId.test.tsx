import { renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { useRouteOrgScope } from "@/lib/routeOrg";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { useUsageOrgId } from "../usageOrgId";

const routeStore = useRouteOrgScope.getState();
const tabStore = useActiveOrgStore.getState();

afterEach(() => {
  useRouteOrgScope.setState(routeStore);
  useActiveOrgStore.setState(tabStore);
});

describe("Usage & billing's organization", () => {
  it("uses the committed route even before the tab's effective org has caught up", () => {
    useActiveOrgStore.setState({ effectiveId: "org-a" });
    useRouteOrgScope.setState({ scope: { kind: "member", org: { id: "org-b", slug: "side" } } });
    expect(renderHook(() => useUsageOrgId()).result.current).toBe("org-b");
  });

  it("does not fall back to the previous tab org when route membership is pending", () => {
    useActiveOrgStore.setState({ effectiveId: "org-a" });
    useRouteOrgScope.setState({ scope: { kind: "pending" } });
    expect(renderHook(() => useUsageOrgId()).result.current).toBe("");
  });
});
