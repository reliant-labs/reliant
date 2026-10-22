import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { TokenKind } from "../../../gen/reliant/v1/token_pb";

// A daemon credential is PERMANENT (control-plane migration 00112), so there
// is no expiry to bound it and "what can this token do" is the only audit
// question left. These pin that the list answers it, that the bound daemon is
// visible, and that editing goes through UpdateToken — which never reissues
// the secret, so a remote daemon keeps working.

const listTokens = vi.fn();
const createToken = vi.fn();
const revokeToken = vi.fn();
const updateToken = vi.fn();

vi.mock("../../../api/grpc-client", () => ({
  grpcClient: {
    token: () => ({ listTokens, createToken, revokeToken, updateToken }),
  },
}));

import { TokenSettings } from "../TokenSettings";

const daemonToken = {
  id: "tok-1",
  name: "build-box",
  tokenPrefix: "rlat_A3f9Kd2p",
  createdAt: "2026-09-01T00:00:00Z",
  lastUsedAt: "2026-09-20T00:00:00Z",
  expiresAt: "",
  kind: TokenKind.DAEMON,
  ephemeral: false,
  daemonId: "daemon-7",
  scopes: ["daemon:connect", "deploy:write", "secret:read"],
};

describe("TokenSettings — permissions and editing", () => {
  beforeEach(() => {
    listTokens.mockReset().mockResolvedValue({ tokens: [daemonToken] });
    createToken.mockReset();
    revokeToken.mockReset().mockResolvedValue({});
    updateToken.mockReset().mockResolvedValue({
      info: { ...daemonToken, name: "renamed" },
    });
  });

  it("shows the permissions a token carries", async () => {
    // Mutation caught: rendering the row without scopes, which leaves a
    // permanent credential's authority invisible.
    render(<TokenSettings />);
    await screen.findByText("build-box");
    expect(screen.getByText("daemon:connect")).toBeInTheDocument();
    expect(screen.getByText("deploy:write")).toBeInTheDocument();
    expect(screen.getByText("secret:read")).toBeInTheDocument();
  });

  it("shows which daemon a token is bound to", async () => {
    render(<TokenSettings />);
    await screen.findByText("build-box");
    expect(screen.getByText(/daemon-7/)).toBeInTheDocument();
  });

  it("reports a permanent token as never expiring, not as blank", async () => {
    // A blank expiry column reads as missing data. "Never expires" is the
    // actual fact, and it is the reason revoking matters.
    render(<TokenSettings />);
    await screen.findByText("build-box");
    expect(screen.getByText(/never expires/i)).toBeInTheDocument();
  });

  it("renames through UpdateToken without sending scopes", async () => {
    // Mutation caught: sending an empty scopes list on a rename, which would
    // strip every permission the daemon holds.
    render(<TokenSettings />);
    await screen.findByText("build-box");

    fireEvent.click(screen.getByRole("button", { name: /edit/i }));
    const nameField = await screen.findByDisplayValue("build-box");
    fireEvent.change(nameField, { target: { value: "renamed" } });
    fireEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => expect(updateToken).toHaveBeenCalledTimes(1));
    const sent = updateToken.mock.calls[0][0];
    expect(sent).toMatchObject({ id: "tok-1", name: "renamed" });
    expect(sent.scopes).toBeUndefined();
  });

  it("edits permissions through UpdateToken, carrying the full set", async () => {
    // The request replaces the whole set, so unticking one permission must
    // send the REMAINING ones, not the removed one.
    render(<TokenSettings />);
    await screen.findByText("build-box");

    fireEvent.click(screen.getByRole("button", { name: /edit/i }));
    await screen.findByDisplayValue("build-box");
    fireEvent.click(screen.getByRole("checkbox", { name: /deploy:write/i }));
    fireEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => expect(updateToken).toHaveBeenCalledTimes(1));
    const sent = updateToken.mock.calls[0][0];
    expect(sent.id).toBe("tok-1");
    expect(sent.scopes.scopes).toContain("daemon:connect");
    expect(sent.scopes.scopes).toContain("secret:read");
    expect(sent.scopes.scopes).not.toContain("deploy:write");
  });

  it("refreshes the list after an edit", async () => {
    render(<TokenSettings />);
    await screen.findByText("build-box");
    expect(listTokens).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByRole("button", { name: /edit/i }));
    const nameField = await screen.findByDisplayValue("build-box");
    fireEvent.change(nameField, { target: { value: "renamed" } });
    fireEvent.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => expect(listTokens).toHaveBeenCalledTimes(2));
  });

  it("warns that revoking disconnects the bound daemon", async () => {
    // Revoking a daemon credential is not just housekeeping: it drops that
    // daemon. The confirmation has to say so.
    render(<TokenSettings />);
    await screen.findByText("build-box");
    fireEvent.click(screen.getByRole("button", { name: /revoke/i }));
    expect(await screen.findByText(/disconnect/i)).toBeInTheDocument();
  });
});
