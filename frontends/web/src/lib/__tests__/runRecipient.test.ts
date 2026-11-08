import { describe, expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";

import { runRecipientRefusal } from "../runRecipient";

function refused(reason: string): ConnectError {
  return new ConnectError(
    "refused",
    Code.FailedPrecondition,
    new Headers({ "x-forge-error-reason": reason }),
  );
}

describe("runRecipientRefusal", () => {
  it("tells the user to pick an agent when several run in parallel", () => {
    expect(runRecipientRefusal(refused("run_recipient_ambiguous"))).toMatch(
      /Open the thread of the agent you mean/,
    );
  });

  it("says nothing was sent when no agent is running", () => {
    expect(runRecipientRefusal(refused("run_no_listening_agent"))).toMatch(
      /wasn't sent/,
    );
  });

  it("leaves every other error to the caller", () => {
    expect(runRecipientRefusal(refused("chat_not_started"))).toBeNull();
    expect(runRecipientRefusal(new Error("boom"))).toBeNull();
  });
});
