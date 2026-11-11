// Copyright (c) 2025 Reliant Labs
import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { getReaderSession, subscribeReaderSession } from "./readerSession";

/** Evict the departing authenticated reader, not every independent fixture or
 * reader sharing this client. Snapshot the global reader before subscribing:
 * a fixture-mounted browser still cleans production results in its client. */
export function useReaderCacheCleanup(namespace: "data-browser" | "storage-browser") {
  const client = useQueryClient();
  useEffect(() => {
    let previous = getReaderSession();
    return subscribeReaderSession(() => {
      const departing = previous;
      previous = getReaderSession();
      if (departing === previous || !departing.principalId) return;
      const owned = (query: { queryKey: readonly unknown[] }) => query.queryKey[0] === namespace
        && query.queryKey[1] === departing.principalId && query.queryKey[2] === departing.epoch;
      void client.cancelQueries({ predicate: owned });
      client.removeQueries({ predicate: owned });
    });
  }, [client, namespace]);
}
