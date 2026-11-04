// Copyright (c) 2026 Reliant Labs
import { describe, expect, it } from "vitest";
import { askWorkspaceName, buildAskReliantPrompt, type AskSubject } from "../../AskReliant/askReliantPrompt";
const subject: Extract<AskSubject, { kind: "graph" }> = { kind: "graph", env: null, orgId: "org", projectId: "shop", envIds: ["env"], nodeId: "node1", rpcId: "shop.v1.Orders/Get", traceIds: ["abc"], startMs: 60000, endMs: 120000, origin: "PRE_TAIL_RECEIVED", truncated: true, provisional: true };
describe("graph Ask reference", () => {
  it("includes only the closed reference list, never runtime content", () => {
    const prompt = buildAskReliantPrompt({ ...subject, message: "SECRET", sql: "SECRET", headers: "SECRET" } as AskSubject, { forgeProject: "shop" });
    expect(prompt).toContain('"orgId":"org"');
    expect(prompt).toContain('"truncated":true');
    expect(prompt).toContain("never invoke the explored RPC");
    expect(prompt).not.toContain("SECRET");
    expect(prompt).not.toContain("--env dev");
    expect(askWorkspaceName(subject, "abcd")).toBe("ask-graph-shop-v1-orders-get-abcd");
  });
});
