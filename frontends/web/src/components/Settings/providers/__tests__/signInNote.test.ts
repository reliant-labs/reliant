import { describe, expect, it } from "vitest";
import { signInNote } from "../AddAccountButton";

// What the connect panel tells the user after a sign-in that did not do what they expected.
describe("signInNote", () => {
  it("says nothing when a new account was added", () => {
    expect(signInNote("claude", "add", { created: true })).toBeNull();
  });

  it("says nothing when a reconnect refreshed its own account", () => {
    expect(signInNote("claude", "reconnect", { created: false })).toBeNull();
  });

  it("explains that the browser re-authorized an account that was already connected", () => {
    const note = signInNote("claude", "add", { created: false, accountLabel: "alice@example.com" });
    expect(note?.ok).toBe(true);
    expect(note?.message).toContain("That's alice@example.com, already connected.");
    expect(note?.message).toContain("sign out of claude.ai");
    expect(note?.message).toContain("private window");
  });

  it("names the site to sign out of per provider", () => {
    expect(signInNote("codex", "add", { created: false })?.message).toContain("chatgpt.com");
    expect(signInNote("antigravity", "add", { created: false })?.message).toContain("accounts.google.com");
    expect(signInNote("copilot", "add", { created: false })?.message).toContain("github.com");
  });

  it("surfaces an identity mismatch as the failure it is, whatever the mode", () => {
    const msg = "You signed in as bob@example.com, not alice@example.com — saved as a separate account";
    for (const mode of ["add", "reconnect"] as const) {
      expect(signInNote("claude", mode, { created: true, identityMismatch: msg })).toEqual({ ok: false, message: msg });
    }
  });
});
