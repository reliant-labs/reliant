// Copyright (c) 2025 Reliant Labs

/**
 * lib/settingsPersistence.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): nobody signs in to the demo, so there are no
 * account settings to read or write. The tour keeps its progress in memory
 * there (demoShellBoot.ts), so this is never asked for anything.
 */

import type * as Real from "./settingsPersistence";

export type PersistedStringSetting = Real.PersistedStringSetting;
export type SettingRead = Real.SettingRead;

export const readSetting: typeof Real.readSetting = async () => ({ status: "missing" });

export const safeGetSetting: typeof Real.safeGetSetting = async () => null;

export const upsertStringSetting: typeof Real.upsertStringSetting = async () => {};

export const deleteSettingIfExists: typeof Real.deleteSettingIfExists = async () => {};
