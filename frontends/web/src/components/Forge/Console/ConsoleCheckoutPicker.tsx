// Copyright (c) 2026 Reliant Labs

/**
 * WHICH CHECKOUT BACKS THIS CONSOLE, asked. An app's console is addressed by
 * its forge.yaml name (`/o/<org>/apps/<name>`), and several of the person's
 * projects can declare one name: two checkouts of one repo, or two repos
 * whose apps share a name (every scaffold called `my-app`). A link that knows
 * which carries `?project=<Reliant project id>` (lib/paths appPath); one that
 * does not lands here rather than on whichever project happened to be listed
 * first (consoleHost.ts consoleCheckout).
 *
 * Choosing writes the answer into the URL (`?project=`, replace), so a
 * refresh, Back and a copied link keep it, and the console's own links carry
 * it on (forgeConsoleRoutes.tsx, retainSearchParams).
 */

import { useNavigate } from "@tanstack/react-router";

import type { Project } from "@/store/projectStore";

export function ConsoleCheckoutPicker({ app, candidates }: { app: string; candidates: readonly Project[] }) {
  const navigate = useNavigate();
  const choose = (project: Project) =>
    void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, project: project.id }), replace: true } as never);
  return (
    <div data-testid="console-checkout-picker" className="mx-auto mt-8 max-w-md overflow-hidden rounded-xl border border-border bg-surface">
      <div className="p-4 pb-2">
        <h2 className="text-base font-semibold text-ink">Which {app}?</h2>
        <p className="mt-1 text-xs text-ink-muted">
          {candidates.length} of your projects declare the app <span className="font-medium text-ink">{app}</span>. Choose
          the one this console reads its code, chats and machines from.
        </p>
      </div>
      <ul className="divide-y divide-border">
        {candidates.map((project) => (
          <li key={project.id}>
            <button
              type="button"
              onClick={() => choose(project)}
              data-testid={`console-checkout-${project.id}`}
              className="flex w-full flex-col items-start gap-0.5 px-4 py-2.5 text-left hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <span className="text-ui font-medium text-ink">{project.name}</span>
              {project.path ? <span className="truncate text-meta text-ink-muted">{project.path}</span> : null}
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}
