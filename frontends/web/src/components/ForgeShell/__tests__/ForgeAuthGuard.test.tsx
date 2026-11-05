import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

const navigate = vi.fn();
let href = "/forge/p/storefront/releases?env=prod";
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
  useRouterState: ({ select }: { select: (s: { location: { href: string } }) => unknown }) => select({ location: { href } }),
}));

type TestUser = { id: string; is_anonymous?: boolean; email_confirmed_at?: string | null } | null;
const auth = { user: null as TestUser, loading: false, initialized: true, initialize: vi.fn() };
vi.mock("@/store/authStore", () => ({
  useAuthStore: (selector: (s: typeof auth) => unknown) => selector(auth),
}));

vi.mock("@/components/Layout/LoadingSpinner", () => ({ LoadingSpinner: () => <div data-testid="spinner" /> }));

import { ForgeAuthGuard, forgeAuthDecision, forgeSignInHref } from "../ForgeAuthGuard";

/**
 * The forge shell's guard asks one thing: is someone signed in. No onboarding,
 * no daemon, no machine — and a deep link survives the sign-in.
 */

beforeEach(() => {
  vi.clearAllMocks();
  auth.user = null;
  auth.loading = false;
  auth.initialized = true;
  href = "/forge/p/storefront/releases?env=prod";
});

describe("forgeAuthDecision", () => {
  it.each([
    [{ initialized: false, loading: false, user: null }, "wait"],
    [{ initialized: true, loading: true, user: null }, "wait"],
    [{ initialized: true, loading: false, user: null }, "sign-in"],
    [{ initialized: true, loading: false, user: { email_confirmed_at: null } }, "verify-email"],
    [{ initialized: true, loading: false, user: { email_confirmed_at: "2026-01-01" } }, "allow"],
    // A claim in flight holds an anonymous session: it is let in, as in Reliant.
    [{ initialized: true, loading: false, user: { is_anonymous: true } }, "allow"],
  ] as const)("%j → %s", (state, expected) => {
    expect(forgeAuthDecision(state)).toBe(expected);
  });
});

describe("forgeSignInHref", () => {
  it("returns to the page that was asked for", () => {
    expect(forgeSignInHref("/forge/p/storefront/releases?env=prod")).toBe(
      "/auth?redirect=%2Fforge%2Fp%2Fstorefront%2Freleases%3Fenv%3Dprod",
    );
  });

  it("needs no return for the home page, and refuses an off-origin one", () => {
    expect(forgeSignInHref("/")).toBe("/auth");
    expect(forgeSignInHref("//evil.test/x")).toBe("/auth");
    expect(forgeSignInHref("https://evil.test")).toBe("/auth");
  });
});

describe("ForgeAuthGuard", () => {
  it("sends a signed-out visitor to sign in, keeping the deep link", () => {
    render(<ForgeAuthGuard><p>console</p></ForgeAuthGuard>);
    expect(screen.queryByText("console")).not.toBeInTheDocument();
    expect(navigate).toHaveBeenCalledWith({
      href: "/auth?redirect=%2Fforge%2Fp%2Fstorefront%2Freleases%3Fenv%3Dprod",
      replace: true,
    });
  });

  it("renders the page for a signed-in user, with no other gate", () => {
    auth.user = { id: "u1", email_confirmed_at: "2026-01-01" };
    render(<ForgeAuthGuard><p>console</p></ForgeAuthGuard>);
    expect(screen.getByText("console")).toBeInTheDocument();
    expect(navigate).not.toHaveBeenCalled();
  });

  it("waits while auth initialises, and starts it", () => {
    auth.initialized = false;
    render(<ForgeAuthGuard><p>console</p></ForgeAuthGuard>);
    expect(screen.getByTestId("spinner")).toBeInTheDocument();
    expect(auth.initialize).toHaveBeenCalled();
    expect(navigate).not.toHaveBeenCalled();
  });

  it("sends an unconfirmed email account to verify it", () => {
    auth.user = { id: "u1", email_confirmed_at: null };
    render(<ForgeAuthGuard><p>console</p></ForgeAuthGuard>);
    expect(navigate).toHaveBeenCalledWith({ href: "/verify-email", replace: true });
  });
});
