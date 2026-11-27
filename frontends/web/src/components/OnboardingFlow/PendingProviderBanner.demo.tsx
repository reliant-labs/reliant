// Copyright (c) 2025 Reliant Labs

/**
 * components/OnboardingFlow/PendingProviderBanner.tsx in the forge demo's
 * bundle (a demo twin, vite-plugin-demo-bundle.ts). The banner finishes
 * connecting the model provider a person picked while signing up, for the
 * signed-in identity (lib/auth). Nobody signs up in the demo, so there is
 * never a provider pending and the banner never shows.
 */

import type * as Real from "./PendingProviderBanner";

export const PendingProviderBanner: typeof Real.PendingProviderBanner = () => null;
