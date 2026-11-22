// Copyright (c) 2026 Reliant Labs

/**
 * The top bar proposal (owner, 2026-11-22: "revisit the entire top bar on the
 * agents screen"). DRAFT: two variants live side by side until the owner picks
 * one; the loser and this switch are deleted then.
 *
 * - "a" — one bar. On a chat, the chat's title, threads, runs, context ring
 *   and ⋯ move INTO the app bar (Claude.ai, ChatGPT, Linear): one 48px row
 *   instead of two.
 * - "b" — two calm rows. The app bar keeps project / branch and the window's
 *   panels; the chat row keeps the chat (Vercel, Cursor). Each fact has one
 *   home: the machine lives in the app bar only.
 * - "current" — main as it was, for comparison.
 *
 * Picked with `?topbar=a|b|current`, remembered in localStorage.
 */

import { create } from "zustand";

export type TopBarVariant = "a" | "b" | "current";

const STORAGE_KEY = "reliant.topbar-variant";
const DEFAULT_VARIANT: TopBarVariant = "a";

function isVariant(value: unknown): value is TopBarVariant {
  return value === "a" || value === "b" || value === "current";
}

export function readTopBarVariant(): TopBarVariant {
  if (typeof window === "undefined") return DEFAULT_VARIANT;
  try {
    const fromUrl = new URLSearchParams(window.location.search).get("topbar");
    if (isVariant(fromUrl)) {
      window.localStorage.setItem(STORAGE_KEY, fromUrl);
      return fromUrl;
    }
    const stored = window.localStorage.getItem(STORAGE_KEY);
    if (isVariant(stored)) return stored;
  } catch {
    // Storage refused (private mode): the default.
  }
  return DEFAULT_VARIANT;
}

// Read once per page load: switching variants is a reload, never a remount mid-session.
const variant = readTopBarVariant();

export function useTopBarVariant(): TopBarVariant {
  return variant;
}

/**
 * Variant A's chat slot: the element in the app bar the open chat's header
 * renders into. One chat holds it at a time (`owner`); any other ChatHeader
 * on screen keeps its own row.
 */
interface TopBarSlotState {
  el: HTMLElement | null;
  owner: symbol | null;
  setEl: (el: HTMLElement | null) => void;
  claim: (owner: symbol) => boolean;
  release: (owner: symbol) => void;
}

export const useTopBarSlot = create<TopBarSlotState>((set, get) => ({
  el: null,
  owner: null,
  setEl: (el) => set({ el }),
  claim: (owner) => {
    const current = get().owner;
    if (current && current !== owner) return false;
    if (current !== owner) set({ owner });
    return true;
  },
  release: (owner) => {
    if (get().owner === owner) set({ owner: null });
  },
}));
