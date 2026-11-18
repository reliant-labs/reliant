import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "https://admin.example.test",
  hasControlPlane: true,
}));

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

import { CliLoginsSection } from "../CliLoginsSection";

/**
 * Settings › API tokens › CLI logins: a person sees each live `forge login`
 * (never an ended one) and revokes one by its id; a refused list says so
 * rather than claiming there are none (reliant#1831).
 */

const DAY = 86_400_000;
const now = Date.now();

function renderSection() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <CliLoginsSection />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  setTransport(null);
});

describe("CliLoginsSection", () => {
  it("lists live CLI logins and hides revoked and expired ones", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.ListMyCLILogins": {
          response: {
            logins: [
              {
                id: "cli_live",
                clientId: "forge-cli",
                deviceName: "laptop",
                createdAt: timestampFromMs(now - 3 * DAY),
                expiresAt: timestampFromMs(now + 80 * DAY),
                lastUsedAt: timestampFromMs(now - DAY),
              },
              {
                id: "cli_revoked",
                clientId: "forge-cli",
                deviceName: "old-desktop",
                createdAt: timestampFromMs(now - 30 * DAY),
                expiresAt: timestampFromMs(now + 60 * DAY),
                revokedAt: timestampFromMs(now - 2 * DAY),
              },
              {
                id: "cli_expired",
                clientId: "forge-cli",
                deviceName: "ci-box",
                createdAt: timestampFromMs(now - 100 * DAY),
                expiresAt: timestampFromMs(now - 10 * DAY),
              },
            ],
          },
        },
      }),
    );
    renderSection();

    const row = await screen.findByTestId("cli-login-cli_live");
    expect(row.textContent).toContain("laptop");
    expect(row.textContent).toContain("forge CLI");
    expect(screen.queryByTestId("cli-login-cli_revoked")).toBeNull();
    expect(screen.queryByTestId("cli-login-cli_expired")).toBeNull();
  });

  it("revokes the login it names and reloads the list", async () => {
    const revoke = vi.fn(() => ({}));
    let listed = 0;
    setTransport(
      mockTransport({
        "AccessTokenService.ListMyCLILogins": () => {
          listed++;
          return listed === 1
            ? { logins: [{ id: "cli_1", clientId: "forge-cli", deviceName: "laptop", expiresAt: timestampFromMs(now + DAY) }] }
            : { logins: [] };
        },
        "AccessTokenService.RevokeMyCLILogin": revoke,
      }),
    );
    renderSection();

    fireEvent.click(await screen.findByRole("button", { name: /revoke the cli login on laptop/i }));

    await waitFor(() => expect(revoke).toHaveBeenCalledWith(expect.objectContaining({ id: "cli_1" })));
    expect(await screen.findByText("No CLI logins.")).toBeTruthy();
  });

  it("a refused list is reported, not shown as empty", async () => {
    setTransport(
      mockTransport({
        "AccessTokenService.ListMyCLILogins": () => {
          throw new ConnectError("person session required", Code.PermissionDenied);
        },
      }),
    );
    renderSection();

    expect(await screen.findByText(/could not load your cli logins/i)).toBeTruthy();
    expect(screen.queryByText("No CLI logins.")).toBeNull();
  });
});
