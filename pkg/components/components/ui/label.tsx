import React from "react";

import { FormFieldContext } from "./form_field_context";

/**
 * Label — form field label primitive. Renders a native <label> with the
 * forge-default font size/weight + an optional required asterisk.
 *
 * ## Auto-binding to a sibling input
 *
 * Inside a `<FormField>` wrapper, Label reads `FormFieldContext` and
 * automatically wires `htmlFor` to the same generated id that the
 * sibling input picks up. The page-author writes neither id nor
 * htmlFor:
 *
 *   <FormField>
 *     <Label>Email</Label>
 *     <Input type="email" />
 *   </FormField>
 *
 * is accessibility-equivalent to:
 *
 *   <Label htmlFor="x">Email</Label>
 *   <Input id="x" type="email" />
 *
 * Outside a FormField, explicit `htmlFor` still works the same as
 * before. Explicit props always win over the context.
 *
 * The same pattern is used by shadcn/ui (via Radix UI's Label
 * primitive) and headlessui's `<Field>`; the forge component library
 * stays radix-free at the base layer by re-implementing the minimum
 * context shape — see `FormFieldContext` in `form_field_context.tsx`.
 */

export interface LabelProps extends React.LabelHTMLAttributes<HTMLLabelElement> {
  /** When true, append a red "*" after the label text. */
  required?: boolean;
}

export default function Label({ required, className, children, htmlFor, ...rest }: LabelProps) {
  // Pull the FormField-minted id when the caller didn't pass htmlFor.
  // Explicit htmlFor wins so consumers can still wire to an arbitrary
  // id elsewhere on the page.
  const ctx = React.useContext(FormFieldContext);
  const resolvedHtmlFor = htmlFor ?? ctx?.id;

  const composed = ["mb-1 block text-sm font-medium text-ink", className ?? ""]
    .filter(Boolean)
    .join(" ");

  return (
    <label className={composed} htmlFor={resolvedHtmlFor} {...rest}>
      {children}
      {required ? (
        <span aria-hidden className="ml-0.5 text-danger">
          *
        </span>
      ) : null}
    </label>
  );
}
