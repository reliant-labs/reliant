/**
 * Shared touch-native row primitives for the four mobile settings panels
 * (Notifications, Privacy, Appearance, Workspace preferences).
 *
 * These wrap the SAME store/hook actions the desktop `Settings/*` panels
 * call — they own no settings state themselves, only presentation. A panel
 * passes `checked`/`value` and an `onChange` that calls the desktop mutation
 * or `settingsSync` write directly.
 *
 * `MobileToggleRow` and `MobileSegmentedRow` make the whole row the tap
 * target (≥44px tall) rather than resizing the visual switch/button to
 * 44px — the switch stays the same visual scale as desktop's `Toggle`, but
 * the padded row around it is what receives the tap, per the "pad the
 * tappable region" guidance.
 */

import { useState } from "react";
import { Check, ChevronRight, X } from "lucide-react";
import { cn } from "../../lib/utils";

export interface MobileSettingOption {
  value: string;
  label: string;
  description?: string;
}

export function MobileToggleRow({
  label,
  description,
  checked,
  onChange,
  disabled,
  icon,
}: {
  label: string;
  description?: string;
  checked: boolean;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
  icon?: React.ReactNode;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn(
        "flex min-h-[44px] w-full items-center justify-between gap-4 px-4 py-3 text-left active:bg-muted/50",
        disabled && "opacity-50",
      )}
    >
      <div className="flex min-w-0 flex-1 items-start gap-2.5">
        {icon && <div className="mt-0.5 shrink-0 text-muted-foreground">{icon}</div>}
        <div className="min-w-0">
          <p className="text-sm font-medium text-foreground">{label}</p>
          {description && (
            <p className="mt-0.5 text-xs text-muted-foreground">{description}</p>
          )}
        </div>
      </div>
      <span
        aria-hidden
        className={cn(
          "relative inline-flex h-6 w-11 shrink-0 items-center rounded-full border-2 transition-colors",
          checked
            ? "border-primary bg-primary"
            : "border-gray-400 bg-gray-300 dark:border-gray-600 dark:bg-gray-700",
        )}
      >
        <span
          className={cn(
            "inline-block h-4 w-4 transform rounded-full bg-background transition-transform",
            checked ? "translate-x-5" : "translate-x-0.5",
          )}
        />
      </span>
    </button>
  );
}

/** Tap-to-open-sheet row: shows the current value, opens a bottom sheet of options on tap. */
export function MobileSelectRow({
  label,
  description,
  value,
  options,
  onChange,
  sheetTitle,
}: {
  label: string;
  description?: string;
  value: string;
  options: MobileSettingOption[];
  onChange: (value: string) => void;
  sheetTitle?: string;
}) {
  const [open, setOpen] = useState(false);
  const selected = options.find((o) => o.value === value);

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="flex min-h-[44px] w-full items-center justify-between gap-3 px-4 py-3 text-left active:bg-muted/50"
      >
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium text-foreground">{label}</p>
          {description && (
            <p className="mt-0.5 text-xs text-muted-foreground">{description}</p>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-1 text-sm text-muted-foreground">
          <span className="max-w-[140px] truncate">{selected?.label ?? value}</span>
          <ChevronRight className="h-4 w-4" />
        </div>
      </button>
      {open && (
        <MobileOptionSheet
          title={sheetTitle ?? label}
          options={options}
          value={value}
          onSelect={(v) => {
            onChange(v);
            setOpen(false);
          }}
          onClose={() => setOpen(false)}
        />
      )}
    </>
  );
}

/** Bottom sheet listing selectable options — the sheet body `MobileSelectRow` opens. */
export function MobileOptionSheet({
  title,
  options,
  value,
  onSelect,
  onClose,
}: {
  title: string;
  options: MobileSettingOption[];
  value: string;
  onSelect: (value: string) => void;
  onClose: () => void;
}) {
  return (
    <div className="fixed inset-0 z-50 flex flex-col justify-end">
      <button
        type="button"
        aria-label="Dismiss"
        onClick={onClose}
        className="absolute inset-0 bg-black/40"
      />
      <div
        className="relative flex max-h-[75vh] flex-col rounded-t-2xl border-t border-border bg-background shadow-lg"
        style={{ paddingBottom: "env(safe-area-inset-bottom)" }}
      >
        <div className="flex items-center justify-between border-b border-border px-4 py-3">
          <span className="text-sm font-semibold text-foreground">{title}</span>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted"
          >
            <X className="h-5 w-5" />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto py-1">
          {options.map((option) => {
            const isSelected = option.value === value;
            return (
              <button
                key={option.value}
                type="button"
                onClick={() => onSelect(option.value)}
                className="flex min-h-[44px] w-full items-center justify-between gap-3 px-4 py-3 text-left active:bg-muted/50"
              >
                <div className="min-w-0">
                  <p
                    className={cn(
                      "text-sm",
                      isSelected ? "font-medium text-foreground" : "text-foreground",
                    )}
                  >
                    {option.label}
                  </p>
                  {option.description && (
                    <p className="mt-0.5 text-xs text-muted-foreground">
                      {option.description}
                    </p>
                  )}
                </div>
                {isSelected && <Check className="h-4 w-4 shrink-0 text-primary" />}
              </button>
            );
          })}
        </div>
      </div>
    </div>
  );
}

/** Inline 2-3 way button group for short-labeled binary/ternary choices (theme, mode toggles). */
export function MobileSegmentedRow({
  label,
  options,
  value,
  onChange,
}: {
  label?: string;
  options: Array<{ value: string; label: string; icon?: React.ReactNode }>;
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <div className="px-4 py-3">
      {label && (
        <p className="mb-2 text-sm font-medium text-foreground">{label}</p>
      )}
      <div className="flex items-center gap-1 rounded-lg border border-border p-1">
        {options.map((option) => {
          const isSelected = option.value === value;
          return (
            <button
              key={option.value}
              type="button"
              onClick={() => onChange(option.value)}
              aria-pressed={isSelected}
              className={cn(
                "flex min-h-[44px] flex-1 items-center justify-center gap-1.5 rounded-md px-2 text-sm",
                isSelected
                  ? "bg-muted font-medium text-foreground"
                  : "text-muted-foreground",
              )}
            >
              {option.icon}
              {option.label}
            </button>
          );
        })}
      </div>
    </div>
  );
}

export function MobileSettingsSectionTitle({
  title,
  description,
}: {
  title: string;
  description?: string;
}) {
  return (
    <div className="px-4 pt-4 pb-1">
      <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        {title}
      </h3>
      {description && (
        <p className="mt-0.5 text-xs text-muted-foreground">{description}</p>
      )}
    </div>
  );
}
