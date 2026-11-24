// Copyright (c) 2025 Reliant Labs

/**
 * Every generated service descriptor (src/gen/**, reliant's and the vendored
 * control-plane contract), for tests that check a whole pack. A pack grows
 * services as handlers land (Organization, User, Tests, …), so a hand-kept
 * list goes stale and breaks main when the last sibling PR merges; this one
 * cannot. A scenario key naming a service that is not generated at all is
 * still a problem checkFixturePack reports by name.
 *
 * Test-only: it imports every generated module, which no bundle may do.
 */

import type { DescService } from "@bufbuild/protobuf";

let registry: readonly DescService[] | undefined;

/** Every generated service, one descriptor per type name. */
export function allServices(): readonly DescService[] {
  if (registry) return registry;
  const byName = new Map<string, DescService>();
  for (const [path, mod] of Object.entries(import.meta.glob<Record<string, unknown>>("/src/gen/**/*_pb.ts", { eager: true }))) {
    for (const value of Object.values(mod)) {
      const service = value as Partial<DescService> | null;
      if (service?.kind !== "service" || typeof service.typeName !== "string") continue;
      const seen = byName.get(service.typeName);
      // The same service generated twice must be the same contract, or a check would depend on which copy won.
      if (seen && seen.methods.map((m) => m.name).join() !== service.methods!.map((m) => m.name).join()) {
        throw new Error(`allServices: ${service.typeName} is generated twice with different methods (${path})`);
      }
      if (!seen) byName.set(service.typeName, service as DescService);
    }
  }
  registry = [...byName.values()];
  return registry;
}
