import { describe, expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { classifyRpcFailure, isUnroutedProcedure } from "../rpcErrorPolicy";

const reliant = { serviceTypeName: "reliant.v1.ChatService", signalAborted: false };

describe("classifyRpcFailure", () => {
  it("calls anything after the request's own abort an abort, whatever was thrown", () => {
    expect(classifyRpcFailure("missing request message", { ...reliant, signalAborted: true })).toBe("aborted");
    expect(
      classifyRpcFailure(new ConnectError("boom", Code.Internal), { ...reliant, signalAborted: true }),
    ).toBe("aborted");
  });

  it("recognises the machine-wait signal only as Unavailable with the marker", () => {
    const waiting = new ConnectError(
      "your machine is waking up: no daemon connected yet",
      Code.Unavailable,
    );
    expect(classifyRpcFailure(waiting, reliant)).toBe("machine-wait");
    // The same text on a code the server should not have used stays visible.
    expect(
      classifyRpcFailure(new ConnectError("unavailable: no daemon connected for user", Code.Internal), reliant),
    ).toBe("report");
    expect(classifyRpcFailure(new ConnectError("upstream reset", Code.Unavailable), reliant)).toBe("report");
  });

  it("reads a bare 404 from the reliant API as this app being out of date", () => {
    const unrouted = new ConnectError("HTTP 404", Code.Unimplemented);
    expect(isUnroutedProcedure(unrouted)).toBe(true);
    expect(classifyRpcFailure(unrouted, reliant)).toBe("version-skew");
    // A handler that deliberately answered Unimplemented is not skew.
    const handler = new ConnectError("reliant.v1.ChatService.Foo is not implemented", Code.Unimplemented);
    expect(classifyRpcFailure(handler, reliant)).toBe("report");
    // Nor is another backend's 404.
    expect(
      classifyRpcFailure(unrouted, { serviceTypeName: "controlplane.v1.DeployService", signalAborted: false }),
    ).toBe("report");
  });

  it("keeps the expected-code list and reports the rest", () => {
    expect(classifyRpcFailure(new ConnectError("x", Code.NotFound), reliant)).toBe("expected");
    expect(classifyRpcFailure(new ConnectError("x", Code.Internal), reliant)).toBe("report");
    expect(classifyRpcFailure(new Error("x"), reliant)).toBe("report");
  });
});

describe("account_required", () => {
  const accountRequired = new ConnectError(
    "sign in with an email account to start a cloud machine",
    Code.FailedPrecondition,
    new Headers({ "x-reliant-reason": "account_required" }),
  );

  it("is a state the UI renders, not a report", () => {
    expect(classifyRpcFailure(accountRequired, reliant)).toBe("account-required");
    expect(
      classifyRpcFailure(accountRequired, { serviceTypeName: "controlplane.v1.DaemonService", signalAborted: false }),
    ).toBe("account-required");
  });

  it("is keyed on the reason, so a re-tuned status code still is not reported", () => {
    const internal = new ConnectError("x", Code.Internal, new Headers({ "x-reliant-reason": "account_required" }));
    expect(classifyRpcFailure(internal, reliant)).toBe("account-required");
  });
});
