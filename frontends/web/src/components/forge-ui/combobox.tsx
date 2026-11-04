import React, { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";

import { FormFieldContext } from "./form_field_context";
import Popover, { type PopoverTriggerProps } from "./popover";

/**
 * Combobox — a searchable select, single or multiple (WAI-ARIA combobox
 * with a listbox popup). Use it where a native <select> stops working: more
 * options than fit a glance, options with a description or an icon, groups,
 * or several choices at once. A handful of plain choices is still a Select.
 *
 * The trigger is a button that reads like a field and shows the choice. It
 * opens a panel (Popover: portals, flips, clamps, follows scroll) holding a
 * search box and the options:
 *
 *   - Typing filters on label, value, description and `keywords`; the first
 *     match is active. ArrowUp/ArrowDown move through enabled options
 *     (wrapping), PageUp/PageDown by ten; the active option scrolls into
 *     view. Focus stays in the search box throughout — the options are named
 *     to assistive tech by aria-activedescendant, not focused.
 *   - Enter or a click chooses. Single: the panel closes and focus returns
 *     to the trigger. Multiple: the option toggles, the panel stays open,
 *     and a footer counts the choices with a Clear action.
 *   - Escape closes (claimed: the dialog behind does not also close); Tab
 *     closes with focus on the trigger; so does a press outside.
 *   - A single choice is cleared with the trigger's own clear button when
 *     `clearable`.
 *
 * In a <FormField>, the trigger takes the field's id, so its <Label> names
 * it. `trigger` replaces the default field-shaped button with your own
 * element (a compact toolbar chip), given the props to spread on it and the
 * chosen options.
 */

export interface ComboboxOption {
  value: string;
  label: string;
  description?: string;
  icon?: React.ReactNode;
  /** Options with the same group are listed under one heading, in order. */
  group?: string;
  disabled?: boolean;
  /** Extra words the search matches. */
  keywords?: string[];
}

export interface ComboboxTriggerProps extends PopoverTriggerProps {
  disabled?: boolean;
  /** The chosen options, in option order. */
  selected: ComboboxOption[];
  open: boolean;
}

export type ComboboxSize = "sm" | "md" | "lg";

interface ComboboxCommonProps {
  options: ComboboxOption[];
  /** Trigger text when nothing is chosen. */
  placeholder?: string;
  /** The search box's placeholder and accessible name. Defaults to "Search". */
  searchPlaceholder?: string;
  /** Shown when nothing matches the search. */
  emptyMessage?: React.ReactNode;
  /** The options are still loading: the list says so. */
  loading?: boolean;
  disabled?: boolean;
  /** Danger border, aria-invalid. */
  invalid?: boolean;
  size?: ComboboxSize;
  /** The trigger's id; defaults to the enclosing FormField's. */
  id?: string;
  /** Accessible name when no <Label> names the trigger. */
  "aria-label"?: string;
  /** Replaces the default trigger. */
  trigger?: (props: ComboboxTriggerProps) => React.ReactElement;
  /** Which trigger edge the panel lines up with. Defaults to "left". */
  align?: "left" | "right";
  /** Preferred side; flips when there is no room. Defaults to "bottom". */
  side?: "bottom" | "top";
  /** Classes on the default trigger. */
  className?: string;
  /** Classes on the panel (width). It is at least as wide as the trigger. */
  panelClassName?: string;
}

export interface ComboboxSingleProps extends ComboboxCommonProps {
  multiple?: false;
  value: string | null;
  onChange: (value: string | null) => void;
  /** Show a clear button on the trigger while something is chosen. */
  clearable?: boolean;
}

export interface ComboboxMultipleProps extends ComboboxCommonProps {
  multiple: true;
  value: string[];
  onChange: (value: string[]) => void;
  clearable?: never;
}

export type ComboboxProps = ComboboxSingleProps | ComboboxMultipleProps;

const sizeStyles: Record<ComboboxSize, string> = {
  sm: "min-h-8 pl-2.5 pr-8 text-xs",
  md: "min-h-10 pl-3 pr-9 text-sm",
  lg: "min-h-12 pl-3.5 pr-10 text-base",
};

const PAGE = 10;
const MIN_PANEL_WIDTH = 240;

function matches(option: ComboboxOption, query: string): boolean {
  if (!query) return true;
  return [option.label, option.value, option.description ?? "", ...(option.keywords ?? [])].some(
    (text) => text.toLowerCase().includes(query),
  );
}

function Chevron() {
  return (
    <svg
      aria-hidden
      className="pointer-events-none absolute right-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-ink-subtle"
      fill="none"
      viewBox="0 0 24 24"
      strokeWidth={2}
      stroke="currentColor"
    >
      <path strokeLinecap="round" strokeLinejoin="round" d="M8 9l4-4 4 4M16 15l-4 4-4-4" />
    </svg>
  );
}

function Check({ visible }: { visible: boolean }) {
  return (
    <svg
      aria-hidden
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      className={`mt-0.5 h-4 w-4 shrink-0 text-accent-ink ${visible ? "" : "invisible"}`}
    >
      <path strokeLinecap="round" strokeLinejoin="round" d="M3.5 8.5l3 3 6-7" />
    </svg>
  );
}

function Checkbox({ checked }: { checked: boolean }) {
  return (
    <span
      aria-hidden
      className={`mt-0.5 inline-flex h-4 w-4 shrink-0 items-center justify-center rounded-sm border ${
        checked ? "border-accent bg-accent text-on-accent" : "border-border-strong bg-surface"
      }`}
    >
      {checked && (
        <svg
          viewBox="0 0 16 16"
          fill="none"
          stroke="currentColor"
          strokeWidth={2.5}
          className="h-3 w-3"
        >
          <path strokeLinecap="round" strokeLinejoin="round" d="M3.5 8.5l3 3 6-7" />
        </svg>
      )}
    </span>
  );
}

export default function Combobox(props: ComboboxProps) {
  const {
    options,
    placeholder = "Select…",
    searchPlaceholder = "Search",
    emptyMessage = "No results",
    loading,
    disabled,
    invalid,
    size = "md",
    id,
    trigger,
    align = "left",
    side = "bottom",
    className,
    panelClassName,
  } = props;
  const ariaLabel = props["aria-label"];
  const multiple = props.multiple === true;
  // One shape for both modes: the chosen values, and a setter for them.
  const values = useMemo(
    () => (props.multiple === true ? props.value : props.value === null ? [] : [props.value]),
    [props.multiple, props.value],
  );
  const onChange = props.onChange;
  const setValues = useCallback(
    (next: string[]) => {
      if (multiple) (onChange as (v: string[]) => void)(next);
      else (onChange as (v: string | null) => void)(next[0] ?? null);
    },
    [multiple, onChange],
  );
  const chosen = useMemo(() => new Set(values), [values]);
  const selected = useMemo(() => options.filter((o) => chosen.has(o.value)), [options, chosen]);

  const ctx = React.useContext(FormFieldContext);
  const triggerId = id ?? ctx?.id;
  const listboxId = useId();
  const optionIdBase = useId();
  const valueId = useId();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState<string | null>(null);
  const [triggerWidth, setTriggerWidth] = useState(0);
  const triggerRef = useRef<HTMLElement | null>(null);
  const listRef = useRef<HTMLDivElement | null>(null);
  const closeRef = useRef<() => void>(() => undefined);

  const needle = query.trim().toLowerCase();
  const visible = useMemo(() => options.filter((o) => matches(o, needle)), [options, needle]);
  const enabled = useMemo(() => visible.filter((o) => !o.disabled), [visible]);
  // Groups in the order they first appear; ungrouped options form one group.
  const groups = useMemo(() => {
    const out: Array<{ name: string | undefined; options: ComboboxOption[] }> = [];
    for (const option of visible) {
      const group = out.find((g) => g.name === option.group);
      if (group) group.options.push(option);
      else out.push({ name: option.group, options: [option] });
    }
    return out;
  }, [visible]);
  const optionId = useCallback(
    (value: string) => `${optionIdBase}-${options.findIndex((o) => o.value === value)}`,
    [optionIdBase, options],
  );

  const onOpenChange = useCallback(
    (next: boolean) => {
      if (next && disabled) return;
      if (next) {
        setQuery("");
        const first =
          options.find((o) => !o.disabled && chosen.has(o.value)) ??
          options.find((o) => !o.disabled);
        setActive(first?.value ?? null);
        setTriggerWidth(triggerRef.current?.getBoundingClientRect().width ?? 0);
      }
      setOpen(next);
    },
    [disabled, options, chosen],
  );

  // The search moves the active option to the first match.
  const onQuery = (next: string) => {
    setQuery(next);
    const n = next.trim().toLowerCase();
    setActive(options.find((o) => !o.disabled && matches(o, n))?.value ?? null);
  };

  useEffect(() => {
    if (!open || active === null) return;
    const el = Array.from(
      listRef.current?.querySelectorAll<HTMLElement>('[role="option"]') ?? [],
    ).find((o) => o.dataset.value === active);
    el?.scrollIntoView?.({ block: "nearest" });
  }, [open, active]);

  const choose = useCallback(
    (value: string) => {
      const option = options.find((o) => o.value === value);
      if (!option || option.disabled) return;
      setActive(value);
      if (multiple) {
        setValues(values.includes(value) ? values.filter((v) => v !== value) : [...values, value]);
      } else {
        closeRef.current();
        setValues([value]);
      }
    },
    [options, multiple, values, setValues],
  );

  const onSearchKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    const at = active === null ? -1 : enabled.findIndex((o) => o.value === active);
    const move = (i: number) => {
      e.preventDefault();
      if (enabled.length === 0) return;
      setActive(enabled[(i + enabled.length) % enabled.length]!.value);
    };
    if (e.key === "ArrowDown") move(at + 1);
    else if (e.key === "ArrowUp") move(at < 0 ? enabled.length - 1 : at - 1);
    else if (e.key === "PageDown") move(Math.min(at + PAGE, enabled.length - 1));
    else if (e.key === "PageUp") move(Math.max(at - PAGE, 0));
    else if (e.key === "Enter") {
      e.preventDefault();
      if (active !== null && enabled.some((o) => o.value === active)) choose(active);
    } else if (e.key === "Tab") {
      // Tab leaves the panel: close it, back on the trigger.
      e.preventDefault();
      closeRef.current();
    }
  };

  // Pointer on the options, delegated to the listbox: the options are not
  // focusable controls (aria-activedescendant names them), so a press must
  // not move focus out of the search box.
  useEffect(() => {
    const list = listRef.current;
    if (!open || !list) return;
    const optionAt = (target: EventTarget | null) =>
      target instanceof Element ? target.closest<HTMLElement>('[role="option"]') : null;
    function onMouseDown(e: MouseEvent) {
      if (optionAt(e.target)) e.preventDefault();
    }
    function onMouseMove(e: MouseEvent) {
      const el = optionAt(e.target);
      if (el && el.getAttribute("aria-disabled") !== "true" && el.dataset.value !== undefined) {
        setActive(el.dataset.value);
      }
    }
    function onClick(e: MouseEvent) {
      const el = optionAt(e.target);
      if (el?.dataset.value !== undefined) choose(el.dataset.value);
    }
    list.addEventListener("mousedown", onMouseDown);
    list.addEventListener("mousemove", onMouseMove);
    list.addEventListener("click", onClick);
    return () => {
      list.removeEventListener("mousedown", onMouseDown);
      list.removeEventListener("mousemove", onMouseMove);
      list.removeEventListener("click", onClick);
    };
  }, [open, choose]);

  const clear = () => setValues([]);
  const showClear = !multiple && props.clearable && selected.length > 0 && !disabled;

  const summary =
    selected.length === 0 ? null : multiple && selected.length > 2 ? (
      <>
        <span className="truncate">
          {selected
            .slice(0, 2)
            .map((o) => o.label)
            .join(", ")}
        </span>
        <span className="shrink-0 rounded-sm bg-surface-muted px-1 text-xs text-ink-muted">
          +{selected.length - 2}
        </span>
      </>
    ) : (
      <>
        {!multiple && selected[0]?.icon && (
          <span
            aria-hidden
            className="inline-flex h-4 w-4 shrink-0 items-center justify-center text-ink-muted"
          >
            {selected[0].icon}
          </span>
        )}
        <span className="truncate">{selected.map((o) => o.label).join(", ")}</span>
      </>
    );

  const renderTrigger = (p: PopoverTriggerProps) => {
    const merged: PopoverTriggerProps = {
      ...p,
      ref: (node) => {
        triggerRef.current = node;
        p.ref(node);
      },
    };
    if (trigger)
      return trigger({ ...merged, id: triggerId ?? merged.id, disabled, selected, open });
    return (
      <span className={`relative block ${className ?? ""}`}>
        <button
          type="button"
          {...merged}
          id={triggerId ?? merged.id}
          disabled={disabled}
          aria-label={ariaLabel}
          aria-describedby={valueId}
          aria-invalid={invalid || undefined}
          className={`flex w-full items-center gap-2 rounded-md border bg-surface text-left text-ink transition-colors focus-visible:outline-2 focus-visible:-outline-offset-1 focus-visible:outline-accent disabled:cursor-not-allowed disabled:bg-surface-muted disabled:text-ink-muted ${
            invalid ? "border-danger" : "border-border-strong aria-expanded:border-accent"
          } ${sizeStyles[size]} ${showClear ? "pr-14" : ""}`}
        >
          {/* The choice, read after the field's name (a <Label> names the button). */}
          <span id={valueId} className="flex min-w-0 items-center gap-2">
            {summary ?? <span className="truncate text-ink-subtle">{placeholder}</span>}
          </span>
        </button>
        {showClear && (
          <button
            type="button"
            aria-label="Clear"
            onClick={clear}
            className="absolute right-8 top-1/2 inline-flex h-5 w-5 -translate-y-1/2 items-center justify-center rounded-sm text-ink-subtle hover:bg-surface-muted hover:text-ink focus-visible:outline-2 focus-visible:outline-accent"
          >
            <svg
              aria-hidden
              viewBox="0 0 16 16"
              fill="none"
              stroke="currentColor"
              strokeWidth={2}
              className="h-3 w-3"
            >
              <path strokeLinecap="round" d="M4 4l8 8M12 4l-8 8" />
            </svg>
          </button>
        )}
        <Chevron />
      </span>
    );
  };

  return (
    <Popover
      trigger={renderTrigger}
      open={open}
      onOpenChange={onOpenChange}
      align={align}
      side={side}
      disabled={disabled}
      label={ariaLabel ?? searchPlaceholder}
      panelClassName={panelClassName}
      onOpenFocus={(panel) =>
        panel.querySelector<HTMLElement>('input[role="combobox"]')?.focus({ preventScroll: true })
      }
    >
      {({ close }) => {
        closeRef.current = close;
        return (
          <div
            className="flex flex-col"
            style={{ width: Math.max(triggerWidth, MIN_PANEL_WIDTH), maxWidth: "100%" }}
          >
            <div className="flex items-center gap-2 border-b border-border px-3">
              <svg
                aria-hidden
                viewBox="0 0 16 16"
                fill="none"
                stroke="currentColor"
                strokeWidth={1.75}
                className="h-4 w-4 shrink-0 text-ink-subtle"
              >
                <circle cx="7" cy="7" r="4.5" />
                <path strokeLinecap="round" d="M10.5 10.5L14 14" />
              </svg>
              <input
                type="text"
                role="combobox"
                aria-label={searchPlaceholder}
                aria-expanded
                aria-controls={listboxId}
                aria-autocomplete="list"
                aria-activedescendant={
                  active !== null && enabled.some((o) => o.value === active)
                    ? optionId(active)
                    : undefined
                }
                placeholder={searchPlaceholder}
                value={query}
                onChange={(e) => onQuery(e.target.value)}
                onKeyDown={onSearchKeyDown}
                className="h-10 w-full min-w-0 bg-transparent text-sm text-ink placeholder:text-ink-subtle focus:outline-none"
              />
            </div>
            <div
              ref={listRef}
              id={listboxId}
              role="listbox"
              aria-label={ariaLabel ?? searchPlaceholder}
              aria-multiselectable={multiple || undefined}
              aria-busy={loading || undefined}
              className="relative max-h-72 overflow-y-auto overscroll-contain p-1"
            >
              {loading ? (
                <div className="px-2 py-6 text-center text-sm text-ink-muted">Loading…</div>
              ) : visible.length === 0 ? (
                <div className="px-2 py-6 text-center text-sm text-ink-muted">{emptyMessage}</div>
              ) : (
                groups.map((group, gi) => {
                  const headingId = `${listboxId}-g${gi}`;
                  return (
                    <div
                      key={gi}
                      role={group.name ? "group" : "presentation"}
                      aria-labelledby={group.name ? headingId : undefined}
                    >
                      {group.name && (
                        <div
                          id={headingId}
                          role="presentation"
                          className="px-2 pb-1 pt-2 text-xs text-ink-muted"
                        >
                          {group.name}
                        </div>
                      )}
                      {group.options.map((option) => {
                        const isSelected = chosen.has(option.value);
                        const isActive = option.value === active;
                        return (
                          <div
                            key={option.value}
                            id={optionId(option.value)}
                            role="option"
                            aria-selected={isSelected}
                            aria-disabled={option.disabled || undefined}
                            data-value={option.value}
                            data-active={isActive || undefined}
                            className={`flex cursor-pointer items-start gap-2 rounded-md px-2 py-1.5 text-sm ${
                              option.disabled
                                ? "cursor-not-allowed text-ink-subtle"
                                : isActive
                                  ? "bg-surface-muted text-ink forced-colors:outline forced-colors:outline-1"
                                  : "text-ink"
                            }`}
                          >
                            {multiple && <Checkbox checked={isSelected} />}
                            {option.icon && (
                              <span
                                aria-hidden
                                className="mt-0.5 inline-flex h-4 w-4 shrink-0 items-center justify-center text-ink-muted"
                              >
                                {option.icon}
                              </span>
                            )}
                            <span className="min-w-0 flex-1">
                              <span className="block truncate">{option.label}</span>
                              {option.description && (
                                <span className="mt-0.5 line-clamp-2 block text-xs text-ink-muted">
                                  {option.description}
                                </span>
                              )}
                            </span>
                            {!multiple && <Check visible={isSelected} />}
                          </div>
                        );
                      })}
                    </div>
                  );
                })
              )}
            </div>
            {multiple && selected.length > 0 && (
              <div className="flex items-center justify-between border-t border-border px-3 py-2 text-xs text-ink-muted">
                <span aria-live="polite">{selected.length} selected</span>
                <button
                  type="button"
                  onClick={clear}
                  className="rounded-sm text-ink-muted hover:text-ink focus-visible:outline-2 focus-visible:outline-accent"
                >
                  Clear
                </button>
              </div>
            )}
          </div>
        );
      }}
    </Popover>
  );
}
