import { describe, expect, it } from "vitest";
import { boldSegments, providerNoticeKind } from "../providerNotice";

describe("providerNoticeKind", () => {
  it("recognises the account switch notices", () => {
    expect(
      providerNoticeKind("Claude account **a@x** reached its 5-hour limit (resets 3:40 PM). Continuing on **b@x**."),
    ).toBe("account_switch");
    expect(providerNoticeKind("Codex account **a@x** needs sign-in. Continuing on **b@x**.")).toBe("account_switch");
  });
  it("recognises the cross-provider switch and return", () => {
    expect(
      providerNoticeKind(
        "Claude is rate-limited on all your accounts until 3:40 PM. Continuing on **GPT-5.6 Sol · Codex** as this chat allows; returning to Claude after the reset.",
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
