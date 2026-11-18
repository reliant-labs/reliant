/**
 * useForgeRoster memoizes its wrapper around the live and topology query
 * results on queryStateKey, so a caller sees a result field move only when the
 * key holds it. These tests pin that the key holds every value field a query
 * result carries (and fail when TanStack adds one), and that a field moving
 * alone — `isStale` here, with data, status and fetchStatus unchanged — reaches
 * a memoized consumer.
 */
import { describe, expect, it } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { createElement, useMemo, type ReactNode } from "react";
import {
  QueryClient,
  QueryClientProvider,
  QueryObserver,
  useQuery,
} from "@tanstack/react-query";

import { QUERY_RESULT_STATE_FIELDS, queryStateKey } from "../forge-queries";

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

  it("hands on a result whose isStale moved while its status held still", async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    client.setQueryData(["k"], 1);
    const wrapper = ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children);
    const { result } = renderHook(
      () => {
        const query = useQuery({
          queryKey: ["k"],
          queryFn: () => 1,
          staleTime: 30,
          refetchOnMount: false,
        });
        // eslint-disable-next-line react-hooks/exhaustive-deps -- the key is the result's state, as in useForgeRoster.
        return useMemo(() => ({ query }), queryStateKey(query));
      },
      { wrapper },
    );
    expect(result.current.query.isStale).toBe(false);
    const { status, fetchStatus, dataUpdatedAt } = result.current.query;
    await waitFor(() => expect(result.current.query.isStale).toBe(true));
    expect(result.current.query).toMatchObject({
      status,
      fetchStatus,
      dataUpdatedAt,
      data: 1,
    });
  });
});
