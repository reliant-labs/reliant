// Copyright (c) 2025 Reliant Labs

/**
 * A deploy in a chat that forge queued on billing (exit 7) offers the one
 * action that sends it out: "Set up billing", on the hosting tab's agreement
 * card, for the org the environment was read in — so billing asks before
 * acting in any other — with the chat as the way back.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const navigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));

vi.mock("@/hooks/forge-queries", () => ({
  useCloudEnvironments: () => ({ data: { envs: [], availability: "available", detail: "" }, isLoading: false }),
  useCloudEnvStatus: () => ({ data: undefined, isLoading: false }),
}));

vi.mock("@/store/projectStore", () => {
  const state = { currentProject: { id: "proj-1", forge_project_name: "shop" } };
  return { useProjectStore: (selector: (s: unknown) => unknown) => selector(state) };
});

const scope = { ready: true, orgId: "org-b" as string | null, error: null, retry: () => {} };
vi.mock("@/lib/orgScope", () => ({ useOrgScope: () => scope }));

import { DeployedAppCard } from "../DeployedAppCard";

beforeEach(() => {
  navigate.mockReset();
  scope.orgId = "org-b";
  window.history.pushState({}, "", "/chat/c1?thread=t1");
});

describe("DeployedAppCard, queued on billing", () => {
  it("keeps the queued copy and offers Set up billing for the env's org", async () => {
    render(<DeployedAppCard deploy={{ env: "prod", queued: true }} />);
    expect(screen.getByTestId("deployed-app-card")).toHaveTextContent("Deploy to prod queued — it goes live once billing is set up");

    await userEvent.click(screen.getByTestId("deployed-app-billing"));
    expect(navigate).toHaveBeenCalledWith({
      to: "/settings/billing",
      search: { tab: "hosting", from: "chat", returnTo: "/chat/c1?thread=t1", org: "org-b" },
      hash: "app-hosting",
    });
  });

  it("names no org when the org scope has none", async () => {
    scope.orgId = null;
    render(<DeployedAppCard deploy={{ env: "prod", queued: true }} />);
    await userEvent.click(screen.getByTestId("deployed-app-billing"));
    const call = navigate.mock.calls.at(-1)?.[0] as { search: Record<string, unknown> };
    expect(call.search).not.toHaveProperty("org");
  });

  it("offers no billing action for a deploy that went out", () => {
    render(<DeployedAppCard deploy={{ env: "prod", queued: false }} />);
    expect(screen.queryByTestId("deployed-app-billing")).toBeNull();
  });
});
