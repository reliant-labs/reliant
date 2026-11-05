// Copyright (c) 2025 Reliant Labs

// The execution-mode enum (`mode`: manual | auto | plan) keeps "auto" as its
// default and a valid value, but the UI does not list it as a choice.
export const AUTO_MODE = "auto";

/** True for an enum that is an execution mode: "auto" beside "manual" and/or "plan". */
export function isExecutionMode(options: readonly string[] | null | undefined): boolean {
  return !!options && options.includes(AUTO_MODE) && (options.includes("manual") || options.includes("plan"));
}

/** The options to list: an execution mode without "auto", any other enum unchanged. */
export function selectableOptions(options: readonly string[] | null | undefined): string[] {
  const all = options ? [...options] : [];
  return isExecutionMode(all) ? all.filter((option) => option !== AUTO_MODE) : all;
}

/** An execution mode left with fewer than two choices has nothing to switch between. */
export function isSelectorHidden(options: readonly string[] | null | undefined): boolean {
  return isExecutionMode(options) && selectableOptions(options).length < 2;
}
