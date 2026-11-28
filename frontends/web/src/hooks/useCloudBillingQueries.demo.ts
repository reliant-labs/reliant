// Copyright (c) 2026 Reliant Labs

/**
 * hooks/useCloudBillingQueries.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): the demo's Agent sandboxes page
 * (demoAgentsRoutes.tsx, Settings/cloud/machines.tsx) has no plan, price list
 * or compute balance to read, so both reads it makes stay idle and answer
 * nothing, and the page shows machines without costs. The real module is the
 * account's billing (checkout, wallet, invoices) and reads the signed-in
 * identity, which the demo bundle must not contain.
 */

import { useQuery } from "@tanstack/react-query";

import type * as Real from "./useCloudBillingQueries";

export const usePlans: typeof Real.usePlans = () =>
  useQuery({ queryKey: ["demo", "no-plans"], queryFn: () => Promise.reject(new Error("The demo has no plans.")), enabled: false }) as ReturnType<typeof Real.usePlans>;

export const useComputeUsage: typeof Real.useComputeUsage = () =>
  useQuery({ queryKey: ["demo", "no-compute-usage"], queryFn: () => Promise.reject(new Error("The demo has no compute usage.")), enabled: false }) as ReturnType<typeof Real.useComputeUsage>;
