/** The mailed link that confirms a new address (/auth/account/email/confirm?code=…). */
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  confirmEmailChange: vi.fn(),
  navigate: vi.fn(),
  refreshSession: vi.fn(async () => {}),
}));

vi.mock("@/lib/auth/account", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/auth/account")>()),
  confirmEmailChange: mocks.confirmEmailChange,
}));
vi.mock("@/store/authStore", () => ({ useAuthStore: { getState: () => ({ refreshSession: mocks.refreshSession }) } }));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => mocks.navigate }));

const { AccountError } = await import("@/lib/auth/account");
const { ConfirmEmailChange } = await import("../ConfirmEmailChange");

beforeEach(() => {
  mocks.confirmEmailChange.mockReset();
  mocks.navigate.mockReset();
  mocks.refreshSession.mockClear();
});

describe("ConfirmEmailChange", () => {
  it("confirms the code on arrival, once", async () => {
    mocks.confirmEmailChange.mockResolvedValue({ email: "ada@new.example", emailVerified: true });
    render(<ConfirmEmailChange code="123456" />);
    expect(await screen.findByText("Your address is now ada@new.example.")).toBeInTheDocument();
    expect(mocks.confirmEmailChange).toHaveBeenCalledTimes(1);
    expect(mocks.confirmEmailChange).toHaveBeenCalledWith("123456");
    expect(mocks.refreshSession).toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Go to account settings" }));
    expect(mocks.navigate).toHaveBeenCalledWith({ to: "/settings/account" });
  });

  it("offers sign-in, coming back to the link, when signed out", async () => {
    mocks.confirmEmailChange.mockRejectedValue(new AccountError("Sign in first.", 401, "not_signed_in"));
    render(<ConfirmEmailChange code="123456" />);
    await userEvent.click(await screen.findByRole("button", { name: "Sign in" }));
    expect(mocks.navigate).toHaveBeenCalledWith({
      to: "/auth",
      search: { redirect: `${window.location.pathname}${window.location.search}` },
    });
  });

  it("shows a refused code with no retry", async () => {
    mocks.confirmEmailChange.mockRejectedValue(
      new AccountError("That code or link is not valid. Codes are single-use and expire; ask for a new one.", 401, "invalid_code"),
    );
    render(<ConfirmEmailChange code="bad" />);
    expect(await screen.findByText(/That code or link is not valid/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
  });

  it("says a link without a code is broken, and calls nothing", () => {
    render(<ConfirmEmailChange code="" />);
    expect(screen.getByText("This link has no code. Open the link from the email again.")).toBeInTheDocument();
    expect(mocks.confirmEmailChange).not.toHaveBeenCalled();
  });
});
