/**
 * The "Connect a machine" modal every no-machine surface opens (NoMachineState,
 * new chat, ConnectMachineDialog).
 *
 * It used to wrap onboarding's ComputeStep. That step only records a launch
 * plan — provisioning happens once, at onboarding's commit point — so picking
 * a cloud machine here created nothing and the modal then waited for a machine
 * that could never arrive, without ever asking an unpaid user to pay. It now
 * opens the picker's add-a-machine modal, which creates the machine (behind
 * the same eligibility gate, coupon and billing affordances) or walks through
 * connecting a self-hosted one.
 */
import { focusChatInput } from "../../hooks/useFocusManager";
import { ConnectDaemonModal as AddMachineModal } from "../Projects/ConnectDaemonModal";

interface ConnectDaemonModalProps {
  isOpen: boolean;
  onClose: () => void;
}

export function ConnectDaemonModal({ isOpen, onClose }: ConnectDaemonModalProps) {
  // Mounted only while open, like the picker's: the modal reads eligibility
  // and billing queries, and every no-machine surface renders this closed.
  if (!isOpen) return null;
  return <AddMachineModal isOpen={isOpen} onClose={onClose} fallbackFocus={focusChatInput} />;
}
