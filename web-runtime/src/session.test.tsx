// @vitest-environment jsdom
// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// fixedSession is the session of a bundle with no auth code (a demo, a
// preview, a scaffold's mock mode). These pin what such a bundle's shell
// reads through SessionProvider: signed in, never loading, no credential.
import { act, type ReactElement } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, describe, expect, it } from "vitest";

import {
  fixedSession,
  SessionProvider,
  useSession,
  useSignedIn,
  type Session,
  type SessionAuth,
} from "./session.js";

(
  globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

const cleanups: Array<() => void> = [];
afterEach(() => {
  for (const cleanup of cleanups.splice(0)) cleanup();
});

// render mounts the element and lets SessionProvider's token effect settle.
async function render(element: ReactElement): Promise<void> {
  const container = document.createElement("div");
  const root = createRoot(container);
  cleanups.push(() => act(() => root.unmount()));
  await act(async () => {
    root.render(element);
  });
}

// readSession renders a probe under `auth` and returns what it read last.
async function readSession(
  auth: SessionAuth | null,
): Promise<{ session: Session; signedIn: boolean }> {
  const last: { seen?: { session: Session; signedIn: boolean } } = {};
  function Probe(): null {
    last.seen = { session: useSession(), signedIn: useSignedIn() };
    return null;
  }
  await render(
    auth ? (
      <SessionProvider auth={auth}>
        <Probe />
      </SessionProvider>
    ) : (
      <Probe />
    ),
  );
  if (!last.seen) throw new Error("the probe never rendered");
  return last.seen;
}

// A JWT with no signature check needed: decodeJwt reads the payload only.
function unsignedJwt(claims: Record<string, unknown>): string {
  const b64 = (s: string) =>
    btoa(s).replace(/=+$/, "").replace(/\+/g, "-").replace(/\//g, "_");
  return `${b64('{"alg":"none"}')}.${b64(JSON.stringify(claims))}.sig`;
}

describe("fixedSession", () => {
  it("is signed in as the user, never loading, and holds no token", async () => {
    const auth = fixedSession({
      id: "dev",
      email: "dev@localhost",
      name: "Dev User",
    });

    expect(auth.user).toEqual({
      id: "dev",
      email: "dev@localhost",
      name: "Dev User",
    });
    expect(auth.isAuthenticated).toBe(true);
    expect(auth.isLoading).toBe(false);
    await expect(auth.getToken()).resolves.toBeNull();
  });

  it("resolves the token it was given", async () => {
    const auth = fixedSession({ id: "dev" }, { token: "mock-token-dev" });

    await expect(auth.getToken()).resolves.toBe("mock-token-dev");
    await expect(auth.getToken()).resolves.toBe("mock-token-dev");
  });

  it("is stable: frozen, a copy of the user, and one getToken", () => {
    const user = { id: "dev", email: "dev@localhost" };
    const auth = fixedSession(user);
    user.email = "changed@example.com";

    expect(auth.user?.email).toBe("dev@localhost");
    expect(Object.isFrozen(auth)).toBe(true);
    expect(Object.isFrozen(auth.user)).toBe(true);
    expect(auth.getToken).toBe(auth.getToken);
  });
});

describe("SessionProvider with a fixedSession", () => {
  it("reports the fixed user as signed in, with no claims", async () => {
    const { session, signedIn } = await readSession(
      fixedSession({ id: "dev", email: "dev@localhost", name: "Dev User" }),
    );

    expect(session).toEqual({
      userId: "dev",
      email: "dev@localhost",
      name: "Dev User",
      claims: null,
      isAuthenticated: true,
      isLoading: false,
    });
    expect(signedIn).toBe(true);
  });

  it("decodes the claims of a token it was given", async () => {
    const { session } = await readSession(
      fixedSession(
        { id: "dev" },
        { token: unsignedJwt({ sub: "dev", org: "acme" }) },
      ),
    );

    expect(session.claims).toEqual({ sub: "dev", org: "acme" });
  });
});

describe("useSignedIn", () => {
  it("is false outside a SessionProvider", async () => {
    const { session, signedIn } = await readSession(null);

    expect(signedIn).toBe(false);
    expect(session.isAuthenticated).toBe(false);
  });

  it("follows isAuthenticated, not the presence of a user", async () => {
    const { signedIn } = await readSession({
      user: null,
      isAuthenticated: false,
      isLoading: false,
      getToken: () => Promise.resolve(null),
    });

    expect(signedIn).toBe(false);
  });
});
