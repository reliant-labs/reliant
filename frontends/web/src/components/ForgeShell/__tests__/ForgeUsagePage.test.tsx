import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useRouteOrgScope } from "@/lib/routeOrg";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { ForgeUsagePage } from "../ForgeUsagePage";

vi.mock("@/components/Forge/Observability/Dashboards/OrganizationUsageView", () => ({
  OrganizationUsageView: ({ orgId }: { orgId: string }) => <div data-testid="usage-org">{orgId}</div>,
}));

const routeStore = useRouteOrgScope.getState();
const tabStore = useActiveOrgStore.getState();

afterEach(() => {
  cleanup();
  useRouteOrgScope.setState(routeStore);
  useActiveOrgStore.setState(tabStore);
});

describe("Forge usage organization", () => {
  it("uses the committed route even before the tab's effective org has caught up", () => {
    useActiveOrgStore.setState({ effectiveId: "org-a" });
    useRouteOrgScope.setState({ scope: { kind: "member", org: { id: "org-b", slug: "side" } } });
    render(<ForgeUsagePage />);
    expect(screen.getByTestId("usage-org")).toHaveTextContent("org-b");
  });

  it("does not fall back to the previous tab org when route membership is pending", () => {
    useActiveOrgStore.setState({ effectiveId: "org-a" });
    useRouteOrgScope.setState({ scope: { kind: "pending" } });
    render(<ForgeUsagePage />);
    expect(screen.getByTestId("usage-org")).toBeEmptyDOMElement();
  });
});
