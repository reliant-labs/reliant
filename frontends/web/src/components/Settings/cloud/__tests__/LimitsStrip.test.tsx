import { describe, expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";

import { aiLimit, hostingLimit, machineLimit, monitoringLimit } from "../LimitsStrip";

const unimplemented = new ConnectError("no", Code.Unimplemented);

describe("limits strip rows", () => {
  it("hosting: the org's budget, or why there is none", () => {
    expect(hostingLimit({ data: { enabled: true, agreement: { status: "active", infraBudgetCents: 5000n } }, error: null }).value).toBe("$50.00 / month");
    expect(hostingLimit({ data: { enabled: true }, error: null }).value).toBe("Not set up");
    expect(hostingLimit({ data: { enabled: false }, error: null }).value).toBe("Not available yet");
    expect(hostingLimit({ error: unimplemented }).value).toBe("Not available yet");
    expect(hostingLimit({ data: { enabled: true, agreement: { status: "pending", infraBudgetCents: 1n } }, error: null }).value).toBe("Checkout pending");
    expect(hostingLimit({ error: new Error("boom") }).value).toBe("Unavailable");
  });

  it("monitoring: zero means no overage, never a $0 cap", () => {
    expect(monitoringLimit({ data: { spendCapCents: 0n }, error: null }).value).toBe("No overage");
    expect(monitoringLimit({ data: { spendCapCents: 2500n }, error: null }).value).toBe("$25.00 / month");
  });

  it("machine: the person's overage choice", () => {
    expect(machineLimit({ data: {}, error: null }).value).toBe("No machine plan");
    expect(machineLimit({ data: { subscription: { overageEnabled: false } }, error: null }).value).toBe("Stops at included hours");
    expect(machineLimit({ data: { subscription: { overageEnabled: true, budgetCents: 0n } }, error: null }).value).toBe("No ceiling");
    expect(machineLimit({ data: { subscription: { overageEnabled: true, budgetCents: 3000n } }, error: null }).value).toBe("$30.00 / month");
  });

  it("AI: auto top-up's ceiling, or spend bounded by credit", () => {
    expect(aiLimit({ data: {}, error: null }).value).toBe("Auto top-up off");
    expect(aiLimit({ data: { autoRecharge: { enabled: true, maxPerMonthCents: 10000n } }, error: null }).value).toBe("$100.00 / month");
  });
});
