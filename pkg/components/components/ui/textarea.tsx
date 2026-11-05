import React from "react";

import { FormFieldContext } from "./form_field_context";

/**
 * Textarea — generic multi-line text primitive, the `<Input>`'s sibling. Wraps
 * a native <textarea> with the same border, focus ring, invalid and disabled
 * styling, so a form that mixes the two reads as one control family. All
 * standard <textarea> attributes (rows, value, onChange, maxLength, ...) are
 * forwarded; pass `className` to extend or override.
 *
 * Inside a <FormField>, Textarea picks up the field's generated id, so a
 * sibling <Label> wires `htmlFor` to it with no id/htmlFor boilerplate.
 *
 * `resize` defaults to "vertical": a user can grow the box to fit what they
 * are writing, but not push it out of its column.
 */
export type TextareaSize = "sm" | "md" | "lg";

export interface TextareaProps extends React.TextareaHTMLAttributes<HTMLTextAreaElement> {
  /** Visual size (text size and padding). Defaults to "md". */
  textareaSize?: TextareaSize;
  /** Mark the textarea as invalid; toggles the red border and focus ring. */
  invalid?: boolean;
  /** Which way the user may resize it. Defaults to "vertical". */
  resize?: "none" | "vertical" | "both";
}

const sizeStyles: Record<TextareaSize, string> = {
  sm: "min-h-16 px-2.5 py-1.5 text-xs",
  md: "min-h-20 px-3 py-2 text-sm",
  lg: "min-h-24 px-3.5 py-2.5 text-base",
};

const resizeStyles: Record<NonNullable<TextareaProps["resize"]>, string> = {
  none: "resize-none",
  vertical: "resize-y",
  both: "resize",
};

const Textarea = React.forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  { textareaSize = "md", invalid, resize = "vertical", className, id, ...rest },
  ref,
) {
  // Pull id from FormFieldContext when the caller didn't pass one.
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
    sizeStyles[textareaSize],
    resizeStyles[resize],
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <textarea
      ref={ref}
      id={resolvedId}
      aria-invalid={invalid || undefined}
      className={composed}
      {...rest}
    />
  );
});

export default Textarea;
