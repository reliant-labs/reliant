import { cn } from "../../lib/utils";

interface ToggleProps {
  id?: string;
  checked: boolean;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
  label?: string;
  srLabel?: string;
  className?: string;
}

export function Toggle({
  id,
  checked,
  onChange,
  disabled = false,
  label,
  srLabel,
  className,
}: ToggleProps) {
  return (
    <button
      id={id}
      role="switch"
      aria-checked={checked}
      aria-label={srLabel || label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn(
        "relative inline-flex h-6 w-11 items-center rounded-full transition-all duration-200 focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 border-2",
        checked
          ? "bg-primary border-primary shadow-lg"
          : "bg-gray-300 dark:bg-gray-700 border-gray-400 dark:border-gray-600 hover:bg-gray-400 dark:hover:bg-gray-600 shadow-sm",
        disabled && "opacity-50 cursor-not-allowed",
        className
      )}
    >
      <span className="sr-only">{srLabel || label || "Toggle"}</span>
      <span
        className={cn(
          "inline-block h-4 w-4 transform rounded-full transition-all duration-200 shadow-md",
          checked
            ? "translate-x-5 bg-[hsl(var(--primary-foreground))]"
            : "translate-x-0.5 bg-gray-600 dark:bg-gray-300"
        )}
      />
    </button>
  );
}
