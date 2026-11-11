import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "https://admin.example.test",
  hasControlPlane: true,
}));

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { useActiveOrgStore } from "@/store/activeOrgStore";

import { ForgeTokensSection } from "../ForgeTokensSection";

/**
 * Settings › API tokens: the secret is shown once after a create the server
 * accepted and never after one it refused, every rpc carries the active org,
 * and the instructions name the variable the forge CLI actually reads.
 */

const ORG = "org_acme";

function renderSection() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ForgeTokensSection />
    </QueryClientProvider>,
  );
}

function createToken(name = "ci-deploy") {
  fireEvent.change(screen.getByTestId("forge-token-name"), { target: { value: name } });
  fireEvent.click(screen.getByRole("button", { name: /create token/i }));
}

beforeEach(() => {
  useActiveOrgStore.setState({ userId: "u_1", chosenId: ORG, effectiveId: ORG });
});

afterEach(() => {
  setTransport(null);
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null });
});

describe("ForgeTokensSection", () => {
  it("shows the secret once after a successful create, with the right CLI instructions", async () => {
    const createSeen = vi.fn(() => ({ secret: "rlt_shown_once" }));
    setTransport(
      mockTransport({
        "AccessTokenService.ListMyTokens": { response: { tokens: [] } },
        "AccessTokenService.CreateMyToken": createSeen,
      }),
    );
    renderSection();
    await screen.findByText("No tokens yet.");

    createToken();

    const box = await screen.findByTestId("forge-token-secret");
    expect(box.textContent).toContain("rlt_shown_once");
    const usage = screen.getByTestId("forge-token-usage").textContent ?? "";
    expect(usage).toContain("FORGE_CONTROL_PLANE_TOKEN");
    expect(usage).not.toMatch(/\bFORGE_TOKEN\b/);
    expect(createSeen).toHaveBeenCalledWith(
      expect.objectContaining({ orgId: ORG, name: "ci-deploy", scopes: ["deploy:read", "deploy:write"] }),
    );
  });

  it("a refused create shows an error and never a secret", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.ListMyTokens": { response: { tokens: [] } },
        "AccessTokenService.CreateMyToken": () => {
          throw new ConnectError("denied", Code.PermissionDenied);
        },
      }),
    );
    renderSection();
    await screen.findByText("No tokens yet.");

    createToken();

    expect(await screen.findByText(/could not create the token/i)).toBeTruthy();
    expect(screen.queryByTestId("forge-token-secret")).toBeNull();
  });

  it("discloses secret:read's access to local secret values before creating", async () => {
    setTransport(mockTransport({ "AccessTokenService.ListMyTokens": { response: { tokens: [] } } }));
    renderSection();

    expect(await screen.findByText(/pull the secret VALUES of local environments/i)).toBeTruthy();
  });

  it("lists only live tokens and revokes in the active org", async () => {
    const revokeSeen = vi.fn(() => ({}));
    setTransport(
      mockTransport({
        "AccessTokenService.ListMyTokens": {
          response: {
            tokens: [
              { id: "t_live", name: "ci-deploy", displayPrefix: "rlt_ab", scopes: ["deploy:write"] },
              { id: "t_ephemeral", name: "session", ephemeral: true },
            ],
          },
        },
        "AccessTokenService.RevokeMyToken": revokeSeen,
      }),
    );
    renderSection();

    const row = await screen.findByTestId("forge-token-t_live");
    expect(screen.queryByTestId("forge-token-t_ephemeral")).toBeNull();

    fireEvent.click(row.querySelector("button") as HTMLButtonElement);

    await waitFor(() => expect(revokeSeen).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG, id: "t_live" })));
  });
});
