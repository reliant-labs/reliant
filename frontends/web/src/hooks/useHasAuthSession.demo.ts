// Copyright (c) 2026 Reliant Labs

/**
 * hooks/useHasAuthSession.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): there is no auth store in the demo. Its
 * session is the shell's (demoShellRoutes.tsx DEMO_SESSION, lib/session), a
 * fixed visitor with no credential, so the reads behind this gate go to the
 * demo's transport (the shop app layer) as they would for a signed-in user.
 */

import { useSignedIn } from "@/lib/session";

import type * as Real from "./useHasAuthSession";

export const useHasAuthSession: typeof Real.useHasAuthSession = () => useSignedIn();
