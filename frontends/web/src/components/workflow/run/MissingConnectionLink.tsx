// Copyright (c) 2025 Reliant Labs

import { Link } from "@tanstack/react-router";

import { integrationDisplayName } from "../inputs/inputTypeNouns";
import { missingConnectionIntegration } from "./builderRun";

/** For a "no slack connection" run error: a link to where one is made. Renders nothing for any other error. */
export function MissingConnectionLink({ message, className }: { message: string | undefined; className?: string }) {
  const integration = missingConnectionIntegration(message);
  if (!integration) return null;
  return (
    <Link to="/settings/$section" params={{ section: "integrations" }} className={className}>
      Connect {integrationDisplayName(integration)} in Settings → Integrations
    </Link>
  );
}
