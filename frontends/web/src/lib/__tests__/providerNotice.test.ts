import { describe, expect, it } from "vitest";
import { boldSegments, localizeNoticeTimes, providerNoticeKind } from "../providerNotice";

describe("providerNoticeKind", () => {
  it("recognises the account switch notices", () => {
    expect(
      providerNoticeKind("Claude account **a@x** reached its 5-hour limit (resets <time:2026-10-19T15:40:00Z>). Continuing on **b@x**."),
    ).toBe("account_switch");
    expect(providerNoticeKind("Codex account **a@x** needs sign-in. Continuing on **b@x**.")).toBe("account_switch");
  });
  it("recognises the cross-provider switch and return", () => {
    expect(
      providerNoticeKind(
        "Claude is rate-limited on all your accounts until <time:2026-10-19T15:40:00Z>. Continuing on **GPT-5.6 Sol · Codex** as this chat allows; returning to Claude after the reset.",
      ),
    ).toBe("provider_switch");
    expect(providerNoticeKind("Claude's limit has reset. Back on **Claude 5.5 Opus · Claude**.")).toBe("return");
  });
  it("leaves other warnings alone", () => {
    expect(providerNoticeKind("Max turns reached.")).toBeUndefined();
    expect(providerNoticeKind("Model **x** is unavailable; using **y**.")).toBeUndefined();
  });
});

describe("boldSegments", () => {
  it("splits on bold markers", () => {
    expect(boldSegments("on **b@x**.")).toEqual([
      { text: "on ", bold: false },
      { text: "b@x", bold: true },
      { text: ".", bold: false },
    ]);
  });
  it("keeps an unmatched marker literal", () => {
    expect(boldSegments("a ** b")).toEqual([{ text: "a ** b", bold: false }]);
  });
});

describe("localizeNoticeTimes", () => {
  const token = "<time:2026-10-19T15:40:00Z>";
  const now = Date.parse("2026-10-19T12:00:00Z");

  it("renders the instant in the reader's zone, whatever zone the server wrote it in", () => {
    const expected = new Date("2026-10-19T15:40:00Z").toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
    expect(localizeNoticeTimes(`resets ${token}.`, now)).toBe(`resets ${expected}.`);
    expect(localizeNoticeTimes(`resets ${token}.`, now)).not.toContain("<time:");
  });

  it("adds the weekday once the time is not today", () => {
    const later = Date.parse("2026-10-21T12:00:00Z");
    expect(localizeNoticeTimes(`until ${token}`, later)).toMatch(/^until [A-Z][a-z]{2} /);
  });

  it("handles several tokens and leaves an unparseable one as written", () => {
    const out = localizeNoticeTimes(`${token} and <time:not-a-time> and ${token}`, now);
    expect(out).toContain("<time:not-a-time>");
    expect(out.match(/<time:/g)).toHaveLength(1);
  });

  it("leaves text without a token alone", () => {
    expect(localizeNoticeTimes("Max turns reached.", now)).toBe("Max turns reached.");
  });
});
