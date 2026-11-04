// Copyright (c) 2025 Reliant Labs

/**
 * What the Observability panel tells the views under it that they would
 * otherwise each be handed: the environments in scope (a trace knows an
 * environment by id; the CLI wants its name) and the forge project.
 */

import { createContext, useContext } from "react";

import type { ObsEnvironment } from "../obsPlace";

export interface ObsAskContext {
  environments: readonly ObsEnvironment[];
  forgeProject?: string;
}

const NONE: ObsAskContext = { environments: [] };

export const ObsAskContextValue = createContext<ObsAskContext>(NONE);

export const useObsAskContext = () => useContext(ObsAskContextValue);
