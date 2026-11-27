// Copyright (c) 2026 Reliant Labs

/**
 * The note an aggregate page shows where it differs from one project's page
 * (owner, 2026-11-27): "Select one project to browse its package and file
 * tree." It is quiet on purpose — a line, not a banner — and its action opens
 * the choice of the projects in scope: picking one opens the same page for
 * that project alone (drillDown.ts).
 */

import { Info } from "lucide-react";

import Button from "@/components/forge-ui/button";
import DropdownMenu from "@/components/forge-ui/dropdown_menu";

import { ProjectMark } from "../../../Layout/ProjectMark";
import type { ConsolePage } from "../paths";
import type { SeveralApp } from "../severalProjects";
import { useDrillDown } from "./drillDown";

export interface DrillDownHintProps {
  /** What one project's page shows that this one does not; completes "Select one project to …". */
  purpose: string;
  /** The projects to choose from: the ones in scope. */
  projects: readonly SeveralApp[];
  /** The page the choice opens, for the chosen project alone. */
  page: ConsolePage;
}

export function DrillDownHint({ purpose, projects, page }: DrillDownHintProps) {
  const drillDown = useDrillDown();
  return (
    <div
      role="note"
      className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 rounded-xl border border-dashed border-border px-4 py-2.5 text-ui text-ink-muted"
      data-testid="drill-down-hint"
    >
      <span className="flex min-w-0 items-center gap-2">
        <Info className="size-4 shrink-0" aria-hidden="true" />
        <span className="min-w-0">Select one project to {purpose}.</span>
      </span>
      {projects.length > 0 && (
        <DropdownMenu
          label="Choose a project"
          align="right"
          menuClassName="min-w-[13rem] max-h-80 overflow-y-auto"
          groups={[
            {
              items: projects.map((project) => ({
                label: project.name,
                icon: <ProjectMark name={project.name} />,
                hint: project.app !== project.name ? project.app : undefined,
                onClick: () => drillDown(project, page),
                testId: `drill-down-choose-${project.app}`,
              })),
            },
          ]}
          trigger={(props) => (
            <Button {...props} variant="ghost" size="sm" data-testid="drill-down-choose">
              Choose a project
            </Button>
          )}
        />
      )}
    </div>
  );
}
