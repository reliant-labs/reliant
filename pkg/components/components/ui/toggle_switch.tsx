import React from "react";

import { FormFieldContext } from "./form_field_context";

/**
 * ToggleSwitch — an on/off control (`role="switch"`), with an optional visible
 * label and description beside it.
 *
 * Every switch needs a name. Give it one of:
 *
 *   - `label`: visible text beside the switch, wired by `aria-labelledby`
 *     (and clicking it toggles the switch, as a native label would);
 *   - `ariaLabel`: when the name is visible elsewhere — a settings row's
 *     heading, a table cell — or is not visible at all;
 *   - an external `<label htmlFor>`: pass the same `id`, or put the switch in
 *     a `<FormField>` and it picks up the field's id.
 *
 * `description` is announced as the switch's description, not folded into
 * its name, so a screen reader says "Notifications, switch, on — Sends a
 * desktop alert…" rather than one long name.
 *
 * The off track is `bg-border-strong`, not a surface token: a surface-coloured
 * track on a surface-coloured card is invisible, and the off state is the one
 * a user has to be able to see to know there is a control at all.
 */
export type ToggleSwitchSize = "sm" | "md" | "lg";

export interface ToggleSwitchProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  /** Visible label beside the switch; also its accessible name. */
  label?: React.ReactNode;
  /** Secondary text under the label, announced as the description. */
  description?: React.ReactNode;
  /** Accessible name when there is no visible `label`. */
  ariaLabel?: string;
  disabled?: boolean;
  /** Defaults to "md". */
  size?: ToggleSwitchSize;
  /** The switch's id, for an external `<label htmlFor>`. Defaults to the FormField's. */
  id?: string;
  /** Extra classes on the root element (layout, a `.forge-ui` scope). */
  className?: string;
}

const sizeStyles: Record<ToggleSwitchSize, { track: string; thumb: string; translate: string }> = {
  sm: { track: "h-4 w-7", thumb: "h-3 w-3", translate: "translate-x-3.5" },
  md: { track: "h-5 w-9", thumb: "h-4 w-4", translate: "translate-x-4.5" },
  lg: { track: "h-6 w-11", thumb: "h-5 w-5", translate: "translate-x-5.5" },
};

export default function ToggleSwitch({
  checked,
  onChange,
  label,
  description,
  ariaLabel,
  disabled,
  size = "md",
  id,
  className,
}: ToggleSwitchProps) {
  const s = sizeStyles[size];
  const ctx = React.useContext(FormFieldContext);
  const generatedId = React.useId();
  const switchId = id ?? ctx?.id ?? generatedId;
  const labelId = `${switchId}-label`;
  const descriptionId = `${switchId}-description`;

  const root = [
    // A lone label centres on the track; with a description under it, the
    // track aligns with the label's line instead of the pair's middle.
    !label && !description
      ? "inline-flex"
      : description
        ? "flex items-start gap-3"
        : "flex items-center gap-3",
    disabled ? "cursor-not-allowed opacity-50" : "",
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <div className={root}>
      <button
        id={switchId}
        type="button"
        role="switch"
        aria-checked={checked}
        aria-label={label ? undefined : ariaLabel}
        aria-labelledby={label ? labelId : undefined}
        aria-describedby={description ? descriptionId : undefined}
        disabled={disabled}
        onClick={() => !disabled && onChange(!checked)}
        className={`relative inline-flex flex-shrink-0 items-center rounded-full transition-colors duration-200 ease-in-out focus:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2 disabled:cursor-not-allowed motion-reduce:transition-none ${
          s.track
        } ${checked ? "bg-accent" : "bg-border-strong"}`}
      >
        <span
          aria-hidden
          className={`inline-block rounded-full shadow-sm ring-0 transition-transform duration-200 ease-in-out motion-reduce:transition-none ${
            s.thumb
          } ${checked ? `${s.translate} bg-on-accent` : "translate-x-0.5 bg-surface"}`}
        />
      </button>
      {(label || description) && (
        <div className="min-w-0">
          {label && (
            // A native label for the pointer (clicking it toggles the
            // switch); aria-labelledby above is what names it.
            <label
              id={labelId}
              htmlFor={switchId}
              className={`text-sm font-medium text-ink ${disabled ? "" : "cursor-pointer"}`}
            >
              {label}
            </label>
          )}
          {description && (
            <p id={descriptionId} className="text-sm text-ink-muted">
              {description}
            </p>
          )}
        </div>
      )}
    </div>
  );
}
