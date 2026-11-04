import React from "react";

import { FormFieldContext } from "./form_field_context";

/**
 * Input — generic text input primitive. Wraps a native <input> with
 * tasteful Tailwind defaults plus optional invalid styling. All standard
 * <input> attributes (type, value, onChange, autoComplete, ...) are
 * forwarded; pass `className` to extend or override.
 *
 * Pair with the <Label> primitive for labelling and accessibility.
 * When used inside a <FormField>, Input auto-picks-up the FormField's
 * generated id so a sibling <Label> can wire `htmlFor` to it without
 * the caller writing any id/htmlFor boilerplate.
 */
export type InputSize = "sm" | "md" | "lg";

export interface InputProps extends Omit<React.InputHTMLAttributes<HTMLInputElement>, "size"> {
  /** Visual size of the control. Defaults to "md". */
  inputSize?: InputSize;
  /** Mark the input as invalid; toggles the red focus ring. */
  invalid?: boolean;
}

const sizeStyles: Record<InputSize, string> = {
  sm: "h-8 px-2.5 text-xs",
  md: "h-10 px-3 text-sm",
  lg: "h-12 px-3.5 text-base",
};

const Input = React.forwardRef<HTMLInputElement, InputProps>(function Input(
  { inputSize = "md", invalid, className, type, id, ...rest },
  ref,
) {
  // Pull id from FormFieldContext when the caller didn't pass one.
  // Explicit id wins — preserves the ability to wire to an external
  // aria-describedby / aria-labelledby reference.
  const ctx = React.useContext(FormFieldContext);
  const resolvedId = id ?? ctx?.id;

  const composed = [
    "block w-full rounded-md border bg-surface",
    "placeholder:text-ink-subtle",
    "focus:outline-none focus:ring-1",
    "disabled:cursor-not-allowed disabled:bg-surface-muted disabled:text-ink-muted",
    invalid
      ? "border-danger focus:border-danger focus:ring-danger"
      : "border-border-strong focus:border-accent focus:ring-accent",
    sizeStyles[inputSize],
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <input
      ref={ref}
      id={resolvedId}
      type={type ?? "text"}
      aria-invalid={invalid || undefined}
      className={composed}
      {...rest}
    />
  );
});

export default Input;
