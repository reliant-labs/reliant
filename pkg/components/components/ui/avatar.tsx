interface AvatarProps {
  src?: string;
  alt?: string;
  name?: string;
  size?: "xs" | "sm" | "md" | "lg" | "xl";
  status?: "online" | "offline" | "busy" | "away";
}

const sizeStyles: Record<
  NonNullable<AvatarProps["size"]>,
  { container: string; text: string; status: string }
> = {
  xs: {
    container: "h-6 w-6",
    text: "text-[10px]",
    status: "h-1.5 w-1.5 ring-1",
  },
  sm: { container: "h-8 w-8", text: "text-xs", status: "h-2 w-2 ring-[1.5px]" },
  md: { container: "h-10 w-10", text: "text-sm", status: "h-2.5 w-2.5 ring-2" },
  lg: { container: "h-12 w-12", text: "text-base", status: "h-3 w-3 ring-2" },
  xl: { container: "h-16 w-16", text: "text-lg", status: "h-3.5 w-3.5 ring-2" },
};

const statusColors: Record<NonNullable<AvatarProps["status"]>, string> = {
  online: "bg-success",
  offline: "bg-ink-subtle",
  busy: "bg-danger",
  away: "bg-warning",
};

function getInitials(name: string): string {
  return name
    .split(/\s+/)
    .map((w) => w[0])
    .filter(Boolean)
    .slice(0, 2)
    .join("")
    .toUpperCase();
}

// Deterministic per-name identity colors, drawn from the SEMANTIC tint pairs
// (`<role>-surface` fill + `<role>-ink` text) rather than raw palette hues.
// Each pair is held to 4.5:1 in light AND dark by the scaffold's palette
// contract test, so initials stay legible at the 10px `xs` size, and a rebrand
// or dark mode moves avatars with everything else. Raw `bg-*-500` with white
// initials measured 2.89:1.
const initialsColors = [
  "bg-accent-surface text-accent-ink",
  "bg-success-surface text-success-ink",
  "bg-warning-surface text-warning-ink",
  "bg-danger-surface text-danger-ink",
  "bg-surface-sunken text-ink",
] as const;

function initialsColor(name: string): string {
  const hash = [...name].reduce((a, c) => a + c.charCodeAt(0), 0);
  // Index is provably in-bounds, but noUncheckedIndexedAccess can't see
  // that — the fallback keeps the lookup total.
  return initialsColors[hash % initialsColors.length] ?? "bg-surface-sunken text-ink";
}

export default function Avatar({ src, alt, name, size = "md", status }: AvatarProps) {
  const s = sizeStyles[size];

  return (
    <div className={`relative inline-flex flex-shrink-0 ${s.container}`}>
      {src ? (
        // Framework-agnostic library component (also ships to Vite SPAs); wrap
        // with next/image at the call site if you want Next's optimization.
        // The Next.js scaffold's eslint config exempts src/components/ui/**
        // from @next/next/no-img-element. That exemption lives there and NOT
        // as an in-file directive, because a directive naming a
        // framework-specific rule is a hard error in any tree whose config
        // does not load that plugin (a Vite SPA does not load
        // @next/eslint-plugin-next).
        <img
          src={src}
          alt={alt ?? name ?? "Avatar"}
          className={`${s.container} rounded-full object-cover`}
        />
      ) : name ? (
        <div
          className={`${
            s.container
          } flex items-center justify-center rounded-full ${initialsColor(name)}`}
        >
          <span className={`font-medium leading-none ${s.text}`}>{getInitials(name)}</span>
        </div>
      ) : (
        <div className={`${s.container} flex items-center justify-center rounded-full bg-border`}>
          <svg
            className="h-1/2 w-1/2 text-ink-muted"
            fill="none"
            viewBox="0 0 24 24"
            strokeWidth={1.5}
            stroke="currentColor"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              d="M15.75 6a3.75 3.75 0 11-7.5 0 3.75 3.75 0 017.5 0zM4.501 20.118a7.5 7.5 0 0114.998 0A17.933 17.933 0 0112 21.75c-2.676 0-5.216-.584-7.499-1.632z"
            />
          </svg>
        </div>
      )}
      {status && (
        <span
          className={`absolute bottom-0 right-0 block rounded-full ring-surface ${s.status} ${statusColors[status]}`}
        />
      )}
    </div>
  );
}
