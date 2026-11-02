// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { parameterNames, platformRule } from "../alertExpr";

describe("platformRule", () => {
  it("reads the event a platform rule's marker names, and whether the env disabled it", () => {
    expect(platformRule("SELECT 1 AS value WHERE 0 /* reliant:platform new-issue env:{reliant_env_id:String} */")).toEqual({ key: "new-issue", off: false });
    expect(platformRule("SELECT 1 AS value WHERE 0 /* reliant:platform observability-quota:over_100 env:'e1' off */")).toEqual({
      key: "observability-quota:over_100",
      off: true,
    });
  });

  it("is null for a real query, even one that scopes by the env's id", () => {
    expect(platformRule("SELECT count() AS value FROM otel_logs WHERE ResourceAttributes['reliant.env.id'] = {reliant_env_id:String}")).toBeNull();
    expect(platformRule("SELECT 1 /* reliant: a note */")).toBeNull();
  });
});

describe("parameterNames", () => {
  it("lists each parameter once, in order of first use", () => {
    expect(parameterNames("SELECT {a:String}, { b : UInt32 } WHERE x = {a:String} AND y IN {c:Array(String)}")).toEqual(["a", "b", "c"]);
  });

  it("skips string literals and comments", () => {
    expect(parameterNames("SELECT '{a:String}', \"{b:String}\" /* {c:String} */ -- {d:String}\nWHERE e = {e:String} # {f:String}")).toEqual(["e"]);
  });
});
