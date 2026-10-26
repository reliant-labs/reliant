// Copyright (c) 2025 Reliant Labs

/**
 * "Set up a machine" from the automation dialog's Runs on, when the person
 * has none. The ONE place the dialog reaches the add-a-machine flow.
 *
 * SEAM: the UI overhaul's workstream 6 is building a standalone AddMachine
 * sheet, used everywhere a machine is added. Until it lands this opens the
 * existing connect-a-machine modal; when it lands, mount the sheet here and
 * nothing else in the dialog changes. The dialog's machine list refreshes on
 * its own (useDaemonStatus), so a machine added here becomes choosable at once.
 */

import { ConnectDaemonModal } from "../../Layout/ConnectDaemonModal";

export interface AddMachineSeamProps {
  open: boolean;
  onClose: () => void;
}

export function AddMachineSeam({ open, onClose }: AddMachineSeamProps) {
  return <ConnectDaemonModal isOpen={open} onClose={onClose} />;
}
