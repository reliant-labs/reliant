import { describe, expect, it } from "vitest";
import type { ProviderAccount, UsageWindow } from "../../types/providerAccount";
import { accountChipState, accountChipText, shortAccountLabel } from "../accountUsageChip";

const NOW = new Date("2026-10-18T12:00:00Z");
const iso = (min: number) => new Date(NOW.getTime() + min * 60_000).toISOString();

const win = (name: string, used: number | undefined, resetMin = 120, label = name): UsageWindow => ({
  name,
  label,
  usedFraction: used,
  resetsAt: iso(resetMin),
  lengthSeconds: name === "5h" ? 18_000 : 604_800,
});

function acct(p: Partial<ProviderAccount> & { id: string }): ProviderAccount {
  return {
    provider: "claude",
    providerDisplayName: "Claude subscription",
    authKind: "oauth",
    label: `${p.id}@acme.com`,
    priority: 0,
    status: "available",
    windows: [],
    usageSource: "headers",
    createdAt: "2026-10-01T00:00:00Z",
    usageObservedAt: iso(-1),
    ...p,
  };
}

describe("accountChipState", () => {
  it("is hidden below 80%", () => {
    const a = acct({ id: "alice", windows: [win("5h", 0.79), win("7d", 0.3, 5000)] });
    expect(accountChipState(a, [a], NOW)).toBeNull();
  });

  it("is hidden when usage is unknown or the account has no windows", () => {
    expect(accountChipState(acct({ id: "a", windows: [win("5h", undefined)] }), [], NOW)).toBeNull();
    expect(accountChipState(acct({ id: "a" }), [], NOW)).toBeNull();
  });

  it("shows the fullest window from 80%", () => {
    const a = acct({ id: "alice", windows: [win("5h", 0.82), win("7d", 0.5, 5000)] });
    expect(accountChipState(a, [a], NOW)).toEqual({ kind: "usage", percent: 82, windowLabel: "5h" });
  });

  it("picks the weekly window when it is the fuller one", () => {
    const a = acct({ id: "alice", windows: [win("5h", 0.3), win("7d", 0.91, 5000, "Weekly")] });
    expect(accountChipState(a, [a], NOW)).toMatchObject({ percent: 91, windowLabel: "7d" });
  });

  it("ignores a model-scoped window", () => {
    const a = acct({ id: "alice", windows: [win("5h", 0.2), win("7d_opus", 0.99, 5000)] });
    expect(accountChipState(a, [a], NOW)).toBeNull();
  });

  it("does not keep showing a window that has already reset", () => {
    const a = acct({ id: "alice", windows: [win("5h", 0.95, -10)] });
    expect(accountChipState(a, [a], NOW)).toBeNull();
  });

  it("limited with a sibling that can serve names the next account", () => {
    const a = acct({ id: "alice", status: "limited", limitedUntil: iso(100), priority: 0 });
    const paused = acct({ id: "pat", status: "paused", priority: 1 });
    const bob = acct({ id: "bob", priority: 2 });
    expect(accountChipState(a, [a, paused, bob], NOW)).toEqual({ kind: "limited_next", next: bob });
  });

  it("limited with no sibling reads limited-until", () => {
    const a = acct({ id: "alice", status: "limited", limitedUntil: iso(100) });
    const other = acct({ id: "bob", status: "limited", limitedUntil: iso(50), priority: 1 });
    const codex = acct({ id: "cx", provider: "codex", priority: 2 });
    expect(accountChipState(a, [a, other, codex], NOW)).toEqual({ kind: "limited_until" });
  });

  it("treats a bench whose end has passed as served", () => {
    const a = acct({ id: "alice", status: "limited", limitedUntil: iso(-1), windows: [win("5h", 0.4)] });
    expect(accountChipState(a, [a], NOW)).toBeNull();
  });
});

describe("accountChipText", () => {
  const alice = acct({ id: "alice", label: "alice@acme.com", windows: [win("5h", 0.82)] });
  const bob = acct({ id: "bob", label: "bob@acme.com", priority: 1 });

  it("usage: percent, window, short account", () => {
    const t = accountChipText({ kind: "usage", percent: 82, windowLabel: "5h" }, alice, [alice, bob], NOW, false);
    expect(t.label).toBe("82% of 5h · alice@");
    expect(t.full).toContain("alice@acme.com");
  });

  it("limited with a next account", () => {
    const t = accountChipText({ kind: "limited_next", next: bob }, alice, [alice, bob], NOW, false);
    expect(t.label).toBe("Limited · next account bob@");
  });

  it("limited until the reset time", () => {
    const limited = acct({ id: "alice", status: "limited", limitedUntil: "2026-10-18T15:40:00" });
    const t = accountChipText({ kind: "limited_until" }, limited, [limited], new Date("2026-10-18T12:00:00"), false);
    expect(t.label).toMatch(/^Limited until 3:40\s?PM$/);
  });

  it("compact drops the account name", () => {
    expect(
      accountChipText({ kind: "usage", percent: 82, windowLabel: "5h" }, alice, [alice], NOW, true).label,
    ).toBe("82% of 5h");
    expect(accountChipText({ kind: "limited_next", next: bob }, alice, [alice, bob], NOW, true).label).toBe(
      "Limited · next account",
    );
  });
});

describe("shortAccountLabel", () => {
  it("keeps the whole label when the short form is ambiguous", () => {
    const a = acct({ id: "a", label: "sam@acme.com" });
    const b = acct({ id: "b", label: "sam@home.org" });
    expect(shortAccountLabel(a, [a, b])).toBe("sam@acme.com");
    expect(shortAccountLabel(a, [a])).toBe("sam@");
  });
  it("leaves a non-email label alone", () => {
    expect(shortAccountLabel(acct({ id: "k", label: "sk-…a1b2" }), [])).toBe("sk-…a1b2");
  });
});
