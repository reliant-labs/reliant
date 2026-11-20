// Copyright (c) 2026 Reliant Labs

import { navConfigA } from "./a";
import { navConfigB } from "./b";
import { navConfigC } from "./c";
import type { NavConfig, NavVariant } from "./types";

export const NAV_CONFIGS: Readonly<Record<NavVariant, NavConfig>> = { a: navConfigA, b: navConfigB, c: navConfigC };

export * from "./types";
export * from "./model";
export * from "./variant";
