import { useEffect, useMemo, useState } from "react";
import { cn } from "../../lib/utils";

export const MCP_TILE_COLORS = [
  "from-sky-500/30 to-blue-500/20",
  "from-emerald-500/30 to-teal-500/20",
  "from-violet-500/30 to-fuchsia-500/20",
  "from-amber-500/30 to-orange-500/20",
  "from-rose-500/30 to-pink-500/20",
  "from-cyan-500/30 to-indigo-500/20",
];

const DISCOVER_MCP_ICON_URLS: Record<string, string> = {
  "chrome-devtools":
    "https://raw.githubusercontent.com/ChromeDevTools/devtools-logo/master/logos/svg/chrome-devtools-square-64.svg",
  context7: "https://context7.com/brand/context7-icon-dark.svg",
  supabase:
    "https://raw.githubusercontent.com/supabase/supabase/master/packages/common/assets/images/supabase-logo-icon.svg",
  linear: "https://raw.githubusercontent.com/linear/linear/master/docs/logo.svg",
  serena: "https://raw.githubusercontent.com/oraios/serena/main/resources/serena-logo.svg",
  github: "https://cdn.simpleicons.org/github/181717",
  sqlite: "https://cdn.simpleicons.org/sqlite/003B57",
  postgres: "https://cdn.simpleicons.org/postgresql/4169E1",
  slack: "https://cdn.jsdelivr.net/npm/simple-icons@v11/icons/slack.svg",
  sentry: "https://cdn.simpleicons.org/sentry/362D59",
  aws: "https://cdn.jsdelivr.net/npm/simple-icons@v11/icons/amazonaws.svg",
  docker: "https://cdn.simpleicons.org/docker/2496ED",
};

function normalizeName(value: string): string {
  return (value || "").trim().toLowerCase();
}

export function mcpVisualSeed(...parts: string[]): number {
  const input = parts.join("|").toLowerCase();
  let hash = 0;
  for (let i = 0; i < input.length; i += 1) {
    hash = (hash * 31 + input.charCodeAt(i)) >>> 0;
  }
  return hash;
}

export function getMcpAvatarColorClass(...parts: string[]): string {
  const normalized = parts.filter(Boolean);
  const seed = mcpVisualSeed(...(normalized.length > 0 ? normalized : ["mcp"]));
  return MCP_TILE_COLORS[seed % MCP_TILE_COLORS.length];
}

export function getMcpInitials(name: string): string {
  const words = (name || "")
    .replace(/[^a-zA-Z0-9]+/g, " ")
    .trim()
    .split(/\s+/)
    .filter(Boolean);

  const initials = words
    .slice(0, 2)
    .map((word) => word[0] || "")
    .join("")
    .toUpperCase();

  return initials || "M";
}

export function getMcpIconUrl(name: string): string | undefined {
  return DISCOVER_MCP_ICON_URLS[normalizeName(name)] || undefined;
}

export function buildMcpAvatarContainerClassName({
  hasImageIcon,
  colorClass,
  className,
}: {
  hasImageIcon: boolean;
  colorClass: string;
  className?: string;
}): string {
  return cn(
    "h-11 w-11 rounded-xl border border-border/70 flex items-center justify-center text-sm font-semibold shrink-0 overflow-hidden",
    hasImageIcon ? "bg-white/95" : `bg-gradient-to-br ${colorClass} text-foreground/90`,
    className,
  );
}

interface MCPAvatarProps {
  name: string;
  iconSrc?: string;
  colorSeedParts?: string[];
  alt?: string;
  className?: string;
}

export function MCPAvatar({ name, iconSrc, colorSeedParts, alt, className }: MCPAvatarProps) {
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    setFailed(false);
  }, [iconSrc]);

  const initials = useMemo(() => getMcpInitials(name), [name]);
  const colorClass = useMemo(
    () => getMcpAvatarColorClass(...(colorSeedParts && colorSeedParts.length > 0 ? colorSeedParts : [name])),
    [colorSeedParts, name],
  );

  const hasImageIcon = Boolean(iconSrc && !failed);

  return (
    <div className={buildMcpAvatarContainerClassName({ hasImageIcon, colorClass, className })}>
      {hasImageIcon ? (
        <img
          src={iconSrc}
          alt={alt || `${name} icon`}
          className="h-full w-full object-contain p-1.5"
          loading="lazy"
          onError={() => setFailed(true)}
        />
      ) : (
        <span className="leading-none">{initials}</span>
      )}
    </div>
  );
}
