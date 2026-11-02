/** Signing out loses nothing, so it asks nothing and is not a red button. */
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ signOut: vi.fn(async () => {}), navigate: vi.fn() }));
vi.mock("../../../store/authStore", () => ({
  useAuthStore: () => ({
    user: { email: "alex@example.com", identities: [] },
    signOut: mocks.signOut,
    authError: null,
    clearAuthError: vi.fn(),
  }),
}));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => mocks.navigate }));
vi.mock("../../LinkedAccounts", () => ({ LinkedAccounts: () => null }));
vi.mock("../DeleteAccountDialog", () => ({ DeleteAccountDialog: () => null }));

const { AccountSettings } = await import("../AccountSettings");

describe("Account → Sign out", () => {
  it("signs out at once, with no confirm()", async () => {
    const confirm = vi.spyOn(window, "confirm");
    render(<AccountSettings />);
    const button = screen.getByRole("button", { name: "Sign out" });
    // Not styled like Delete account, the destructive action below it.
    expect(button.className).not.toMatch(/danger/);
    await userEvent.click(button);
    expect(confirm).not.toHaveBeenCalled();
    expect(mocks.signOut).toHaveBeenCalledOnce();
    expect(mocks.navigate).toHaveBeenCalledWith({ to: "/auth", search: { redirect: undefined } });
  });
});
