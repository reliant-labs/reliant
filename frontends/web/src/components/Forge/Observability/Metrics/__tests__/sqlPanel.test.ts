// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { sqlPanelStatement, stringLiteral, unboundParams } from "../sqlPanel";

const window = { startMs: 1_700_000_000_000, endMs: 1_700_003_600_000 };
const ns = (ms: number) => String(BigInt(ms) * 1_000_000n);
const ctx = { window, stepSeconds: 60, envIds: ["env-prod"] };

describe("sqlPanelStatement", () => {
  it("binds the window as {from}/{to}, the step as {step} and the scope's environments as {env_ids}", () => {
    const sql = sqlPanelStatement(
      {
        sql: "SELECT toStartOfInterval(TimeUnix, toIntervalSecond({p4:UInt32})) AS t FROM t_org_x.otel_metrics_sum WHERE MetricName = {p1:String} AND TimeUnix >= fromUnixTimestamp64Nano({p2:Int64}) AND TimeUnix < fromUnixTimestamp64Nano({p3:Int64}) AND has({p5:Array(String)}, env)",
        params: { p1: "orders.created", p2: ns(window.startMs), p3: ns(window.endMs), p4: "60", p5: "['env-prod']" },
      },
      ctx
    );
    expect(sql).toBe(
      "SELECT toStartOfInterval(TimeUnix, toIntervalSecond({step:UInt32})) AS t FROM otel_metrics_sum WHERE MetricName = 'orders.created' AND TimeUnix >= {from:DateTime64(9)} AND TimeUnix < {to:DateTime64(9)} AND has({env_ids:Array(String)}, env)"
    );
    expect(unboundParams(sql)).toEqual([]);
  });

  it("keeps a lookback as an offset from the window, so the panel reads it whatever the range", () => {
    const sql = sqlPanelStatement({ sql: "TimeUnix >= fromUnixTimestamp64Nano({p1:Int64})", params: { p1: ns(window.startMs - 120_000) } }, ctx);
    expect(sql).toBe("TimeUnix >= ({from:DateTime64(9)} - toIntervalSecond(120))");
    const sec = sqlPanelStatement({ sql: "t < toDateTime({p1:UInt32})", params: { p1: String(window.endMs / 1000 + 30) } }, ctx);
    expect(sec).toBe("t < (toDateTime({to:DateTime64(9)}) + toIntervalSecond(30))");
  });

  it("writes every other parameter in as a literal of its type", () => {
    const sql = sqlPanelStatement(
      {
        sql: "{a:String} {b:Float64} {c:Int64} {d:Array(String)} {e:Date} toIntervalSecond({f:UInt32})",
        params: { a: "it's \\ odd", b: "0.95", c: "-3", d: "['x','y']", e: "2026-10-23", f: "300" },
      },
      ctx
    );
    // {f} is not the step the panel ran at, so it stays a literal.
    expect(sql).toBe("'it\\'s \\\\ odd' 0.95 -3 ['x','y'] CAST('2026-10-23', 'Date') toIntervalSecond(300)");
  });

  it("leaves the scope's own parameters and anything it cannot bind as they are", () => {
    const sql = sqlPanelStatement({ sql: "{from:DateTime64(9)} {services:Array(String)} {missing:String}", params: {} }, ctx);
    expect(sql).toBe("{from:DateTime64(9)} {services:Array(String)} {missing:String}");
    expect(unboundParams(sql)).toEqual(["missing"]);
  });

  it("does not bind an environment list that is not the scope's", () => {
    const sql = sqlPanelStatement({ sql: "has({p1:Array(String)}, env)", params: { p1: "['env-dev']" } }, ctx);
    expect(sql).toBe("has(['env-dev'], env)");
  });
});

describe("stringLiteral", () => {
  it("escapes quotes and backslashes", () => {
    expect(stringLiteral("a'b\\c")).toBe("'a\\'b\\\\c'");
  });
});
