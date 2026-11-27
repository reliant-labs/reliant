// Copyright (c) 2026 Reliant Labs

/**
 * A run's Logs tab (02-views §3.6): Observability's logs explorer over every
 * pod of the scope (the run, a phase, an attempt or one rank), filtered by
 * the `ml.*` attributes the trainer's pods carry. A line from a traced step
 * opens that trace on the Traces tab.
 */

import { MlExplorer } from "../../kit/explorer";
import { scopeRange, type RunScope } from "../runScope";

export function LogsTab({ scope }: { scope: RunScope }) {
  return <MlExplorer view="logs" subject={scope.subject} label={scope.subject.label || scope.run.name} range={scopeRange(scope)} testId="ml-run-logs" />;
}
