/**
 * ModalLayer
 *
 * Forge "Phase 1" single mount point for all migrated modals. Reads the
 * `activeModal` discriminator from `useModalStore` and renders exactly one
 * modal on top of the underlying app shell.
 *
 * IMPORTANT: This component is mounted as a sibling of every ModernApp branch
 * return (project picker, settings, workflow, main shell, …) so that modals
 * stay visible regardless of which branch is currently rendering.
 */

import {
  useModalStore,
  type UpgradeRequiredData,
  type BillingEmailRequiredData,
} from "@/store/modalStore";
import { ApiKeySetupModal } from "../ApiKeySetupModal";
import { UpgradeRequiredModal } from "../UpgradeRequiredModal";
import { BillingEmailRequiredModal } from "../BillingEmailRequiredModal";

export function ModalLayer() {
  const activeModal = useModalStore((s) => s.activeModal);
  const data = useModalStore((s) => s.data);
  const closeModal = useModalStore((s) => s.closeModal);

  if (activeModal === "api-key-setup") {
    return <ApiKeySetupModal isOpen onClose={closeModal} />;
  }

  if (activeModal === "upgrade-required") {
    return (
      <UpgradeRequiredModal
        isOpen
        onClose={closeModal}
        data={data as UpgradeRequiredData}
      />
    );
  }

  if (activeModal === "billing-email-required") {
    return (
      <BillingEmailRequiredModal
        isOpen
        onClose={closeModal}
        data={data as BillingEmailRequiredData}
      />
    );
  }

  return null;
}
