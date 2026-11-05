import React, { useRef } from "react";

interface SearchInputProps {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  onSubmit?: () => void;
  autoFocus?: boolean;
  size?: "sm" | "md" | "lg";
  shortcutKey?: string;
}

const sizeStyles: Record<NonNullable<SearchInputProps["size"]>, { input: string; icon: string }> = {
  sm: { input: "h-8 pl-8 pr-8 text-sm", icon: "h-3.5 w-3.5 left-2.5" },
  md: { input: "h-10 pl-10 pr-10 text-sm", icon: "h-4 w-4 left-3" },
  lg: { input: "h-12 pl-12 pr-12 text-base", icon: "h-5 w-5 left-3.5" },
};

export default function SearchInput({
  value,
  onChange,
  placeholder = "Search...",
  onSubmit,
  autoFocus,
  size = "md",
  shortcutKey,
}: SearchInputProps) {
  const inputRef = useRef<HTMLInputElement>(null);
  const s = sizeStyles[size];

  React.useEffect(() => {
    if (!shortcutKey) return;
    function handleKeyDown(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key === shortcutKey) {
        e.preventDefault();
        inputRef.current?.focus();
      }
    }
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [shortcutKey]);

  return (
    <div className="relative">
      <svg
        className={`pointer-events-none absolute top-1/2 -translate-y-1/2 text-ink-subtle ${s.icon}`}
        fill="none"
        viewBox="0 0 24 24"
        strokeWidth={2}
        stroke="currentColor"
      >
        <path
          strokeLinecap="round"
          strokeLinejoin="round"
          d="M21 21l-5.197-5.197m0 0A7.5 7.5 0 105.196 5.196a7.5 7.5 0 0010.607 10.607z"
        />
      </svg>
      <input
        ref={inputRef}
        type="text"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => e.key === "Enter" && onSubmit?.()}
        placeholder={placeholder}
        autoFocus={autoFocus}
        className={`w-full rounded-lg border border-border-strong bg-surface focus:border-accent focus:outline-none focus:ring-1 focus:ring-accent ${s.input}`}
      />
      {value && (
        // Icon-only control: aria-label is the only name it can have, and
        // type="button" keeps a clear from submitting a surrounding form.
        <button
          type="button"
          aria-label="Clear search"
          onClick={() => onChange("")}
          className="absolute right-2.5 top-1/2 -translate-y-1/2 rounded-md p-0.5 text-ink-subtle hover:text-ink-muted"
        >
          <svg
            className="h-4 w-4"
            fill="none"
            viewBox="0 0 24 24"
            strokeWidth={2}
            stroke="currentColor"
          >
            <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
          </svg>
        </button>
      )}
      {shortcutKey && !value && (
        <div className="pointer-events-none absolute right-2.5 top-1/2 -translate-y-1/2">
          <kbd className="rounded border border-border bg-surface-muted px-1.5 py-0.5 text-[10px] font-medium text-ink-subtle">
            ⌘{shortcutKey.toUpperCase()}
          </kbd>
        </div>
      )}
    </div>
  );
}
