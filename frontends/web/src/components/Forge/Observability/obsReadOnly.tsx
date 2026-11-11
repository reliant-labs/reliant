// Copyright (c) 2025 Reliant Labs

/**
 * Whether the console in view may change anything. The demo console
 * (Forge/Demo) provides `{ readOnly: true, reason }`; everywhere else the
 * default holds and the server's answer decides.
 *
 * Controls that write read it and render disabled with the reason as their
 * title, so a visitor sees "Read-only demo" on hover instead of clicking and
 * meeting an error. The API underneath refuses writes regardless
 * (readOnlyApi.ts).
 */

import { createContext, useContext, type ReactNode } from "react";

export interface ObsReadOnly {
  readOnly: boolean;
  /** Why, for a disabled control's title. */
  reason?: string;
}

const ObsReadOnlyContext = createContext<ObsReadOnly>({ readOnly: false });

export function ObsReadOnlyProvider({ value, children }: { value: ObsReadOnly; children: ReactNode }) {
  return <ObsReadOnlyContext.Provider value={value}>{children}</ObsReadOnlyContext.Provider>;
}

export function useObsReadOnly(): ObsReadOnly {
  return useContext(ObsReadOnlyContext);
}
