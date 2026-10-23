import React from "react";

import { FormFieldContext } from "./form_field_context";

/**
 * Form primitives — minimal, unopinionated wrappers for building a
 * forms-as-react-tree shape. The root <Form> is just a styled <form>;
 * <FormField>, <FormError>, and <FormActions> handle spacing and error
 * presentation so individual pages stop hand-rolling them.
 *
 * For schema validation, pair with `react-hook-form` + `zod` (already a
 * dependency of the scaffolded auth UI); these primitives don't bundle a
 * validator.
 */
export type FormProps = React.FormHTMLAttributes<HTMLFormElement>;

export default function Form({ className, noValidate, children, ...rest }: FormProps) {
  const composed = ["space-y-4", className ?? ""].filter(Boolean).join(" ");
  return (
    <form noValidate={noValidate ?? true} className={composed} {...rest}>
      {children}
    </form>
  );
}

/**
 * FormField — vertical-stack container for a Label + Input + optional
 * error. Use as the building block for each row of a form.
 *
 * FormField mints a stable id (`React.useId()`) and provides it to its
 * descendants via `FormFieldContext`. The contained `<Label>` and the
 * contained form control (`<Input>`, `<Select>`, …) both pick up the
 * same id without the caller writing either `htmlFor` or `id`:
 *
 *   <FormField>
 *     <Label>Email</Label>
 *     <Input type="email" />
 *   </FormField>
 *
 * Explicit `htmlFor` or `id` props passed on the children still win,
 * so deterministic ids (e.g. for tour highlights or
 * `aria-describedby` from another node) remain straightforward.
 */
export interface FormFieldProps {
  className?: string;
  /**
   * Optional explicit id. When set, overrides the auto-generated id —
   * useful when an external element (e.g. an aria-describedby on a
   * help text node) needs to reference the input by a known id.
   */
  id?: string;
  children: React.ReactNode;
}

export function FormField({ className, id, children }: FormFieldProps) {
  const generatedId = React.useId();
  const fieldId = id ?? generatedId;
  const composed = ["block", className ?? ""].filter(Boolean).join(" ");
  return (
    <FormFieldContext.Provider value={{ id: fieldId }}>
      <div className={composed}>{children}</div>
    </FormFieldContext.Provider>
  );
}

/**
 * FormError — small red message anchored under an input. Renders
 * nothing when `message` is empty/undefined so callers can safely
 * pass `errors.foo?.message` from react-hook-form.
 */
export interface FormErrorProps {
  message?: string | null;
  className?: string;
}

export function FormError({ message, className }: FormErrorProps) {
  if (!message) return null;
  const composed = ["mt-1 text-xs text-danger", className ?? ""].filter(Boolean).join(" ");
  return <p className={composed}>{message}</p>;
}

/**
 * FormActions — horizontal action row at the bottom of a form
 * (primary submit + optional cancel). Right-aligned by default.
 */
export interface FormActionsProps {
  className?: string;
  align?: "start" | "end" | "between";
  children: React.ReactNode;
}

const alignStyles: Record<NonNullable<FormActionsProps["align"]>, string> = {
  start: "justify-start",
  end: "justify-end",
  between: "justify-between",
};

export function FormActions({ className, align = "end", children }: FormActionsProps) {
  const composed = ["mt-2 flex items-center gap-2", alignStyles[align], className ?? ""]
    .filter(Boolean)
    .join(" ");
  return <div className={composed}>{children}</div>;
}
