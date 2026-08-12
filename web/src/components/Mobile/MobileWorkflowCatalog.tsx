/**
 * `/m/workflows` — browse available workflows.
 *
 * Deliberately not `WorkflowHub`: that component is the desktop
 * authoring surface (2200+ lines, builder-integrated, and pulls in Monaco
 * transitively through its preset/param editing panel). This reads the same
 * `useWorkflows()` list the desktop hub does and renders a touch list,
 * matching `MobileNewChat`'s existing workflow-picker pattern.
 *
 * ## Why the list is sectioned by origin
 *
 * A flat alphabetical list of ~23 two-line rows is the densest screen on this
 * surface and had nothing for the eye to land on — every row the same height,
 * the same weight, the same leading icon. Origin is the one axis that is both
 * stable and meaningful to a user scanning for something ("the one I wrote"
 * versus "one that ships with the app"), so it becomes the section boundary,
 * with the user's own workflows first because they are the few among the many.
 * Ordering stays alphabetical *within* a section, so nothing moves
 * unpredictably between renders.
 *
 * ## Why rows also carry a per-workflow icon
 *
 * Sectioning by origin does nothing for a project with only builtins — every
 * row in the one remaining section still shared the same origin icon, so the
 * list still read as 23 identical rows. `iconForWorkflow` keys off the
 * workflow's name for a small, keyword-matched icon (an audit workflow gets a
 * magnifying glass, a migration gets a branch icon, and so on), falling back
 * to the section's origin icon for anything that matches nothing. This gives
 * the eye scan anchors without inventing a taxonomy the workflow list doesn't
 * actually have.
 *
 * ## Why the second line leads with the description
 *
 * The previous order — step count first, description second — meant every
 * row led with the least distinguishing fact about it ("3 steps" says nothing
 * about what a workflow does) and buried the one line that actually helps
 * someone pick. Description now leads; step count moves to a trailing badge,
 * which keeps it visible without letting it compete with the description for
 * the reader's first glance.
 */

import { Link } from "@tanstack/react-router";
import {
  BookOpen,
  Bot,
  ChevronRight,
  FileText,
  FolderGit2,
  GitBranch,
  GitFork,
  Hammer,
  ListChecks,
  Loader2,
  PenLine,
  PresentationIcon,
  Route,
  Search,
  Workflow as WorkflowIcon,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { useWorkflows, type WorkflowDef } from "../../store/globalDataStore";
import { usePreferencesStore } from "../../store/preferencesStore";
import { normalizeWorkflowRef, getWorkflowDisplayName } from "../workflow/useWorkflowInputs";
import { MobileMenuButton } from "./MobileMenuButton";
import {
  MOBILE_ROW,
  MobileCardGroup,
  MobileEmptyState,
  MobileRowIcon,
  MobileScreenBody,
  MobileScreenHeader,
} from "./MobileChrome";

type OriginId = "user" | "project" | "builtin";

const ORIGINS: { id: OriginId; label: string; icon: LucideIcon }[] = [
  { id: "user", label: "Your workflows", icon: PenLine },
  { id: "project", label: "Project", icon: FolderGit2 },
  { id: "builtin", label: "Built in", icon: WorkflowIcon },
];

// Keyword → icon, checked in order against the normalized workflow name.
// First match wins. Not exhaustive by design — a workflow that matches
// nothing falls back to its section's origin icon, which is exactly what
// every row showed before this existed.
const NAME_ICON_RULES: Array<{ test: RegExp; icon: LucideIcon }> = [
  { test: /audit/, icon: Search },
  { test: /migrat/, icon: GitBranch },
  { test: /review|get-it-right/, icon: ListChecks },
  { test: /parallel|compete/, icon: GitFork },
  { test: /router|relay/, icon: Route },
  { test: /agent|ralph|ring|bmad/, icon: Bot },
  { test: /build|env-setup/, icon: Hammer },
  { test: /pitch|deck|presentation/, icon: PresentationIcon },
  { test: /blog|content|landing|checklist|markdown/, icon: FileText },
  { test: /scope|discovery/, icon: BookOpen },
];

function iconForWorkflow(workflow: WorkflowDef, fallback: LucideIcon): LucideIcon {
  const name = normalizeWorkflowRef(workflow.name);
  return NAME_ICON_RULES.find((rule) => rule.test.test(name))?.icon ?? fallback;
}

/**
 * `source` is absent on refs that reach the store through paths that never set
 * it, so the `builtin://` / `workflow://` prefix on the name is the fallback —
 * it is the same signal `normalizeWorkflowRef` keys off and is always present.
 */
function originOf(workflow: WorkflowDef): OriginId {
  if (workflow.source === "user" || workflow.source === "project") {
    return workflow.source;
  }
  if (workflow.source === "builtin") return "builtin";
  return workflow.name.startsWith("workflow://") ? "user" : "builtin";
}

export function MobileWorkflowCatalog() {
  const { workflows, loading } = useWorkflows();
  const isWorkflowHidden = usePreferencesStore((s) => s.isWorkflowHidden);

  const seen = new Set<string>();
  const visibleWorkflows = workflows
    .filter((w) => {
      if (isWorkflowHidden(w.name)) return false;
      const key = normalizeWorkflowRef(w.name).toLowerCase().trim();
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    })
    .sort((a, b) => a.name.localeCompare(b.name));

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* The menu button is the only way off a top-level destination — without
          it this screen is reachable from the drawer and then inescapable
          except via the browser's back gesture. */}
      <MobileScreenHeader title="Workflows" leading={<MobileMenuButton />} />

      {loading && visibleWorkflows.length === 0 ? (
        <div className="flex flex-1 items-center justify-center">
          <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
        </div>
      ) : visibleWorkflows.length === 0 ? (
        <MobileEmptyState
          icon={WorkflowIcon}
          title="No workflows"
          description="Workflows you create on desktop, and the ones that ship with Reliant, appear here."
        />
      ) : (
        <MobileScreenBody>
          {ORIGINS.map(({ id, label, icon }) => {
            const rows = visibleWorkflows.filter((w) => originOf(w) === id);
            if (rows.length === 0) return null;
            return (
              <MobileCardGroup
                key={id}
                label={`${label} · ${rows.length}`}
              >
                {rows.map((workflow) => (
                  <Link
                    key={workflow.name}
                    to="/m/workflows/$workflowName"
                    params={{ workflowName: workflow.name }}
                    className={MOBILE_ROW}
                  >
                    <MobileRowIcon icon={iconForWorkflow(workflow, icon)} />
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-sm font-medium text-foreground">
                        {getWorkflowDisplayName(workflow.name, true)}
                      </div>
                      {/* Description leads — it's the one line that actually
                          distinguishes a row. A workflow with no description
                          just shows the step count alone rather than an empty
                          line, so the row stays honest instead of padding
                          itself with a placeholder. */}
                      {workflow.description ? (
                        <div className="truncate text-xs text-muted-foreground">
                          {workflow.description}
                        </div>
                      ) : workflow.step_count > 0 ? (
                        <div className="truncate text-xs text-muted-foreground">
                          {workflow.step_count} step
                          {workflow.step_count === 1 ? "" : "s"}
                        </div>
                      ) : null}
                    </div>
                    {workflow.step_count > 0 && workflow.description && (
                      <span className="shrink-0 rounded-full bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary">
                        {workflow.step_count}
                      </span>
                    )}
                    <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
                  </Link>
                ))}
              </MobileCardGroup>
            );
          })}
        </MobileScreenBody>
      )}
    </div>
  );
}
