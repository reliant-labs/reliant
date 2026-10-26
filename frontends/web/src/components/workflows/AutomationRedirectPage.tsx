// Copyright (c) 2025 Reliant Labs

/**
 * /workflows/automations/$triggerId — a retired page kept as a redirect.
 *
 * An automation used to have a page of its own. It is now a trigger of its
 * workflow, configured on that workflow's page (the Triggers card), so a link
 * that names only the trigger id — an old bookmark, a notification, a pasted
 * URL — is resolved here: the trigger names its workflow and project, and the
 * page replaces itself with that trigger's row (automationHref).
 */

import { useEffect } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";

import { isTriggerNotFound, triggerErrorMessage } from "@/hooks/trigger-model";
import { useTrigger } from "@/hooks/trigger-queries";
import { automationHref, WORKFLOWS_LIBRARY_PATH } from "@/lib/workflowsArea";
import Card from "../forge-ui/card";
import Button from "@/components/forge-ui/button";

export function AutomationRedirectPage() {
  const { triggerId } = useParams({ strict: false }) as { triggerId?: string };
  const navigate = useNavigate();
  const trigger = useTrigger(triggerId);

  useEffect(() => {
    if (trigger.data) void navigate({ href: automationHref(trigger.data), replace: true });
  }, [trigger.data, navigate]);

  if (!triggerId || (trigger.isError && isTriggerNotFound(trigger.error))) {
    return (
      <Card padding="lg" role="alert" data-testid="automation-gone">
        <p className="text-sm font-medium text-foreground">This automation no longer exists.</p>
        <p className="mt-1 text-sm text-muted-foreground">Its trigger was removed. What still runs by itself is on the Workflows tab.</p>
        <Link
          to={WORKFLOWS_LIBRARY_PATH}
          search={{ show: "on", allProjects: true }}
          className="mt-4 inline-flex h-9 items-center rounded-md border border-border px-3 text-sm font-medium text-foreground hover:bg-muted/60 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          Show what is on
        </Link>
      </Card>
    );
  }
  if (trigger.isError) {
    return (
      <Card padding="lg" role="alert">
        <p className="text-sm font-medium text-foreground">This automation could not be loaded.</p>
        <p className="mt-1 text-sm text-muted-foreground">{triggerErrorMessage(trigger.error)}</p>
        <Button className="mt-4" variant="outline" onClick={() => void trigger.refetch()}>
          Try again
        </Button>
      </Card>
    );
  }
  return (
    <p className="text-sm text-muted-foreground" aria-busy="true">
      Opening its workflow…
    </p>
  );
}
