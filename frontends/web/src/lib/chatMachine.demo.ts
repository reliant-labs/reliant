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
export const DEFAULT_MACHINE: typeof Real.DEFAULT_MACHINE = "";

// Vite development links transitive modules before Rollup removes unused
// exports. Supply the whole helper surface, with honest no-machine answers,
// so a shared console chunk can load without pulling real daemon code in.
export const isAwakeMachine: typeof Real.isAwakeMachine = () => false;
export const isStartingMachine: typeof Real.isStartingMachine = () => false;
export const isUsableMachineForChat: typeof Real.isUsableMachineForChat = () => false;
export const hasMachine: typeof Real.hasMachine = () => false;
export const installedDaemonIds: typeof Real.installedDaemonIds = () => undefined;
export const defaultMachineDaemon: typeof Real.defaultMachineDaemon = () => undefined;
export const defaultChatMachine: typeof Real.defaultChatMachine = () => NO_MACHINE;
export const chatMachineOptions: typeof Real.chatMachineOptions = () => [];
export const startOptionsForMachine: typeof Real.startOptionsForMachine = () => ({ no_machine: true });
export const continueBranchPoint: typeof Real.continueBranchPoint = () => undefined;

export const NO_MACHINE_PILL: typeof Real.NO_MACHINE_PILL = "No machine · web & integrations";
export const NO_MACHINE_EXPLAINER: typeof Real.NO_MACHINE_EXPLAINER = "No machine: can't read or change files in this project";
export const NO_MACHINE_COMPOSER_HINT: typeof Real.NO_MACHINE_COMPOSER_HINT = "No machine — this chat can use the web and your integrations, not the files in this project.";
