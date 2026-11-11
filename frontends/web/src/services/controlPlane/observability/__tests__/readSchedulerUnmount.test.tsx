// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { QueryClientProvider, useQuery } from "@tanstack/react-query";

import { createTestQueryClient } from "@/test/renderWithQuery";
import { ReadScheduler } from "../readScheduler";

/**
 * A tile that unmounts while its read is running or queued (the user moved on)
 * gives its slot back at once: React Query aborts the signal the hooks pass
 * down, and the scheduler frees the slot on that abort without waiting for the
 * request to settle.
 */
describe("a read whose tile unmounts", () => {
  it("frees its slot, and drops its queued sibling, the moment the tile goes", async () => {
    const scheduler = new ReadScheduler(1);
    const never = () => new Promise<string>(() => {});
    function Tile({ id }: { id: string }) {
      useQuery({ queryKey: ["tile", id], queryFn: ({ signal }) => scheduler.run(never, signal), retry: false });
      return null;
    }
    const view = render(
      <QueryClientProvider client={createTestQueryClient()}>
        <Tile id="a" />
        <Tile id="b" />
      </QueryClientProvider>
    );
    await waitFor(() => expect(scheduler.stats).toEqual({ running: 1, queued: 1 }));

    view.unmount();

    await waitFor(() => expect(scheduler.stats).toEqual({ running: 0, queued: 0 }));
  });
});
