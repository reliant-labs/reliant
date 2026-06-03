/**
 * Modal Store
 *
 * Forge "Phase 1" single source of truth for modal visibility.
 *
 * Replaces a handful of per-modal `show*` flags scattered across other stores
 * (onboardingChecklistStore, apiKeySetupStore, …) so that exactly ONE modal can
 * ever be active, and all mounts live in a single `ModalLayer` component that
 * sits as a sibling of every ModernApp branch return.
 */

import { create } from "zustand";

export type ModalId = "api-key-setup" | "upgrade-required";

export interface UpgradeRequiredData {
  // Machine-readable reason (X-Reliant-Reason header). The modal uses this
  // to pick which copy to show; unrecognized values fall back to a generic
  // "quota exceeded" message. See control-plane/internal/enforcement for
  // the canonical list.
  reason: string;
  // Human-readable message from the backend (Connect error message). Shown
  // verbatim under the title as the explanation.
  message: string;
  // Optional upgrade-page URL from X-Reliant-Upgrade-URL. Empty when the
  // backend doesn't offer one; the CTA button is hidden in that case.
  upgradeUrl: string;
}

export interface ModalData {
  "api-key-setup": undefined;
  "upgrade-required": UpgradeRequiredData;
}

interface ModalState {
  activeModal: ModalId | null;
  data: ModalData[keyof ModalData] | undefined;
  openModal<K extends ModalId>(id: K, data?: ModalData[K]): void;
  closeModal(): void;
}

export const useModalStore = create<ModalState>((set) => ({
  activeModal: null,
  data: undefined,
  openModal: (id, data) => set({ activeModal: id, data }),
  closeModal: () => set({ activeModal: null, data: undefined }),
}));
