// Copyright (c) 2025 Reliant Labs
import { expect, it } from "vitest";

import { NO_MACHINE as canonical } from "@/components/Automations/daemonChoices";
import { isAwakeMachine, NO_MACHINE } from "../chatMachine.demo";

it("keeps the no-machine sentinel available without shipping daemon contracts", () => {
  expect(NO_MACHINE).toBe(canonical);
  expect(isAwakeMachine({ status: 0 })).toBe(false);
});
