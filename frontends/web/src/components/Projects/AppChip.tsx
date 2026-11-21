// Copyright (c) 2025 Reliant Labs

/**
 * AppChip — one app on a project row: its name, a state dot, a globe when it
 * ships a static site, and a lock when the person cannot read the org's apps.
 * The tooltip says the state in words, how strongly the project's code is
 * linked to the app (services/projects/catalog.ts linkLabel), its
 * environments, and the two-repositories warning when there is one.
 *
 * A chip of an app the org has is a link into its console (appPath); one the
 * org does not have yet ("not deployed" from a forge.yaml nobody shipped) or
 * cannot be read is plain text. Inside a row that is itself the control (a
 * selector option) pass `interactive={false}`: the row picks, the chip only
 * describes.
 *
 * Status is a dot, never a fill (web-styling): blue `info` is live, a hollow
 * neutral ring is not deployed, a muted dot is unknown.
 */

import { Link } from "@tanstack/react-router";
import { Globe, Lock } from "lucide-react";

import Tooltip from "@/components/forge-ui/tooltip";
import { appPath } from "@/lib/paths";
import { cn } from "@/lib/utils";
import { chipStateLabel, linkLabel, type AppChipModel, type AppChipState } from "@/services/projects/catalog";

export interface AppChipProps {
  app: AppChipModel;
  /** Link into the app's console (default). False inside a row that is itself the control. */
  interactive?: boolean;
  className?: string;
}

const DOT: Record<Exclude<AppChipState, "no_access">, string> = {
  live: "bg-info",
  not_deployed: "border border-muted-foreground/70 bg-transparent",
  unavailable: "bg-muted-foreground/40",
};

function StateMark({ state }: { state: AppChipState }) {
  if (state === "no_access") return <Lock aria-hidden className="h-3 w-3 shrink-0" />;
  return <span aria-hidden className={cn("h-1.5 w-1.5 shrink-0 rounded-full", DOT[state])} />;
}

/** The chip's tooltip: what it is, how it is linked, where it runs. */
export function AppChipDetails({ app }: { app: AppChipModel }) {
  const link = linkLabel(app.link);
  return (
    <span className="flex flex-col gap-0.5 text-left">
      <span className="font-medium">
        {app.name}
        {app.isStaticSite ? " · static site" : ""}
      </span>
      <span>{chipStateLabel(app.state)}</span>
      {link ? <span>{link}</span> : null}
      {app.environments.length > 0 ? <span>Environments: {app.environments.join(", ")}</span> : null}
      {app.subpath ? <span>In {app.subpath}/</span> : null}
      {app.hosted?.warning ? <span>{app.hosted.warning}</span> : null}
    </span>
  );
}

export function AppChip({ app, interactive = true, className }: AppChipProps) {
  const label = `${app.name}: ${chipStateLabel(app.state).toLowerCase()}${app.isStaticSite ? ", static site" : ""}`;
  const body = (
    <>
      <StateMark state={app.state} />
      {app.isStaticSite ? <Globe aria-hidden className="h-3 w-3 shrink-0" /> : null}
      <span className="truncate">{app.name}</span>
    </>
  );
  const chipClass = cn(
    "inline-flex h-5 min-w-0 max-w-[12rem] items-center gap-1 rounded-md border border-border px-1.5 text-meta leading-none text-muted-foreground",
    className,
  );
  const linked = interactive && app.hosted !== undefined;
  return (
    <Tooltip content={<AppChipDetails app={app} />} focusable={false}>
      {linked ? (
        <Link
          to={appPath(app.name)}
          aria-label={label}
          data-testid="app-chip"
          data-app-state={app.state}
          className={cn(chipClass, "hover:bg-foreground/5 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring")}
        >
          {body}
        </Link>
      ) : (
        <span aria-label={label} role="img" data-testid="app-chip" data-app-state={app.state} className={chipClass}>
          {body}
        </span>
      )}
    </Tooltip>
  );
}
