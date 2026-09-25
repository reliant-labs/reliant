import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { TokenKind } from "../../../gen/reliant/v1/token_pb";

// TokenSettings drives reliant.v1.TokenService (kind DAEMON) — the ONE
// machine-credential surface. These pin the wire requests it sends, because
// a request without `kind` is refused server-side (InvalidArgument) and a
// list without it would surface API tokens on the daemon-token page.

const listTokens = vi.fn();
const createToken = vi.fn();
const revokeToken = vi.fn();

vi.mock("../../../api/grpc-client", () => ({
  grpcClient: {
    token: () => ({ listTokens, createToken, revokeToken }),
  },
}));

import { TokenSettings } from "../TokenSettings";

const liveToken = {
  id: "tok-1",
  name: "build-box",
  tokenPrefix: "rlat_A3f9Kd2p",
  createdAt: "2026-09-01T00:00:00Z",
  lastUsedAt: "",
  expiresAt: "",
  kind: TokenKind.DAEMON,
  ephemeral: false,
  daemonId: "",
};

describe("TokenSettings", () => {
  beforeEach(() => {
    listTokens.mockReset().mockResolvedValue({ tokens: [liveToken] });
    createToken.mockReset();
    revokeToken.mockReset().mockResolvedValue({});
  });

  it("lists DAEMON tokens only and shows their display prefix", async () => {
    // Mutation caught: dropping `kind: TokenKind.DAEMON` from the list call.
    render(<TokenSettings />);
    expect(await screen.findByText("build-box")).toBeInTheDocument();
    expect(screen.getByText("rlat_A3f9Kd2p...")).toBeInTheDocument();
    expect(listTokens).toHaveBeenCalledTimes(1);
    expect(listTokens.mock.calls[0][0]).toMatchObject({ kind: TokenKind.DAEMON });
  });

  it("mints a DAEMON token and reveals the rlat_ secret once", async () => {
    // Mutation caught: minting without `kind` (the server refuses it) or
    // rendering anything but the returned secret.
    createToken.mockResolvedValue({ token: "rlat_secretvalue", info: { id: "tok-2" } });
    render(<TokenSettings />);
    await screen.findByText("build-box");

    fireEvent.click(screen.getByText("New Token"));
    fireEvent.change(screen.getByPlaceholderText("Token name (e.g. my-server)"), {
      target: { value: "  laptop  " },
    });
    fireEvent.click(screen.getByText("Create"));

    await waitFor(() => expect(createToken).toHaveBeenCalledTimes(1));
    expect(createToken.mock.calls[0][0]).toMatchObject({ name: "laptop", kind: TokenKind.DAEMON });
    expect(await screen.findByDisplayValue("rlat_secretvalue")).toBeInTheDocument();
    expect(listTokens).toHaveBeenCalledTimes(2);
  });

  it("revokes by the token's id", async () => {
    // Mutation caught: sending the retired `tokenId` field instead of `id`.
    render(<TokenSettings />);
    await screen.findByText("build-box");
    fireEvent.click(screen.getByRole("button", { name: "" }));
    fireEvent.click(await screen.findByText("Confirm"));
    await waitFor(() => expect(revokeToken).toHaveBeenCalledTimes(1));
    expect(revokeToken.mock.calls[0][0]).toMatchObject({ id: "tok-1" });
  });
});
