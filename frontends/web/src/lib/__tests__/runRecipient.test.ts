import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import { RunRecipientAmbiguousSchema } from "../../gen/reliant/v1/run_recipient_pb";
import { runRecipientChoices, runRecipientLabel, runRecipientRefusal } from "../runRecipient";

function refused(reason: string, agents?: { threadId: string; node?: string; title?: string }[]): ConnectError {
  const details = agents
    ? [{ desc: RunRecipientAmbiguousSchema, value: create(RunRecipientAmbiguousSchema, { agents }) }]
    : [];
  return new ConnectError(
    "refused",
    Code.FailedPrecondition,
    new Headers({ "x-forge-error-reason": reason }),
    details,
  );
}

describe("runRecipientRefusal", () => {
  it("tells the user to pick an agent when several run in parallel", () => {
    expect(runRecipientRefusal(refused("run_recipient_ambiguous"))).toMatch(
      /Open the thread of the agent you mean/,
    );
  });

  it("points at the choices under the message when the refusal lists the agents", () => {
    const error = refused("run_recipient_ambiguous", [{ threadId: "t1" }, { threadId: "t2" }]);
    expect(runRecipientRefusal(error)).toMatch(/Choose the one the message is for/);
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

describe("runRecipientChoices", () => {
  it("reads the agents from the refusal's RunRecipientAmbiguous detail", () => {
    const error = refused("run_recipient_ambiguous", [
      { threadId: "t-impl", node: "implement.agent_loop", title: "Implement" },
      { threadId: "t-review", node: "review.agent_loop" },
    ]);
    expect(runRecipientChoices(error)).toEqual([
      { threadId: "t-impl", node: "implement.agent_loop", title: "Implement" },
      { threadId: "t-review", node: "review.agent_loop", title: "" },
    ]);
  });

  it("is empty for a server that sends no list, another refusal, or a plain error", () => {
    expect(runRecipientChoices(refused("run_recipient_ambiguous"))).toEqual([]);
    expect(runRecipientChoices(refused("run_no_listening_agent", [{ threadId: "t1" }]))).toEqual([]);
    expect(runRecipientChoices(new Error("boom"))).toEqual([]);
  });

  it("skips an agent with no thread: there is nowhere to send to it", () => {
    const error = refused("run_recipient_ambiguous", [{ threadId: "" }, { threadId: "t2" }]);
    expect(runRecipientChoices(error).map((c) => c.threadId)).toEqual(["t2"]);
  });
});

describe("runRecipientLabel", () => {
  it("names the agent by its thread's title, else its node, else its thread", () => {
    expect(runRecipientLabel({ threadId: "t1", node: "implement.agent_loop", title: " Fix it " })).toBe("Fix it");
    expect(runRecipientLabel({ threadId: "t1", node: "implement.agent_loop", title: "" })).toBe("implement");
    expect(runRecipientLabel({ threadId: "0123456789abcdef", node: "", title: "" })).toBe("thread 01234567");
  });
});
