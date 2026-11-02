import { useRef, type KeyboardEvent, type ReactNode } from "react";
import { cn } from "../../lib/utils";

export interface SettingsTabItem<T extends string> {
  id: T;
  label: ReactNode;
  /** Shown after the label, muted (a count). */
  count?: number;
  testId?: string;
}

/**
 * A settings page's tab strip, built to the WAI-ARIA tabs pattern: each tab
 * names the panel it controls (`aria-controls` / `id`), only the selected
 * tab is in the tab order (roving tabindex), and Left/Right/Home/End move
 * between tabs and select them. The selection is the caller's — usually the
 * URL's `?tab=` — so a link or a refresh lands on the same tab.
 *
 * Pair it with {@link SettingsTabPanel} using the same `idPrefix`.
 */
export function SettingsTabs<T extends string>({
  tabs,
  value,
  onChange,
  idPrefix,
  ariaLabel,
  className,
}: {
  tabs: readonly SettingsTabItem<T>[];
  value: T;
  onChange: (id: T) => void;
  /** Unique per page: the tab and panel ids derive from it. */
  idPrefix: string;
  ariaLabel: string;
  className?: string;
}) {
  const refs = useRef<Array<HTMLButtonElement | null>>([]);

  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const last = tabs.length - 1;
    let next: number;
    switch (event.key) {
      case "ArrowRight":
        next = index === last ? 0 : index + 1;
        break;
      case "ArrowLeft":
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
    onChange(tabs[next].id);
    refs.current[next]?.focus();
  };

  return (
    <div role="tablist" aria-label={ariaLabel} className={cn("flex gap-1 overflow-x-auto border-b border-border", className)}>
      {tabs.map((tab, index) => {
        const selected = tab.id === value;
        return (
          <button
            key={tab.id}
            ref={(el) => {
              refs.current[index] = el;
            }}
            type="button"
            role="tab"
            id={settingsTabId(idPrefix, tab.id)}
            aria-selected={selected}
            aria-controls={settingsPanelId(idPrefix, tab.id)}
            tabIndex={selected ? 0 : -1}
            data-testid={tab.testId}
            onClick={() => onChange(tab.id)}
            onKeyDown={(event) => onKeyDown(event, index)}
            className={cn(
              "-mb-px inline-flex shrink-0 items-center gap-1.5 rounded-sm border-b-2 px-3 py-2 text-sm font-medium transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              selected
                ? "border-primary text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {tab.label}
            {tab.count !== undefined && <span className="text-xs tabular-nums text-muted-foreground">{tab.count}</span>}
          </button>
        );
      })}
    </div>
  );
}

/** The panel a {@link SettingsTabs} tab controls; render only the selected one. */
export function SettingsTabPanel({
  idPrefix,
  tab,
  children,
  className,
  ...rest
}: {
  idPrefix: string;
  tab: string;
  children: ReactNode;
  className?: string;
} & Record<`data-${string}`, string>) {
  return (
    <div
      role="tabpanel"
      id={settingsPanelId(idPrefix, tab)}
      aria-labelledby={settingsTabId(idPrefix, tab)}
      tabIndex={0}
      className={cn("focus:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded-sm", className)}
      {...rest}
    >
      {children}
    </div>
  );
}

function settingsTabId(prefix: string, tab: string) {
  return `${prefix}-tab-${tab}`;
}

function settingsPanelId(prefix: string, tab: string) {
  return `${prefix}-panel-${tab}`;
}
