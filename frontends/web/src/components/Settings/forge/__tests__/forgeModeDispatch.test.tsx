/** In forge mode the account surfaces render the forge variant, and none of the Supabase calls. */
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ useAuthStore: vi.fn() }));

vi.mock("@/lib/auth/forge-api", () => ({ isForgeAuth: () => true }));
vi.mock("../ForgeAccountSettings", () => ({ ForgeAccountSettings: () => <div>forge account settings</div> }));
vi.mock("../ForgeLinkedAccounts", () => ({ ForgeLinkedAccounts: () => <div>forge linked accounts</div> }));
// Reading the Supabase user in forge mode is the defect: fail loudly if either does.
vi.mock("@/store/authStore", () => ({ useAuthStore: mocks.useAuthStore }));
vi.mock("../../../../store/authStore", () => ({ useAuthStore: mocks.useAuthStore }));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("../../DeleteAccountDialog", () => ({ DeleteAccountDialog: () => null }));

const { AccountSettings } = await import("../../AccountSettings");
const { LinkedAccounts } = await import("../../../LinkedAccounts");

describe("forge mode", () => {
  it("Settings › Account is the forge variant", () => {
    render(<AccountSettings />);
    expect(screen.getByText("forge account settings")).toBeInTheDocument();
    expect(mocks.useAuthStore).not.toHaveBeenCalled();
  });

  it("LinkedAccounts is the forge variant", () => {
    render(<LinkedAccounts />);
    expect(screen.getByText("forge linked accounts")).toBeInTheDocument();
    expect(mocks.useAuthStore).not.toHaveBeenCalled();
  });
});
