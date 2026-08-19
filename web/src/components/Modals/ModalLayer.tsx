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

import { useLocation } from "@tanstack/react-router";
import {
  useModalStore,
  type UpgradeRequiredData,
  type BillingEmailRequiredData,
} from "@/store/modalStore";
import { isModalAllowedOnRoute } from "./modalRoutePolicy";
import { ApiKeySetupModal } from "../ApiKeySetupModal";
import { UpgradeRequiredModal } from "../UpgradeRequiredModal";
import { BillingEmailRequiredModal } from "../BillingEmailRequiredModal";

export function ModalLayer() {
  const activeModal = useModalStore((s) => s.activeModal);
  const data = useModalStore((s) => s.data);
  const closeModal = useModalStore((s) => s.closeModal);
  const { pathname } = useLocation();

  // Some routes own the screen while they are active — see modalRoutePolicy.
  // Suppressing here rather than clearing `activeModal` is deliberate: the
  // modal is hidden for as long as the user is on that route and reappears if
  // it is still relevant once they leave, so a request the user actually made
  // is deferred rather than silently dropped.
  if (activeModal && !isModalAllowedOnRoute(activeModal, pathname)) {
    return null;
  }

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
