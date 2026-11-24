import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { fixedSession, SessionProvider, useSession, useSignedIn } from "..";

function Reader() {
  const { user, signedIn, loading } = useSession();
  const quick = useSignedIn();
  return (
    <div data-testid="reader" data-user={user?.id ?? ""} data-signed-in={String(signedIn)} data-loading={String(loading)} data-quick={String(quick)} />
  );
}

describe("the session port", () => {
  it("is signed out, and still loading, outside a provider", () => {
    render(<Reader />);
    const reader = screen.getByTestId("reader");
    expect(reader).toHaveAttribute("data-signed-in", "false");
    expect(reader).toHaveAttribute("data-quick", "false");
    expect(reader).toHaveAttribute("data-loading", "true");
    expect(reader).toHaveAttribute("data-user", "");
  });

  it("reads the auth it is given", () => {
    render(
      <SessionProvider auth={{ user: null, isAuthenticated: false, isLoading: false, getToken: async () => null }}>
        <Reader />
      </SessionProvider>,
    );
    expect(screen.getByTestId("reader")).toHaveAttribute("data-loading", "false");
    expect(screen.getByTestId("reader")).toHaveAttribute("data-signed-in", "false");
  });

  it("is the fixed user, signed in and settled, under fixedSession", async () => {
    const auth = fixedSession({ id: "visitor", email: "visitor@example.com" });
    render(
      <SessionProvider auth={auth}>
        <Reader />
      </SessionProvider>,
    );
    const reader = screen.getByTestId("reader");
    expect(reader).toHaveAttribute("data-user", "visitor");
    expect(reader).toHaveAttribute("data-signed-in", "true");
    expect(reader).toHaveAttribute("data-quick", "true");
    expect(reader).toHaveAttribute("data-loading", "false");
    await expect(auth.getToken()).resolves.toBeNull();
  });
});
