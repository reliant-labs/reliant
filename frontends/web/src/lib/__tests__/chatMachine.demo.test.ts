// Copyright (c) 2025 Reliant Labs
import { expect, it } from "vitest";

import { NO_MACHINE as canonical } from "@/components/Automations/daemonChoices";
import * as real from "../chatMachine";
import * as demo from "../chatMachine.demo";
import { isAwakeMachine, NO_MACHINE } from "../chatMachine.demo";

it("keeps the no-machine sentinel available without shipping daemon contracts", () => {
  expect(NO_MACHINE).toBe(canonical);
  expect(isAwakeMachine({ status: 0 })).toBe(false);
});

it("provides every runtime export for Vite's pre-tree-shaking module linking", () => {
  expect(Object.keys(demo).sort()).toEqual(Object.keys(real).sort());
  for (const key of Object.keys(real) as (keyof typeof real)[]) {
    if (typeof real[key] === "string") expect(demo[key], key).toBe(real[key]);
  }
  expect(demo.defaultMachineDaemon([])).toBeUndefined();
  expect(demo.defaultChatMachine({ daemons: [], loading: false })).toBe(demo.NO_MACHINE);
  expect(demo.chatMachineOptions([])).toEqual([]);
});
