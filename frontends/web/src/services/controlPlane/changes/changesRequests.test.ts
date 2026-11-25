// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { stopChangeRequest } from "./changesRequests";

describe("stopChangeRequest", () => {
  it.each([0, 7])("preserves explicit revision %i for concurrency protection", (expectedRevision) => {
    expect(stopChangeRequest("change-1", expectedRevision)).toEqual({
      changeId: "change-1",
      expectedRevision: BigInt(expectedRevision),
    });
  });

  it("omits the revision only when none was supplied", () => {
    expect(stopChangeRequest("change-1")).toEqual({ changeId: "change-1" });
    expect(stopChangeRequest("change-1", undefined)).toEqual({ changeId: "change-1" });
  });
});
