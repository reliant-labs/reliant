import React from "react";

// Canonical variants. `danger` and `default` are accepted aliases for
// `error` and `neutral` respectively — many existing codebases (and
// alternate design systems) name the destructive variant `danger` and
// the no-color variant `default`. Accepting both spellings means
// ports don't need an adapter table at every call site.
type BadgeVariant = "success" | "warning" | "error" | "info" | "neutral";
type BadgeVariantAlias = "danger" | "default";
type BadgeSize = "sm" | "md" | "lg";

interface BadgeProps {
  label: string;
  variant?: BadgeVariant | BadgeVariantAlias;
  size?: BadgeSize;
  dot?: boolean;
  removable?: boolean;
  onRemove?: () => void;
}

const variantStyles: Record<BadgeVariant, string> = {
  success: "bg-success-surface text-success-ink ring-success-border",
  warning: "bg-warning-surface text-warning-ink ring-warning-border",
  error: "bg-danger-surface text-danger-ink ring-danger-border",
  info: "bg-accent-surface text-accent-ink ring-accent-border",
  neutral: "bg-surface-muted text-ink-muted ring-border",
};

const dotStyles: Record<BadgeVariant, string> = {
  success: "bg-success",
  warning: "bg-warning",
  error: "bg-danger",
  info: "bg-accent",
  neutral: "bg-ink-muted",
};

const sizeStyles: Record<BadgeSize, string> = {
  // text-2xs, not an arbitrary px value: a hardcoded size opts this badge out
  // of the font-size preference permanently, which is what
  // fontScaleContract.test.ts guards. See tailwind.config.js's scale comment.
  sm: "px-1.5 py-0.5 text-2xs",
  md: "px-2 py-0.5 text-xs",
  lg: "px-2.5 py-1 text-sm",
};

function resolveVariant(v: BadgeVariant | BadgeVariantAlias): BadgeVariant {
  if (v === "danger") return "error";
  if (v === "default") return "neutral";
  return v;
}

export default function Badge({
  label,
  variant = "neutral",
  size = "md",
  dot,
  removable,
  onRemove,
}: BadgeProps) {
  const v = resolveVariant(variant);
  return (
    <span
      className={`inline-flex items-center gap-1 rounded-full font-medium ring-1 ring-inset ${variantStyles[v]} ${sizeStyles[size]}`}
    >
      {dot && <span className={`h-1.5 w-1.5 rounded-full ${dotStyles[v]}`} />}
      {label}
      {removable && (
        // The icon is the whole button, so the name has to come from
        // aria-label — and it names WHICH badge, because a row of chips
        // otherwise announces as several identical "Remove" buttons.
        <button
          type="button"
          aria-label={`Remove ${label}`}
          onClick={onRemove}
          className="ml-0.5 inline-flex h-3.5 w-3.5 items-center justify-center rounded-full hover:bg-black/10"
        >
          <svg
            className="h-2.5 w-2.5"
            fill="none"
            viewBox="0 0 24 24"
            strokeWidth={3}
            stroke="currentColor"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              d="M6 18L18 6M6 6l12 12"
            />
          </svg>
        </button>
      )}
    </span>
  );
}
