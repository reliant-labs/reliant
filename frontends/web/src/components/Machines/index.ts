/**
 * Adding a machine. `AddMachineSheet` is the one entry point every surface
 * that needs a machine opens; `PairMachinePanel` is its "your own computer"
 * step, also used by onboarding and Settings → Machines.
 */
export { AddMachineSheet, type AddMachineSheetProps, type ReadyMachine } from "./AddMachineSheet";
export { PairMachinePanel, type PairMachineMode } from "./PairMachinePanel";
