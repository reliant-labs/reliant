// Copyright (c) 2025 Reliant Labs

/**
 * Module mocks for the observability tests, in a file that imports nothing
 * from the app so a hoisted vi.mock factory can load it:
 *
 *   vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());
 *   vi.mock("@tanstack/react-virtual", async () => (await import("./obsMocks")).obsMocks.virtual());
 *
 * uPlot: jsdom has no canvas 2D context, and uPlot reads matchMedia at import.
 * react-virtual: jsdom lays nothing out, so a real virtualizer renders no rows.
 */

export const obsMocks = {
  uplot: () => ({
    default: class FakeUPlot {
      width = 0;
      destroy() {}
      setSize() {}
    },
  }),
  /** Render every row: which rows a real virtualizer would choose is beside the point in jsdom. */
  virtual: () => ({
    useVirtualizer: (options: { count: number; getItemKey?: (index: number) => string | number }) => ({
      getVirtualItems: () =>
        Array.from({ length: options.count }, (_, index) => ({
          index,
          key: options.getItemKey ? options.getItemKey(index) : index,
          start: index * 30,
          end: index * 30 + 30,
          size: 30,
          lane: 0,
        })),
      getTotalSize: () => options.count * 30,
      measureElement: () => {},
      scrollToIndex: () => {},
    }),
  }),
};
