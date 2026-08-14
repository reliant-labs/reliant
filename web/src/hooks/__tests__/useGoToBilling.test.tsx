/**
 * useGoToBilling — where "View plans" actually sends people.
 *
 * The case that matters is the ANONYMOUS one: free-tier users run on anonymous
 * Supabase sessions, and sending them straight to billing is a dead end. A
 * subscription bought against a browser-session identity belongs to nobody
 * reachable, and losing the session loses the purchase. They have to link a
 * real identity first, which is what /upgrade does — with returnTo pointing at
 * billing so they still arrive where they meant to go.
 */
import { renderHook, act } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mockNavigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => mockNavigate,
}));

type MockUser = { is_anonymous?: boolean } | null;
let mockUser: MockUser = null;

vi.mock("@/store/authStore", () => ({
  useAuthStore: (selector: (s: { user: MockUser }) => unknown) =>
    selector({ user: mockUser }),
}));

import { useGoToBilling } from "../useGoToBilling";

beforeEach(() => {
  vi.clearAllMocks();
  mockUser = null;
});

describe("useGoToBilling", () => {
  it("sends an anonymous user through identity linking, returning to billing", () => {
    mockUser = { is_anonymous: true };

    const { result } = renderHook(() => useGoToBilling());
    act(() => result.current());

    expect(mockNavigate).toHaveBeenCalledWith({
      to: "/upgrade",
      search: { returnTo: "/settings/billing" },
    });
  });

  it("sends a signed-in user straight to billing", () => {
    mockUser = { is_anonymous: false };

    const { result } = renderHook(() => useGoToBilling());
    act(() => result.current());

    expect(mockNavigate).toHaveBeenCalledWith({
      to: "/settings/$section",
      params: { section: "billing" },
    });
  });

  // api-key / mock / dev synthetic users set is_anonymous: false and must not
  // be pushed through identity linking they cannot complete. Same rule as
  // useAnonSignInNudge: ONLY is_anonymous === true counts as anonymous.
  it("treats a user with no is_anonymous flag as signed in", () => {
    mockUser = {};

    const { result } = renderHook(() => useGoToBilling());
    act(() => result.current());

    expect(mockNavigate).toHaveBeenCalledWith({
      to: "/settings/$section",
      params: { section: "billing" },
    });
  });

  it("sends a session with no user at all to billing rather than dead-ending", () => {
    mockUser = null;

    const { result } = renderHook(() => useGoToBilling());
    act(() => result.current());

    expect(mockNavigate).toHaveBeenCalledWith({
      to: "/settings/$section",
      params: { section: "billing" },
    });
  });
});
