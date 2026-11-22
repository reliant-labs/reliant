// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import { parameterNames } from "../../Observability/Dashboards/alertExpr";
import { eventExploreSql } from "../EventsMetricsPage";

const ESCAPES: Record<string, string> = { "\\": "\\", "'": "'", n: "\n", r: "\r", t: "\t", "0": "\0" };

/**
 * Reads the string literal after `EventName = ` the way ClickHouse does
 * (backslash escapes, a doubled quote), and returns its value and what follows it.
 */
function readEventLiteral(sql: string): { value: string; rest: string } {
  const start = sql.indexOf("EventName = '");
  expect(start).toBeGreaterThan(0);
  let value = "";
  for (let i = start + "EventName = '".length; i < sql.length; i++) {
    const c = sql[i]!;
    if (c === "\\") {
      const next = sql[++i]!;
      value += ESCAPES[next] ?? next;
    } else if (c === "'") {
      if (sql[i + 1] === "'") {
        value += "'";
        i++;
        continue;
      }
      return { value, rest: sql.slice(i + 1) };
    } else {
      value += c;
    }
  }
  throw new Error("unterminated literal");
}

const REST = " AND Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)} GROUP BY t ORDER BY t";

describe("eventExploreSql", () => {
  it.each([
    ["product.signup"],
    ["product.it's"],
    ["product.back\\slash"],
    ["product.trailing\\"],
    ["product.x\\' OR 1=1 --"],
    ["product.x' OR EventName != '"],
    ["product.line\nbreak\ttab\rreturn\0nul"],
    ["product.{step:UInt32}"],
    ["product.ünïcødé 🚀"],
  ])("keeps %j one exact literal, so the query around it is unchanged", (event) => {
    const sql = eventExploreSql(event);
    const { value, rest } = readEventLiteral(sql);
    expect(value).toBe(event);
    expect(rest).toBe(REST);
    // Only the scope's parameters are bound: nothing in the name became one.
    expect(parameterNames(sql)).toEqual(["step", "from", "to"]);
  });
});
