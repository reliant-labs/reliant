// Copyright (c) 2025 Reliant Labs

/**
 * lib/chatMachine.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): the demo has no machines.
 */

import type * as Real from "./chatMachine";

// Keep the picker sentinel without importing Automations/daemonChoices (and
// its daemon contracts) into the public console. The twin contract test pins
// this value to the canonical export.
export const NO_MACHINE: typeof Real.NO_MACHINE = "__no_machine__";
export const isAwakeMachine: typeof Real.isAwakeMachine = () => false;
