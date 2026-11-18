// @vitest-environment node
import { beforeEach, describe, expect, it } from "vitest";

import {
  beginSessionTransition,
  credentialBelongsTo,
  currentSessionOwner,
  isCurrentSessionOwner,
  resetSessionOwnerForTests,
  setSessionOwner,
} from "../sessionOwner";

// A JWT whose `sub` is `subject` (unsigned: only the claims are read).
const jwt = (subject: string) => `h.${btoa(JSON.stringify({ sub: subject }))}.s`;

describe("sessionOwner", () => {
  beforeEach(() => resetSessionOwnerForTests());

  it("never lets work captured with nobody send, with an opaque key or a session token", () => {
    const nobody = currentSessionOwner();
    expect(nobody.userId).toBeNull();
    expect(credentialBelongsTo(nobody, "rk_opaque_key")).toBe(false);
    expect(credentialBelongsTo(nobody, jwt("anyone"))).toBe(false);
    expect(isCurrentSessionOwner(nobody)).toBe(false);
  });

  it("makes the page act for nobody while an identity change is in progress, even from a key to a key", () => {
    setSessionOwner("apikey-user");
    const a = currentSessionOwner();
    expect(credentialBelongsTo(a, "rk_key_a")).toBe(true);

    beginSessionTransition();
    const during = currentSessionOwner();
    // Neither the owner captured before the change nor one captured during it sends.
    expect(credentialBelongsTo(a, "rk_key_b")).toBe(false);
    expect(credentialBelongsTo(during, "rk_key_b")).toBe(false);
    expect(isCurrentSessionOwner(during)).toBe(false);

    // The same synthetic id installed again is a new owner: A's work stays stale.
    setSessionOwner("apikey-user");
    expect(credentialBelongsTo(a, "rk_key_b")).toBe(false);
    expect(credentialBelongsTo(during, "rk_key_b")).toBe(false);
    expect(credentialBelongsTo(currentSessionOwner(), "rk_key_b")).toBe(true);
  });

  it("still judges a session token by its subject", () => {
    setSessionOwner("alice");
    const alice = currentSessionOwner();
    expect(credentialBelongsTo(alice, jwt("alice"))).toBe(true);
    expect(credentialBelongsTo(alice, jwt("bob"))).toBe(false);
  });
});
