import { act, render, screen } from "@testing-library/react";
import type { User } from "@supabase/supabase-js";
import { afterEach, describe, expect, it } from "vitest";

import { useSession } from "@/lib/session";
import { useAuthStore } from "@/store/authStore";
import { AuthStoreSession } from "../authStoreSession";

function Reader() {
  const { user, signedIn, loading } = useSession();
  return (
    <div
      data-testid="reader"
      data-user={user ? JSON.stringify(user) : ""}
      data-signed-in={String(signedIn)}
      data-loading={String(loading)}
    />
  );
}

function renderSession() {
  render(
    <AuthStoreSession>
      <Reader />
    </AuthStoreSession>,
  );
  return screen.getByTestId("reader");
}

afterEach(() => {
  act(() => useAuthStore.setState({ user: null, session: null, loading: false }));
});

const ADA = { id: "u1", email: "ada@example.com", user_metadata: { full_name: "Ada Lovelace" } } as unknown as User;

describe("the app's session", () => {
  it("is signed in exactly while the auth store has a user", () => {
    act(() => useAuthStore.setState({ user: null, loading: false }));
    const reader = renderSession();
    expect(reader).toHaveAttribute("data-signed-in", "false");
    expect(reader).toHaveAttribute("data-user", "");

    act(() => useAuthStore.setState({ user: ADA }));
    expect(reader).toHaveAttribute("data-signed-in", "true");
    expect(JSON.parse(reader.getAttribute("data-user") ?? "")).toEqual({ id: "u1", email: "ada@example.com", name: "Ada Lovelace" });

    act(() => useAuthStore.setState({ user: null }));
    expect(reader).toHaveAttribute("data-signed-in", "false");
  });

  it("is loading while the auth store is", () => {
    act(() => useAuthStore.setState({ loading: true }));
    expect(renderSession()).toHaveAttribute("data-loading", "true");
  });
});
