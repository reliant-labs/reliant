interface ToggleSwitchProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label?: string;
  description?: string;
  disabled?: boolean;
  size?: "sm" | "md" | "lg";
}

const sizeStyles: Record<string, { track: string; thumb: string; translate: string }> = {
  sm: { track: "h-4 w-7", thumb: "h-3 w-3", translate: "translate-x-3" },
  md: { track: "h-5 w-9", thumb: "h-4 w-4", translate: "translate-x-4" },
  lg: { track: "h-6 w-11", thumb: "h-5 w-5", translate: "translate-x-5" },
};

export default function ToggleSwitch({
  checked,
  onChange,
  label,
  description,
  disabled,
  size = "md",
}: ToggleSwitchProps) {
  const s = sizeStyles[size];

  return (
    <label
      className={`flex items-start gap-3 ${
        disabled ? "cursor-not-allowed opacity-50" : "cursor-pointer"
      }`}
    >
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        disabled={disabled}
        onClick={() => !disabled && onChange(!checked)}
        className={`relative inline-flex flex-shrink-0 rounded-full transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-accent focus:ring-offset-2 ${
          s.track
        } ${checked ? "bg-accent" : "bg-surface-muted"}`}
      >
        <span
          className={`inline-block rounded-full bg-surface shadow-sm ring-0 transition-transform duration-200 ease-in-out ${
            s.thumb
          } ${checked ? s.translate : "translate-x-0.5"} mt-[2px] ml-[1px]`}
        />
      </button>
      {(label || description) && (
        <div>
          {label && <span className="text-sm font-medium text-ink">{label}</span>}
          {description && <p className="text-sm text-ink-muted">{description}</p>}
        </div>
      )}
    </label>
  );
}
