// Copyright (c) 2026 Reliant Labs

/**
 * The machine detail view's Resources card: the machine's and the Reliant
 * daemon's CPU, memory and open files, as the daemon's heartbeat last
 * reported them (DaemonInfo.resources, stored by the gateway at most once a
 * minute while the machine is connected). Nothing renders while there is no
 * reading: a disconnected machine, or a daemon that predates the report.
 */

import { Cpu } from "lucide-react";

import type { DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { resourceSections } from "@/lib/machineResources";
import { Card, CardContent, CardHeader, CardTitle } from "./ui";

export function MachineResourcesCard({ daemon }: { daemon: DaemonInfo }) {
  const sections = resourceSections(daemon.resources);
  if (sections.length === 0) return null;
  return (
    <Card className="mt-6" data-testid="machine-resources-card">
      <CardHeader>
        <CardTitle className="inline-flex items-center gap-2">
          <Cpu className="h-4 w-4 text-muted-foreground" /> Resources
        </CardTitle>
      </CardHeader>
      <CardContent>
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
          {sections.map((section) => (
            <section key={section.title} aria-label={section.title}>
              <h4 className="mb-1 text-ui font-medium text-muted-foreground">{section.title}</h4>
              <dl>
                {section.rows.map((row) => (
                  <div
                    key={row.label}
                    className="flex justify-between gap-4 border-b border-border-subtle py-2 last:border-0"
                  >
                    <dt className="text-ui text-muted-foreground">{row.label}</dt>
                    <dd className="text-right text-ui font-medium tabular-nums text-foreground">{row.value}</dd>
                  </div>
                ))}
              </dl>
            </section>
          ))}
        </div>
      </CardContent>
    </Card>
  );
}
