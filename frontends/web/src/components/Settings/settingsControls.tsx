import { useId, useRef, type KeyboardEvent, type ReactNode } from "react";
import { cn } from "../../lib/utils";
import { settingsSync, type SettingKey } from "../../services/settingsSync";
import ToggleSwitch from "../forge-ui/toggle_switch";

/**
 * The controls Appearance and Chat & editor share. Each one is labelled the
 * way assistive tech expects (a label tied to its field, a radiogroup's
 * selection in aria-checked) rather than by colour and position alone.
 */

export function readPref<T extends string>(key: SettingKey, fallback: T): T {
  return settingsSync.getSetting(key, fallback) as T;
}

/**
 * Persist a preference, but only when it actually differs from what is stored.
 *
 * Every preference on these pages lives in a `useState` seeded from storage on
 * mount and written back by an effect keyed on that same state, so merely
 * OPENING a page re-saved values the user never touched. A mount that read a
 * STALE value re-persisted it with a fresh timestamp, making the stale value
 * the newest record — which is how a chosen font size silently reverted. It
 * showed up in the database as a perfect md/lg/md/lg alternation, one pair per
 * visit.
 *
 * Returns whether a write happened so the caller can skip its change event too.
 */
export function persistPref(key: SettingKey, value: string): boolean {
  if (settingsSync.getSetting(key, "") === value) return false;
  void settingsSync.setSettingIfChanged(key, value).catch(console.error);
  return true;
}

/** Wait (up to 5s) for settingsSync to load from the database. */
export async function waitForSettings(): Promise<void> {
  for (let attempts = 0; !settingsSync.isInitialized() && attempts < 50; attempts++) {
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
}

const selectClass =
  "block w-full px-3 py-2 bg-background border border-border text-foreground rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-ring focus:border-primary hover:border-border/80";

export function SettingToggle({
  label,
  description,
  checked,
  onChange,
}: {
  label: string;
  description: string;
  checked: boolean;
  onChange: (checked: boolean) => void;
}) {
  const id = useId();
  return (
    <div className="flex items-center justify-between gap-4">
      <div>
        <label htmlFor={id} className="text-sm font-medium">
          {label}
        </label>
        <p className="text-xs text-muted-foreground mt-0.5">{description}</p>
      </div>
      <ToggleSwitch id={id} checked={checked} onChange={onChange} ariaLabel={label} size="lg" className="forge-ui" />
    </div>
  );
}

export function SettingRange({
  label,
  value,
  min,
  max,
  step = 1,
  onChange,
}: {
  label: string;
  value: number;
  min: number;
  max: number;
  step?: number;
  onChange: (value: number) => void;
}) {
  const id = useId();
  return (
    <div className="space-y-2">
      <label htmlFor={id} className="text-sm font-medium">
        {label}
      </label>
      <input
        id={id}
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={(e) => onChange(parseInt(e.target.value))}
        className="w-full"
      />
    </div>
  );
}

export function SettingSelect({
  label,
  value,
  options,
  onChange,
  hint,
}: {
  label: string;
  value: string;
  options: { value: string; label: string }[];
  onChange: (value: string) => void;
  hint?: ReactNode;
}) {
  const id = useId();
  return (
    <div className="space-y-3">
      <label htmlFor={id} className="text-sm font-medium block">
        {label}
      </label>
      <select id={id} value={value} onChange={(e) => onChange(e.target.value)} className={selectClass}>
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  );
}

export { selectClass as settingSelectClass };

export interface Choice<T extends string> {
  value: T;
  label: string;
  description?: string;
}

/**
 * One-of-N as a real radiogroup: the selection is `aria-checked`, the group
 * is one tab stop (the checked option), and the arrow keys move and select —
 * the behaviour a screen-reader user expects from a set of radio buttons.
 */
export function ChoiceGroup<T extends string>({
  label,
  value,
  choices,
  onChange,
  className,
}: {
  /** The group's accessible name. */
  label: string;
  value: T;
  choices: readonly Choice<T>[];
  onChange: (value: T) => void;
  /** Layout, e.g. `sm:grid-cols-3`. */
  className?: string;
}) {
  const refs = useRef<Array<HTMLButtonElement | null>>([]);
  const selectedIndex = Math.max(
    0,
    choices.findIndex((choice) => choice.value === value),
  );

  const move = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const last = choices.length - 1;
    let next: number;
    switch (event.key) {
      case "ArrowRight":
      case "ArrowDown":
        next = index === last ? 0 : index + 1;
        break;
      case "ArrowLeft":
      case "ArrowUp":
        next = index === 0 ? last : index - 1;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = last;
        break;
      default:
        return;
    }
    event.preventDefault();
    onChange(choices[next].value);
    refs.current[next]?.focus();
  };

  return (
    <div
      role="radiogroup"
      aria-label={label}
      className={cn("grid gap-2", className)}
    >
      {choices.map((choice, index) => {
        const checked = index === selectedIndex;
        return (
          <button
            key={choice.value}
            ref={(el) => {
              refs.current[index] = el;
            }}
            type="button"
            role="radio"
            aria-checked={checked}
            tabIndex={checked ? 0 : -1}
            onClick={() => onChange(choice.value)}
            onKeyDown={(event) => move(event, index)}
            className={cn(
              "rounded-md border px-3 py-2 text-left transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              checked
                ? "border-primary bg-primary/10 text-foreground"
                : "border-border/60 bg-background text-muted-foreground hover:border-border/80 hover:bg-foreground/5 hover:text-foreground",
            )}
          >
            <span className="block text-sm font-medium">{choice.label}</span>
            {choice.description && (
              <span className="mt-1 block text-xs text-muted-foreground">{choice.description}</span>
            )}
          </button>
        );
      })}
    </div>
  );
}
