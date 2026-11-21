// @vitest-environment node
/**
 * The forced upgrade prompt is reported once and opened until it opens: a
 * failed lazy load of the modal store must not leave a refused build looking
 * usable until some other signal happens to arrive.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../lib/errorReporting", () => ({ reportFailure: vi.fn() }));

import { reportFailure } from "../../lib/errorReporting";
import { noteUpgradeRequired, resetVersionSkewForTests, setModalStoreLoaderForTests } from "../versionSkew";

const openModal = vi.fn();
const store = { useModalStore: { getState: () => ({ openModal }) } };

describe("noteUpgradeRequired", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    openModal.mockClear();
    vi.mocked(reportFailure).mockClear();
    resetVersionSkewForTests();
  });
  afterEach(() => {
    resetVersionSkewForTests();
    vi.useRealTimers();
  });

  it("tries again on a backoff when the modal store fails to load, and opens once it loads", async () => {
    const loader = vi
      .fn()
      .mockRejectedValueOnce(new Error("chunk load failed"))
      .mockRejectedValueOnce(new Error("chunk load failed"))
      .mockResolvedValue(store);
    setModalStoreLoaderForTests(loader as never);

    noteUpgradeRequired("refused", { minVersion: "1.9.0" });
    await vi.advanceTimersByTimeAsync(0);
    expect(loader).toHaveBeenCalledTimes(1);
    expect(openModal).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(1_000);
    expect(loader).toHaveBeenCalledTimes(2);
    expect(openModal).not.toHaveBeenCalled();

    // The second wait is longer than the first.
    await vi.advanceTimersByTimeAsync(1_000);
    expect(loader).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(loader).toHaveBeenCalledTimes(3);
    expect(openModal).toHaveBeenCalledTimes(1);
    expect(openModal).toHaveBeenCalledWith("app-out-of-date", {
      forced: true,
      minVersion: "1.9.0",
      upgradeUrl: undefined,
    });
    expect(reportFailure).toHaveBeenCalledTimes(1);
  });

  it("coalesces signals that arrive while a retry is pending into the one prompt", async () => {
    const loader = vi.fn().mockRejectedValueOnce(new Error("chunk load failed")).mockResolvedValue(store);
    setModalStoreLoaderForTests(loader as never);

    noteUpgradeRequired("refused");
    await vi.advanceTimersByTimeAsync(0);
    noteUpgradeRequired("served-minimum", { minVersion: "1.9.0" });
    noteUpgradeRequired("refused");
    await vi.advanceTimersByTimeAsync(1_000);

    expect(loader).toHaveBeenCalledTimes(2);
    expect(openModal).toHaveBeenCalledTimes(1);
    expect(reportFailure).toHaveBeenCalledTimes(1);
  });
});
