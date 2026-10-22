import { forwardRef, type ReactNode, type InputHTMLAttributes } from "react";
import { Search, X, Loader2 } from "lucide-react";
import { cn } from "../../../lib/utils";

interface SidebarInputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, "onChange"> {
  /** Current value */
  value: string;
  /** Callback when value changes */
  onChange: (value: string) => void;
  /** Left icon to display */
  leftIcon?: ReactNode;
  /** Show loading spinner as left icon */
  isLoading?: boolean;
  /** Show clear button when value is present */
  showClear?: boolean;
  /** Right side content (e.g., character count) */
  rightContent?: ReactNode;
  /** Wrapper className (for padding/border container) */
  wrapperClassName?: string;
}

/**
 * Unified input component for right sidebar.
 * Used for search inputs, commit messages, etc.
 */
export const SidebarInput = forwardRef<HTMLInputElement, SidebarInputProps>(
  (
    {
      value,
      onChange,
      leftIcon,
      isLoading = false,
      showClear = true,
      rightContent,
      placeholder,
      className,
      wrapperClassName,
      disabled,
      ...props
    },
    ref
  ) => {
    const hasLeftIcon = leftIcon || isLoading;
    const hasRightContent = rightContent || (showClear && value);

    return (
      <div className={cn("relative w-full", wrapperClassName)}>
        {/* Left icon */}
        {hasLeftIcon && (
          <div className="absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none">
            {isLoading ? (
              <Loader2 className="w-3.5 h-3.5 text-primary animate-spin" />
            ) : (
              <span className="text-muted-foreground">{leftIcon}</span>
            )}
          </div>
        )}

        {/* Input */}
        <input
          ref={ref}
          type="text"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={placeholder}
          disabled={disabled}
          className={cn(
            "w-full py-1.5 text-xs",
            "rounded-md border border-border bg-background/80 shadow-inner",
            "focus:outline-none focus:ring-1 focus:ring-primary/50 focus:border-primary/50",
            "placeholder:text-muted-foreground/50",
            "disabled:opacity-50 disabled:cursor-not-allowed",
            "transition-all",
            hasLeftIcon ? "pl-8" : "pl-3",
            hasRightContent ? "pr-8" : "pr-3",
            className
          )}
          {...props}
        />

        {/* Right content area */}
        {hasRightContent && (
          <div className="absolute right-2 top-1/2 -translate-y-1/2 flex items-center gap-1">
            {rightContent}
            {showClear && value && !rightContent && (
              <button
                type="button"
                onClick={() => onChange("")}
                className="p-0.5 rounded hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
                aria-label="Clear"
                tabIndex={-1}
              >
                <X className="w-3.5 h-3.5" />
              </button>
            )}
          </div>
        )}
      </div>
    );
  }
);

SidebarInput.displayName = "SidebarInput";

/**
 * Pre-configured search input variant
 */
interface SidebarSearchInputProps extends Omit<SidebarInputProps, "leftIcon" | "showClear"> {
  /** Show loading spinner instead of search icon */
  isSearching?: boolean;
}

export function SidebarSearchInput({
  isSearching = false,
  placeholder = "Search...",
  ...props
}: SidebarSearchInputProps) {
  return (
    <SidebarInput
      leftIcon={<Search className="w-3.5 h-3.5" />}
      isLoading={isSearching}
      showClear
      placeholder={placeholder}
      {...props}
    />
  );
}