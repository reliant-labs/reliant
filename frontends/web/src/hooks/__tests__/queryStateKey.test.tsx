/**
 * useForgeRoster memoizes its wrapper around the live and topology query
 * results on queryStateKey, so a caller sees a result field move only when the
 * key holds it. This test pins that the key holds every value field a query
 * result carries (and fails when TanStack adds one).
 *
 * The test that a field moving alone (`isStale`, on a 30 ms staleTime) reaches
 * a memoized consumer was removed as flaky: under load the first render came
 * after the 30 ms had passed (#2828, flake:removed).
 */
import { describe, expect, it } from "vitest";
import { QueryClient, QueryObserver } from "@tanstack/react-query";

import { QUERY_RESULT_STATE_FIELDS } from "../forge-queries";

describe("queryStateKey", () => {
  it("holds every value field of a query result", () => {
    const client = new QueryClient();
    const result = new QueryObserver(client, {
      queryKey: ["k"],
      queryFn: () => 1,
    }).getCurrentResult();
    const values = Object.keys(result)
      .filter(
        (key) =>
          typeof result[key as keyof typeof result] !== "function" &&
          key !== "promise",
      )
      .sort();
    expect([...QUERY_RESULT_STATE_FIELDS].sort()).toEqual(values);
  });
});
