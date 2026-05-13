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

export type ModalId = "api-key-setup";

export interface ModalData {
  "api-key-setup": undefined;
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
