// Copyright (c) 2025 Reliant Labs

/** The phone's run log: runs and the trigger events that started none, in one time order. */

import { describe, expect, it } from "vitest";

import type { RunSummary, UnlaunchedEventSummary } from "@/hooks/run-queries";
import { mobileRunRows } from "../MobileRunsScreen";

describe("mobileRunRows", () => {
  it("merges runs and unlaunched events newest first", () => {
    const run = (chatId: string, createdAt: number) => ({ chatId, createdAt }) as RunSummary;
    const event = (id: string, occurredAt: number) => ({ id, occurredAt }) as UnlaunchedEventSummary;
    const rows = mobileRunRows([run("r1", 30), run("r2", 10)], [event("e1", 20), event("e2", 40)]);
    expect(rows.map((row) => (row.kind === "run" ? row.run.chatId : row.event.id))).toEqual(["e2", "r1", "e1", "r2"]);
  });
});
