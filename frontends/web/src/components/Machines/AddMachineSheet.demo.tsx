// Copyright (c) 2025 Reliant Labs

/**
 * components/Machines/AddMachineSheet.tsx in the forge demo's bundle (a demo
 * twin, vite-plugin-demo-bundle.ts): the demo is read-only and nobody is signed
 * in, so no sandbox can be created or paired. Every surface that opens the sheet
 * (the chat's Connect a machine dialog, the no-machine and wait states, the
 * project picker) gets a short note saying so instead. The real sheet sells
 * compute through the embedded Stripe checkout and links identities, billing
 * and account code the demo bundle must not contain.
 */

import Button from "@/components/forge-ui/button";
import Modal from "@/components/forge-ui/modal";

import type * as Real from "./AddMachineSheet";

export type AddMachineSheetProps = Real.AddMachineSheetProps;
export type ReadyMachine = Real.ReadyMachine;

export const AddMachineSheet: typeof Real.AddMachineSheet = ({ open, onClose, title = "Add an agent sandbox" }) => {
  if (!open) return null;
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      footer={
        <div className="flex justify-end">
          <Button variant="ghost" onClick={onClose}>
            Close
          </Button>
        </div>
      }
    >
      <p className="text-sm text-muted-foreground">
        This demo is read-only, so it can&apos;t start a sandbox.
      </p>
    </Modal>
  );
};
