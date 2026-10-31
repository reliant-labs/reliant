import { createContext, useContext } from "react";
export interface MachineSaleTerms { orgId: string; planId: string; pricingVersion: string; termsSha256: string }
export const MachineSaleContext = createContext<MachineSaleTerms | null>(null);
export const useMachineSaleTerms = () => useContext(MachineSaleContext);
