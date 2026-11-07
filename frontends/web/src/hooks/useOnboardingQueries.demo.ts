// Copyright (c) 2025 Reliant Labs

/**
 * hooks/useOnboardingQueries.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts). Machines (daemons) are a Reliant account's; the
 * demo has none, so the environment page's machine-backed tabs say so.
 */

import { useQuery } from "@tanstack/react-query";

import type * as Real from "./useOnboardingQueries";

type Daemons = NonNullable<ReturnType<typeof Real.useDaemonList>["data"]>;

export const useDaemonList: typeof Real.useDaemonList = () =>
  useQuery({ queryKey: ["demo", "no-machines"], queryFn: async (): Promise<Daemons> => [] as unknown as Daemons, staleTime: Infinity });
