/**
 * The OAuth consent flow's ordering guarantee.
 *
 * The bug this pins: approving the Supabase authorization first redirects the
 * user back to the calling application immediately, ending the flow before a
 * connector exists. The client then completes OAuth, sends its handshake, and
 * gets a 403 with nothing on screen to explain why — the user has "signed in"
 * to something that does not work.
 *
 * So the order is load → configure connector → AuthorizeClient → approve →
 * redirect, and these tests fail if anything moves the approval earlier.
 */

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const approveAuthorization = vi.fn();
const denyAuthorization = vi.fn();
const getAuthorizationDetails = vi.fn();

vi.mock("../../../lib/supabase", () => ({
  supabase: {
    auth: {
      getUser: () => Promise.resolve({ data: { user: { id: "user-1" } } }),
      oauth: {
        getAuthorizationDetails: (...a: unknown[]) =>
          getAuthorizationDetails(...a),
        approveAuthorization: (...a: unknown[]) => approveAuthorization(...a),
        denyAuthorization: (...a: unknown[]) => denyAuthorization(...a),
      },
    },
  },
}));

// Records the call order across the two systems, which is the whole assertion.
const calls: string[] = [];

const authorizeClient = vi.fn(async () => {
  calls.push("authorizeClient");
});

vi.mock("../../Settings/ConnectorConsent", () => ({
  // Stands in for the real form: the ordering guarantee is about when onDone
  // fires relative to the approval, not about which tools were ticked.
  ConnectorConsent: ({
    clientId,
    onDone,
    onCancel,
  }: {
    clientId: string;
    onDone?: () => void;
    onCancel?: () => void;
  }) => (
    <div>
      <span data-testid="client-id">{clientId}</span>
      <button
        onClick={async () => {
          await authorizeClient();
          onDone?.();
        }}
      >
        Save connector
      </button>
      <button onClick={() => onCancel?.()}>Cancel connector</button>
    </div>
  ),
}));

import { OAuthConsent } from "../OAuthConsent";

const DETAILS = {
  authorization_id: "auth-1",
  client: { id: "client-uuid-1", name: "ChatGPT" },
  user: { id: "user-1", email: "ada@example.com" },
  scope: "openid email",
};

beforeEach(() => {
  calls.length = 0;
  vi.clearAllMocks();

  window.history.replaceState({}, "", "/oauth/consent?authorization_id=auth-1");
  getAuthorizationDetails.mockResolvedValue({ data: DETAILS, error: null });
  approveAuthorization.mockImplementation(async () => {
    calls.push("approve");
    return { data: { redirect_url: "https://chatgpt.example/cb?code=xyz" }, error: null };
  });
  denyAuthorization.mockResolvedValue({
    data: { redirect_url: "https://chatgpt.example/cb?error=denied" },
    error: null,
  });
});

describe("OAuth consent ordering", () => {
  it("does not approve the authorization on the identity step", async () => {
    render(<OAuthConsent />);

    const next = await screen.findByRole("button", { name: /continue/i });
    await userEvent.click(next);

    // Moving past identity must not have told Supabase anything.
    expect(approveAuthorization).not.toHaveBeenCalled();
    expect(await screen.findByTestId("client-id")).toBeTruthy();
  });

  it("records the connector binding before approving", async () => {
    render(<OAuthConsent />);

    await userEvent.click(
      await screen.findByRole("button", { name: /continue/i })
    );
    await userEvent.click(
      await screen.findByRole("button", { name: /save connector/i })
    );

    await waitFor(() => expect(approveAuthorization).toHaveBeenCalled());

    // The ordering guarantee, stated as the sequence itself.
    expect(calls).toEqual(["authorizeClient", "approve"]);
  });

  it("binds to the client's stable id rather than its display name", async () => {
    render(<OAuthConsent />);
    await userEvent.click(
      await screen.findByRole("button", { name: /continue/i })
    );

    expect((await screen.findByTestId("client-id")).textContent).toBe(
      "client-uuid-1"
    );
  });

  it("skips the SDK's own redirect so the page controls the last hop", async () => {
    render(<OAuthConsent />);
    await userEvent.click(
      await screen.findByRole("button", { name: /continue/i })
    );
    await userEvent.click(
      await screen.findByRole("button", { name: /save connector/i })
    );

    await waitFor(() => expect(approveAuthorization).toHaveBeenCalled());
    expect(approveAuthorization).toHaveBeenCalledWith("auth-1", {
      skipBrowserRedirect: true,
    });
  });

  it("denies without creating a connector", async () => {
    render(<OAuthConsent />);
    await userEvent.click(
      await screen.findByRole("button", { name: /cancel/i })
    );

    await waitFor(() => expect(denyAuthorization).toHaveBeenCalled());
    expect(authorizeClient).not.toHaveBeenCalled();
    expect(approveAuthorization).not.toHaveBeenCalled();
  });

  it("explains an expired authorization without blaming the connector", async () => {
    approveAuthorization.mockResolvedValue({
      data: null,
      error: { message: "authorization request has expired" },
    });

    render(<OAuthConsent />);
    await userEvent.click(
      await screen.findByRole("button", { name: /continue/i })
    );
    await userEvent.click(
      await screen.findByRole("button", { name: /save connector/i })
    );

    // The connector was saved; the sign-in is what ran out of time, and the
    // message has to say so or the user goes looking for the wrong problem.
    const msg = await screen.findByText(/timed out/i);
    expect(msg.textContent).toMatch(/connector was saved/i);
  });
});
