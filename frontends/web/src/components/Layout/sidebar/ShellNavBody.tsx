// Copyright (c) 2026 Reliant Labs

/**
 * The nav under the switcher (U6c, prototype): the variant `?nav=` names
 * (navConfig/variant.ts), drawn by the one renderer against the route and the
 * shell's scope, with the open app's console model (alerts, the analytics
 * hub, its env scope, the rows other modules register) when there is an app.
 */

import { useMemo } from "react";

import { withSearch } from "@/lib/paths";
import { cn } from "@/lib/utils";

import { consolePageOf, orgPageOf } from "../../Forge/Console/paths";
import type { GroupedNavInput } from "../../Forge/Console/groupedNav";
import { useConsoleNavModel } from "../../Forge/Console/useConsoleNavModel";
import { NAV_CONFIGS, NAV_PARAM, NAV_VARIANTS, useNavVariant, type NavCtx, type NavVariant } from "./navConfig";
import { NavRenderer, type NavSlots } from "./NavRenderer";
import type { ShellProject } from "./shellProject";

export interface ShellNavBodyProps {
  shell: ShellProject;
  pathname: string;
  search: Record<string, unknown>;
  slots: NavSlots;
  onNavigate: (href: string) => void;
}

export function ShellNavBody(props: ShellNavBodyProps) {
  return props.shell.app ? <NavWithConsole app={props.shell.app} {...props} /> : <NavBody {...props} console={null} />;
}

function NavWithConsole({ app, ...props }: ShellNavBodyProps & { app: string }) {
  const model = useConsoleNavModel(app, props.pathname, props.search);
  return <NavBody {...props} console={model} />;
}

function NavBody({ shell, pathname, search, slots, onNavigate, console }: ShellNavBodyProps & { console: GroupedNavInput | null }) {
  const { variant, explicit } = useNavVariant(search);
  const ctx = useMemo<NavCtx>(
    () => ({
      variant,
      pathname,
      search,
      mode: shell.mode,
      projectId: shell.projectId,
      projectName: shell.name,
      app: shell.app,
      console,
      page: consolePageOf(pathname)?.page ?? null,
      orgPage: orgPageOf(pathname),
    }),
    [variant, pathname, search, shell.mode, shell.projectId, shell.name, shell.app, console],
  );
  return (
    <NavRenderer
      config={NAV_CONFIGS[variant]}
      ctx={ctx}
      explicitVariant={explicit}
      slots={slots}
      onNavigate={onNavigate}
      footerExtra={<VariantPicker variant={variant} pathname={pathname} search={search} onNavigate={onNavigate} />}
    />
  );
}

/** The preview's way to compare the variants: the same page under A, B or C (`?nav=`, replacing). */
function VariantPicker({
  variant,
  pathname,
  search,
  onNavigate,
}: {
  variant: NavVariant;
  pathname: string;
  search: Record<string, unknown>;
  onNavigate: (href: string) => void;
}) {
  return (
    <div className="flex items-center gap-1 px-2 pt-1 text-meta text-muted-foreground" role="group" aria-label="Nav variant" data-testid="nav-variant-picker">
      <span className="mr-auto">Nav</span>
      {NAV_VARIANTS.map((candidate) => (
        <button
          key={candidate}
          type="button"
          title={NAV_CONFIGS[candidate].title}
          aria-pressed={candidate === variant}
          data-testid={`nav-variant-${candidate}`}
          onClick={() => onNavigate(withSearch(pathname, { ...search, [NAV_PARAM]: candidate }))}
          className={cn(
            "h-5 rounded px-1.5 font-medium uppercase transition-colors hover:bg-foreground/5 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
            candidate === variant && "bg-foreground/[0.08] text-foreground",
          )}
        >
          {candidate}
        </button>
      ))}
    </div>
  );
}
