// Copyright (c) 2026 Reliant Labs

/**
 * cardRead: a card's range and comparison, which Needs attention reads its
 * watched figures through as well, so the two never ask different things
 * under one cache key.
 */

import { afterEach, describe, expect, it, vi } from "vitest";

import type { Promotion } from "@/services/controlPlane/observability/deliveryTypes";
import type { Panel } from "@/services/controlPlane/observability/types";
import { rangeMs } from "@/services/controlPlane/observability/window";

import { cardRead } from "../cardRead";
import type { CompareTarget } from "../dashboardModel";

const period: CompareTarget = { mode: "period", offsetMs: rangeMs("30d"), label: "previous period" };
const panel = (extra: Partial<Panel>): Panel => ({ id: "p", title: "p", kind: "number", signal: "traces", series: [], ...extra });

describe("cardRead", () => {
  it("compares a traces figure on 30 days with the 7 days it reads, not 30", () => {
    const read = cardRead("30d", panel({}), period);
    expect(read.range).toBe("30d");
    expect(read.widened).toBe(false);
    expect(read.compare?.offsetKey).toBe("period:30d");
    expect(read.compare?.offsetMs()).toBe(rangeMs("7d"));
    // Its chart shifts the previous period by what was read, too.
    expect(read.compare?.shiftMs).toBe(rangeMs("7d"));
  });

  it("compares a logs figure the same way, and a sql figure over the whole range", () => {
    expect(cardRead("30d", panel({ signal: "logs" }), period).compare?.offsetMs()).toBe(rangeMs("7d"));
    expect(cardRead("30d", panel({ kind: "sql", signal: "" }), period).compare?.offsetMs()).toBe(rangeMs("30d"));
  });

  it("widens a figure with a fixed bucket, and compares over the widened range", () => {
    const read = cardRead("1h", panel({ kind: "sql", signal: "", minStep: "1d" }), period);
    expect(read.range).toBe("7d");
    expect(read.widened).toBe(true);
    // The key names the range read, so it never shares an entry with the 1h reads.
    expect(read.compare?.offsetKey).toBe("period:7d");
    expect(read.compare?.offsetMs()).toBe(rangeMs("7d"));
    expect(read.compare?.shiftMs).toBe(rangeMs("7d"));
  });

  afterEach(() => vi.useRealTimers());

  it("compares with the window before a release, keyed by the promotion", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-03-16T12:00:00Z"));
    const promotion = { promotionId: "pr-1", promotedAtMs: Date.now() - 3_600_000 } as Promotion;
    const read = cardRead("24h", panel({}), { mode: "release", offsetMs: 3_600_000, label: "before v1", promotion });
    expect(read.compare?.offsetKey).toBe("release:pr-1");
    vi.setSystemTime(new Date("2026-03-16T12:00:05Z"));
    // Read when it is fetched: the window ending at the promotion, however long ago that is now.
    expect(read.compare?.offsetMs()).toBe(3_605_000);
    expect(read.compare?.shiftMs).toBe(3_600_000);
  });

  it("has no comparison when none is on", () => {
    expect(cardRead("24h", panel({}), { mode: "off" }).compare).toBeNull();
  });
});
