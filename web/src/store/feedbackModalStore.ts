import { create } from 'zustand';
import type { FeedbackSubmission } from '../lib/feedback';

export type FeedbackModalPrefill = Partial<Pick<FeedbackSubmission, 'type' | 'title' | 'description' | 'extraContext'>>;

interface FeedbackModalState {
  isOpen: boolean;
  prefill: FeedbackModalPrefill | null;

  open: (prefill?: FeedbackModalPrefill) => void;
  close: () => void;
}

export const useFeedbackModalStore = create<FeedbackModalState>((set) => ({
  isOpen: false,
  prefill: null,

  open: (prefill) => set({ isOpen: true, prefill: prefill ?? null }),
  close: () => set({ isOpen: false, prefill: null }),
}));
