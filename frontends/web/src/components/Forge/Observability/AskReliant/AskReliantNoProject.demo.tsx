// Copyright (c) 2025 Reliant Labs

/**
 * AskReliantNoProject.tsx in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts). The real one shows how to connect a machine
 * (PairMachinePanel), the way to the plans and Add project: Reliant machines,
 * billing and projects, none of which the demo bundle may contain. The demo is
 * in handoff mode, so "Ask an agent" opens the handoff sheet and never asks
 * which project; this is never reached, and renders nothing if it were.
 */

import type * as Real from "./AskReliantNoProject";

export type AskReliantNoProjectProps = Real.AskReliantNoProjectProps;

export const AskReliantNoProject: typeof Real.AskReliantNoProject = () => <></>;
