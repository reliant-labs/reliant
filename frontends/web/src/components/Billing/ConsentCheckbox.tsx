import { useId, type ComponentPropsWithRef, type ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * The one checkbox a purchase is consented with.
 *
 * A real `<input type="checkbox">`, so keyboard, screen readers and form
 * libraries treat it as the consent it is; styled as a bordered well rather
 * than the browser's bare 13 px box beside a paragraph, so it reads as the
 * step it is. The whole well is the label, so the target is the sentence,
 * not the box.
 *
 * Forge's component library has no checkbox; this is a composition only the
 * billing surfaces use, so it lives with them.
 */
export function ConsentCheckbox({
  children,
  className,
  ...input
}: Omit<ComponentPropsWithRef<"input">, "type" | "children"> & { children: ReactNode }) {
  const generated = useId();
  const id = input.id ?? generated;
  return (
    <label
      htmlFor={id}
      className={cn(
        "flex cursor-pointer items-start gap-3 rounded-lg border border-border/60 bg-background px-3 py-3 text-sm text-foreground",
        "transition-colors hover:border-primary/40 focus-within:ring-2 focus-within:ring-ring focus-within:ring-offset-2 focus-within:ring-offset-background",
        className,
      )}
    >
      <input
        {...input}
        id={id}
        type="checkbox"
        className="mt-0.5 h-4 w-4 flex-shrink-0 cursor-pointer rounded border-border accent-primary focus-visible:outline-none"
      />
      <span className="min-w-0 text-pretty leading-snug">{children}</span>
    </label>
  );
}
