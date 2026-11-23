// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { leaveChatPageFor, useProjectStore } from "../projectStore.demo";

describe("demo project navigation", () => {
  it("does not leave the demo for a Reliant project page", async () => {
    expect(await leaveChatPageFor("project-1")).toBe(false);
    expect(useProjectStore.getState().currentProject).toBeNull();
  });
});
