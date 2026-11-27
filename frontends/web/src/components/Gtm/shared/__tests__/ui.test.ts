// Copyright (c) 2026 Reliant Labs

/** Channel colours (deterministic, distinct) and which written URLs become links. */

import { describe, expect, it } from "vitest";

import { channelColor, externalHref } from "../ui";

describe("channelColor", () => {
  const org = ["blog", "email", "outreach", "dev-forums", "podcast-guests"].map((key) => ({ key }));

  it("keeps a default channel's own colour", () => {
    expect(channelColor({ key: "blog" }, org)).toBe("#6366f1");
    expect(channelColor({ key: "email" })).toBe("#0ea5e9");
  });

  it("gives the org's other channels distinct colours, the same every time", () => {
    const colours = ["outreach", "dev-forums", "podcast-guests"].map((key) => channelColor({ key }, org));
    expect(new Set(colours).size).toBe(3);
    expect(colours.some((c) => ["#6366f1", "#0ea5e9", "#64748b", "#10b981", "#f59e0b", "#f97316", "#ef4444"].includes(c))).toBe(false);
    // The order the list comes in does not matter.
    expect(["outreach", "dev-forums", "podcast-guests"].map((key) => channelColor({ key }, [...org].reverse()))).toEqual(colours);
  });

  it("still picks a stable colour without the org's channels", () => {
    expect(channelColor({ key: "outreach" })).toBe(channelColor({ key: "outreach" }));
  });
});

describe("externalHref", () => {
  it("links only http(s)", () => {
    expect(externalHref(" https://example.com/a ")).toBe("https://example.com/a");
    expect(externalHref("http://example.com")).toBe("http://example.com");
    expect(externalHref("javascript:alert(1)")).toBeNull();
    expect(externalHref("data:text/html,hi")).toBeNull();
    expect(externalHref("example.com")).toBeNull();
  });
});
