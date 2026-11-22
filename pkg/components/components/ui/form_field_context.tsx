import React from "react";

/**
 * FormFieldContext — minted by `<FormField>` (./form), consumed by sibling
 * form primitives (`<Label>`, `<Input>`, `<Select>`, …) so a single id is
 * shared across the label/input pair without callers writing
 * `htmlFor` / `id` boilerplate.
 *
 * Custom form controls can opt into the same auto-binding behavior — read
 * the context, fall back to the prop the caller passed:
 *
 *   const ctx = React.useContext(FormFieldContext);
 *   const id = props.id ?? ctx?.id;
 *
 * It is its own module, not an export of form.tsx, because a file that
 * exports components AND a context defeats React Fast Refresh: an edit to it
 * reloads the page instead of hot-swapping the components
 * (eslint-plugin-react-refresh's only-export-components).
 */
export interface FormFieldContextValue {
  /** Generated id shared between the label and its associated input. */
  id: string;
}

export const FormFieldContext = React.createContext<FormFieldContextValue | null>(null);
