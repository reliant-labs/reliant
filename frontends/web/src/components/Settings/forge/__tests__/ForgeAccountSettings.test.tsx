/** Settings › Account in forge mode, over a mocked broker client (lib/auth/account). */
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Account } from "@/lib/auth/account";

const mocks = vi.hoisted(() => ({
  fetchAccount: vi.fn(),
  reauthenticate: vi.fn(),
  unlinkSocial: vi.fn(),
  removePasskey: vi.fn(),
  revokeSession: vi.fn(),
  revokeOtherSessions: vi.fn(),
  changePassword: vi.fn(),
  changeEmail: vi.fn(),
  confirmEmailChange: vi.fn(),
  startLink: vi.fn(),
  signOut: vi.fn(async () => {}),
  navigate: vi.fn(),
  refreshSession: vi.fn(async () => {}),
}));

vi.mock("@/lib/auth/account", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/auth/account")>();
  return {
    ...actual,
    fetchAccount: mocks.fetchAccount,
    reauthenticate: mocks.reauthenticate,
    unlinkSocial: mocks.unlinkSocial,
    removePasskey: mocks.removePasskey,
    revokeSession: mocks.revokeSession,
    revokeOtherSessions: mocks.revokeOtherSessions,
    changePassword: mocks.changePassword,
    changeEmail: mocks.changeEmail,
    confirmEmailChange: mocks.confirmEmailChange,
    startLink: mocks.startLink,
  };
});
vi.mock("@/lib/auth/native-login", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/auth/native-login")>();
  return {
    ...actual,
    fetchAuthOptions: vi.fn(async () => ({ ...actual.LEGACY_AUTH_OPTIONS, methods: ["password", "passkey"] })),
    fetchPasswordPolicy: vi.fn(async () => null),
    passkeysSupported: () => false,
  };
});
vi.mock("@/lib/auth/use-auth", () => ({
  useAuth: () => ({
    identity: { id: "u1", email: "ada@example.com", name: "Ada L" },
    signOut: mocks.signOut,
  }),
}));
vi.mock("@/store/authStore", () => ({ useAuthStore: { getState: () => ({ refreshSession: mocks.refreshSession }) } }));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => mocks.navigate }));
vi.mock("../../DeleteAccountDialog", () => ({ DeleteAccountDialog: () => null }));

const { AccountError } = await import("@/lib/auth/account");
const { ForgeAccountSettings } = await import("../ForgeAccountSettings");

function anAccount(overrides: Partial<Account> = {}): Account {
  return {
    subject: "u1",
    email: "ada@example.com",
    emailVerified: true,
    hasPassword: true,
    links: [{ provider: "github", label: "GitHub", userName: "ada-gh" }],
    social: [
      { type: "github", label: "GitHub", connected: true },
      { type: "google", label: "Google", connected: false },
    ],
    passkeys: [{ id: "pk1", name: "Laptop" }],
    sessions: [
      { handle: "h-current", userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/120.0 Safari/537.36", createdAt: "", lastSeenAt: "2026-11-20T00:00:00Z", expiresAt: "", current: true },
      { handle: "h-phone", userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Version/17.0 Mobile/15E148 Safari/604.1", createdAt: "", lastSeenAt: "2026-11-19T00:00:00Z", expiresAt: "", current: false },
    ],
    provedAt: "",
    reauthRequired: true,
    ...overrides,
  };
}

const reauthRequired = (methods: ("password" | "sign_in")[]) =>
  new AccountError("Confirm it's you first.", 401, "reauth_required", { methods });

beforeEach(() => {
  for (const m of Object.values(mocks)) m.mockReset();
  mocks.signOut.mockResolvedValue(undefined);
  mocks.refreshSession.mockResolvedValue(undefined);
  mocks.fetchAccount.mockResolvedValue(anAccount());
});

describe("ForgeAccountSettings", () => {
  it("shows the account: address, connected accounts, passkeys and sessions", async () => {
    render(<ForgeAccountSettings />);
    const email = await screen.findByTestId("forge-account-email");
    expect(within(email).getByText("ada@example.com")).toBeInTheDocument();
    expect(within(email).getByText("Verified")).toBeInTheDocument();

    const connected = screen.getByTestId("forge-account-connected");
    expect(within(connected).getByText("ada-gh")).toBeInTheDocument();
    expect(within(connected).getByRole("button", { name: "Disconnect GitHub" })).toBeInTheDocument();
    expect(within(connected).getByRole("button", { name: "Connect Google" })).toBeInTheDocument();

    expect(within(screen.getByTestId("forge-account-passkeys")).getByText("Laptop")).toBeInTheDocument();

    const sessions = screen.getByTestId("forge-account-sessions");
    expect(within(sessions).getByText("This device")).toBeInTheDocument();
    expect(within(sessions).getByText("Chrome on macOS")).toBeInTheDocument();
    // This device has no Sign out of its own; the other one does.
    expect(within(sessions).getAllByRole("button", { name: /^Sign out Safari on iOS$/ })).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Change password" })).toBeInTheDocument();
  });

  it("asks for the password when a change needs a recent proof, then retries it", async () => {
    mocks.unlinkSocial.mockRejectedValueOnce(reauthRequired(["password", "sign_in"])).mockResolvedValueOnce(undefined);
    mocks.reauthenticate.mockResolvedValue(undefined);
    render(<ForgeAccountSettings />);
    await userEvent.click(await screen.findByRole("button", { name: "Disconnect GitHub" }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Confirm it's you")).toBeInTheDocument();
    await userEvent.type(within(dialog).getByLabelText("Password"), "hunter22");
    await userEvent.click(within(dialog).getByRole("button", { name: "Continue" }));

    await waitFor(() => expect(mocks.unlinkSocial).toHaveBeenCalledTimes(2));
    expect(mocks.reauthenticate).toHaveBeenCalledWith("hunter22");
    expect(mocks.unlinkSocial).toHaveBeenCalledWith("github");
    expect(await screen.findByText("GitHub disconnected.")).toBeInTheDocument();
    // The page is re-read after the change.
    await waitFor(() => expect(mocks.fetchAccount).toHaveBeenCalledTimes(2));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("shows a wrong password in the dialog and does not retry", async () => {
    mocks.unlinkSocial.mockRejectedValueOnce(reauthRequired(["password", "sign_in"]));
    mocks.reauthenticate.mockRejectedValue(new AccountError("That password was not accepted.", 401, "wrong_password"));
    render(<ForgeAccountSettings />);
    await userEvent.click(await screen.findByRole("button", { name: "Disconnect GitHub" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText("Password"), "nope");
    await userEvent.click(within(dialog).getByRole("button", { name: "Continue" }));
    expect(await within(dialog).findByText("That password was not accepted.")).toBeInTheDocument();
    expect(mocks.unlinkSocial).toHaveBeenCalledTimes(1);
  });

  it("offers signing in again to an account with no password", async () => {
    mocks.fetchAccount.mockResolvedValue(anAccount({ hasPassword: false }));
    mocks.revokeOtherSessions.mockRejectedValueOnce(reauthRequired(["sign_in"]));
    render(<ForgeAccountSettings />);
    await userEvent.click(await screen.findByRole("button", { name: "Sign out other devices" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).queryByLabelText("Password")).not.toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Sign in again" }));
    await waitFor(() => expect(mocks.signOut).toHaveBeenCalledOnce());
    expect(mocks.navigate).toHaveBeenCalledWith({ to: "/auth", search: { redirect: "/settings/account" } });
  });

  it("says so when removing the last way in is refused", async () => {
    mocks.removePasskey.mockRejectedValue(
      new AccountError("This is your only way to sign in. Add another first.", 409, "last_credential"),
    );
    render(<ForgeAccountSettings />);
    await userEvent.click(await screen.findByRole("button", { name: "Remove Laptop" }));
    await userEvent.click(screen.getByRole("button", { name: "Remove" }));
    expect(await screen.findByText("This is your only way to sign in. Add another first.")).toBeInTheDocument();
    expect(mocks.removePasskey).toHaveBeenCalledWith("pk1");
  });

  it("signs one other device out", async () => {
    mocks.revokeSession.mockResolvedValue(undefined);
    render(<ForgeAccountSettings />);
    await userEvent.click(await screen.findByRole("button", { name: "Sign out Safari on iOS" }));
    await waitFor(() => expect(mocks.revokeSession).toHaveBeenCalledWith("h-phone"));
    expect(await screen.findByText("Safari on iOS signed out.")).toBeInTheDocument();
  });

  it("finishes a password change whose other sessions were not signed out", async () => {
    mocks.changePassword.mockRejectedValue(
      new AccountError("Your password changed, but other devices could not be signed out.", 502, "sessions_not_revoked", {
        changed: true,
        retry: "/auth/account/sessions/revoke-others",
      }),
    );
    mocks.revokeOtherSessions.mockResolvedValue(1);
    render(<ForgeAccountSettings />);
    await userEvent.click(await screen.findByRole("button", { name: "Change password" }));
    await userEvent.type(screen.getByLabelText("Current password"), "old-password");
    await userEvent.type(screen.getByLabelText("New password"), "new-password-1");
    await userEvent.click(within(screen.getByTestId("forge-account-password")).getByRole("button", { name: "Change password" }));

    expect(mocks.changePassword).toHaveBeenCalledWith({ currentPassword: "old-password", newPassword: "new-password-1" });
    const password = screen.getByTestId("forge-account-password");
    await userEvent.click(await within(password).findByRole("button", { name: "Sign out other devices" }));
    await waitFor(() => expect(mocks.revokeOtherSessions).toHaveBeenCalledOnce());
    expect(await within(password).findByText("Password changed. One other device was signed out.")).toBeInTheDocument();
  });

  it("changes the address with a code sent to the new one", async () => {
    mocks.changeEmail.mockResolvedValue({ message: "Check your email: we sent a code to the new address." });
    mocks.confirmEmailChange.mockResolvedValue({ email: "ada@new.example", emailVerified: true });
    render(<ForgeAccountSettings />);
    const email = await screen.findByTestId("forge-account-email");
    await userEvent.click(within(email).getByRole("button", { name: "Change" }));
    await userEvent.type(within(email).getByLabelText("New address"), "ada@new.example");
    await userEvent.click(within(email).getByRole("button", { name: "Send code" }));
    expect(mocks.changeEmail).toHaveBeenCalledWith("ada@new.example");
    await userEvent.type(await within(email).findByLabelText("Code from the email"), "123456");
    await userEvent.click(within(email).getByRole("button", { name: "Confirm" }));
    expect(mocks.confirmEmailChange).toHaveBeenCalledWith("123456");
    expect(await within(email).findByText("Your address is now ada@new.example.")).toBeInTheDocument();
    // The signed-in identity is re-read so the app shows the new address.
    expect(mocks.refreshSession).toHaveBeenCalled();
  });

  it("explains an ended session instead of an empty page", async () => {
    mocks.fetchAccount.mockRejectedValue(new AccountError("Sign in first.", 401, "not_signed_in"));
    render(<ForgeAccountSettings />);
    expect(await screen.findByText("Your session has ended. Sign in again to manage your account.")).toBeInTheDocument();
  });
});
