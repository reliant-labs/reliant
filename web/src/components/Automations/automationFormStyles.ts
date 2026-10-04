// Copyright (c) 2025 Reliant Labs

/**
 * Field classes shared by the automation form's native controls. Native
 * <select>/<input>/<textarea> rather than the app's custom dropdowns because
 * they are keyboard- and screen-reader-complete for free, and every one of
 * them here has a real <label for>.
 */
export const fieldClass =
  "h-9 w-full rounded-md border border-input bg-background px-3 text-sm text-foreground " +
  "placeholder:text-muted-foreground/70 focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/30 " +
  "disabled:cursor-not-allowed disabled:opacity-50 aria-[invalid=true]:border-destructive";

export const textareaClass =
  "min-h-[96px] w-full rounded-md border border-input bg-background px-3 py-2 text-sm text-foreground " +
  "placeholder:text-muted-foreground/70 focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/30 " +
  "aria-[invalid=true]:border-destructive";

export const labelClass = "mb-1.5 block text-sm font-medium text-foreground";

export const hintClass = "mt-1 text-xs text-muted-foreground";

export const errorTextClass = "mt-1 text-xs text-destructive";
