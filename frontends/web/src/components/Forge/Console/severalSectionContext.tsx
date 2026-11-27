// Copyright (c) 2026 Reliant Labs

/**
 * Which project's section of a page across several projects a component is
 * in (severalProjectSections.tsx), and the page header that knows it. Its own
 * module, light on imports, so a page's frame (consolePageFrame.tsx) and the
 * page components the previews mount can read it without loading the
 * sections' hooks.
 */

import { createContext, useContext } from "react";

import Link from "@/components/forge-ui/link";
import PageHeader, { type PageHeaderAction, type PageHeaderProps } from "@/components/forge-ui/page_header";

import type { SeveralApp } from "./severalProjects";

export const InProjectSection = createContext<SeveralApp | null>(null);

/** The project whose section this is, or null outside a section. */
export function useInProjectSection(): SeveralApp | null {
  return useContext(InProjectSection);
}

/**
 * A console page's PageHeader. In one project's section of a page across
 * several, the page's title and sentence are above every section already, so
 * the section keeps only its facts, controls and actions (and nothing when it
 * has none).
 */
export function ConsolePageHeader(props: PageHeaderProps) {
  const section = useInProjectSection();
  if (!section) return <PageHeader {...props} />;
  const { titleAccessory, meta, controls, actions = [] } = props;
  if (!titleAccessory && !meta && !controls && actions.length === 0) return null;
  // No heading: the page's title is the one h1, and the section's project its h2.
  return (
    <div className="flex min-h-8 flex-wrap items-center justify-between gap-x-4 gap-y-2" data-testid="several-section-header">
      <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-sm text-ink-muted">
        {titleAccessory}
        {meta}
      </div>
      {(controls || actions.length > 0) && (
        <div className="flex shrink-0 flex-wrap items-center gap-2">
          {controls}
          {actions.map((action, index) => (
            <SectionAction key={index} action={action} />
          ))}
        </div>
      )}
    </div>
  );
}

const ACTION_VARIANT: Record<NonNullable<PageHeaderAction["variant"]>, string> = {
  primary: "border border-accent bg-accent text-on-accent hover:bg-accent-hover",
  secondary: "border border-border bg-surface text-ink hover:bg-ink/5",
  danger: "border border-border bg-surface text-danger-ink hover:bg-danger-surface",
};

/** A header action, drawn as PageHeader draws its own. */
function SectionAction({ action }: { action: PageHeaderAction }) {
  const cls = `inline-flex h-8 items-center gap-1.5 whitespace-nowrap rounded-md px-3 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50 ${ACTION_VARIANT[action.variant ?? "secondary"]}`;
  if (action.href) {
    return (
      <Link href={action.href} className={cls}>
        {action.icon}
        {action.label}
      </Link>
    );
  }
  return (
    <button type="button" onClick={action.onClick} disabled={action.disabled} className={cls}>
      {action.icon}
      {action.label}
    </button>
  );
}
