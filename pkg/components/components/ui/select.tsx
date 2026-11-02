import React from "react";

import { FormFieldContext } from "./form_field_context";

/**
 * Select — generic select primitive. Wraps a native <select> with
 * tasteful Tailwind defaults so pages stop hand-rolling page-size
 * pickers, status dropdowns, etc.
 *
 * Two ways to populate options:
 *
 *   1. Pass an `options` array — `[{ value, label, disabled? }, ...]`.
 *   2. Pass `<option>` children directly for full control.
 *
 * Pair with the <Label> primitive for labelling. When used inside a
 * <FormField>, Select auto-picks-up the FormField's generated id so a
 * sibling <Label> can wire `htmlFor` to it without the caller writing
 * any id/htmlFor boilerplate.
 */
export type SelectSize = "sm" | "md" | "lg";

export interface SelectOption {
  value: string | number;
  label: string;
  disabled?: boolean;
}

export interface SelectProps extends Omit<React.SelectHTMLAttributes<HTMLSelectElement>, "size"> {
  /** Optional list of `<option>` entries. */
  options?: SelectOption[];
  /** Visual size of the control. Defaults to "md". */
  selectSize?: SelectSize;
  /** Mark the control as invalid; toggles the red focus ring. */
  invalid?: boolean;
}

const sizeStyles: Record<SelectSize, string> = {
  sm: "h-8 pl-2.5 pr-8 text-xs",
  md: "h-10 pl-3 pr-9 text-sm",
  lg: "h-12 pl-3.5 pr-10 text-base",
};

const Select = React.forwardRef<HTMLSelectElement, SelectProps>(function Select(
  { options, selectSize = "md", invalid, className, children, id, ...rest },
  ref,
) {
  // Pull id from FormFieldContext when the caller didn't pass one.
  const ctx = React.useContext(FormFieldContext);
  const resolvedId = id ?? ctx?.id;

  const composed = [
    "block w-full appearance-none rounded-md border bg-surface",
    "focus:outline-none focus:ring-1",
    "disabled:cursor-not-allowed disabled:bg-surface-muted disabled:text-ink-muted",
    invalid
      ? "border-danger focus:border-danger focus:ring-danger"
      : "border-border-strong focus:border-accent focus:ring-accent",
    sizeStyles[selectSize],
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <div className="relative">
      <select ref={ref} id={resolvedId} className={composed} {...rest}>
        {options
          ? options.map((o) => (
              <option key={String(o.value)} value={o.value} disabled={o.disabled}>
                {o.label}
              </option>
            ))
          : children}
      </select>
      <svg
        aria-hidden
        className="pointer-events-none absolute right-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-ink-subtle"
        fill="none"
        viewBox="0 0 24 24"
        strokeWidth={2}
        stroke="currentColor"
      >
        <path strokeLinecap="round" strokeLinejoin="round" d="M19.5 8.25l-7.5 7.5-7.5-7.5" />
      </svg>
    </div>
  );
});

export default Select;
