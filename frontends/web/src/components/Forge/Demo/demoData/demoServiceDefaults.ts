// Copyright (c) 2025 Reliant Labs

/**
 * Default answers for every RPC of every control-plane service a forge
 * console tab reads: forge's generated service mocks (forge#1874), so a tab
 * renders believable, schema-valid data even where the demo's story
 * (consoleScenario.ts, demoScenario.ts) says nothing.
 *
 * Each line is what forge's generator writes per service
 * (`src/mocks/services/<service>_gen.ts`: `export const xDefaults =
 * serviceDefaults(XService)`). The engine is the published forge runtime;
 * this list stays product-owned and limited to the console's services so
 * the demo bundle carries no other contract. The app-wide generated mock
 * registry also includes Reliant services, so it is not this bundle's input.
 *
 * Writes are never answered by these: the transport refuses them first
 * (demoTransport.ts isDemoRead).
 */

import type { DescMessage, DescService } from "@bufbuild/protobuf";
import { create, isMessage } from "@bufbuild/protobuf";

import { ActivityService } from "@/gen/controlplane/services/activity/v1/activity_pb";
import { AlertingService } from "@/gen/controlplane/services/alerting/v1/alerting_pb";
import { AppDatabaseService } from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { StorageBrowserService } from "@/gen/controlplane/services/storage_browser/v1/storage_browser_pb";
import { DashboardService } from "@/gen/controlplane/services/dashboard/v1/dashboard_pb";
import { DeliveryInsightsService } from "@/gen/controlplane/services/delivery_insights/v1/delivery_insights_pb";
import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { DomainService } from "@/gen/controlplane/services/domain/v1/domain_pb";
import { IssuesService } from "@/gen/controlplane/services/issues/v1/issues_pb";
import { IdentityService } from "@/gen/controlplane/services/identity/v1/identity_pb";
import { ObservabilityService } from "@/gen/controlplane/services/observability/v1/observability_pb";
import { ObservabilityBillingService } from "@/gen/controlplane/services/observability_billing/v1/observability_billing_pb";
import { RumService } from "@/gen/controlplane/services/rum/v1/rum_pb";
import { SecretStoreService } from "@/gen/controlplane/services/secret_store/v1/secret_store_pb";

import { serviceDefaults, type AnyServiceDefaults, type MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

/** The services the forge console's tabs read. */
export const DEMO_DEFAULT_SERVICES: readonly DescService[] = [
  ActivityService,
  AlertingService,
  AppDatabaseService,
  StorageBrowserService,
  DashboardService,
  DeliveryInsightsService,
  DeployService,
  DomainService,
  IssuesService,
  IdentityService,
  ObservabilityService,
  ObservabilityBillingService,
  RumService,
  SecretStoreService,
];

export const demoServiceDefaults: readonly AnyServiceDefaults[] = DEMO_DEFAULT_SERVICES.map((service) => serviceDefaults(service));

/**
 * The defaults as scenario handlers, keyed "pkg.Service/Method": the last
 * rung of the dispatch (a scenario handler wins). This materializes the
 * demo's deliberately limited service list for its composed scenario.
 */
export function defaultHandlers(services: readonly AnyServiceDefaults[] = demoServiceDefaults): MockHandlers {
  const out: MockHandlers = {};
  for (const defaults of services) {
    const service = defaults.$service;
    const byName = defaults as unknown as Record<string, (req: unknown) => unknown>;
    for (const method of service.methods) out[`${service.typeName}/${method.name}`] = (req: unknown) => byName[method.localName]?.(req);
  }
  return out;
}

/**
 * Handlers whose plain-object answers are completed into the method's
 * response message. The published transport also completes final responses;
 * this scenario-composition seam retains full messages before outer handlers
 * inspect them. A built message passes through.
 */
export function completed(service: DescService, handlers: MockHandlers): MockHandlers {
  const out: MockHandlers = {};
  for (const [key, handler] of Object.entries(handlers)) {
    const method = service.methods.find((m) => `${service.typeName}/${m.name}` === key);
    const output = method?.output as DescMessage | undefined;
    out[key] = async (req: unknown) => {
      const res = await handler(req);
      return output && res && typeof res === "object" && !isMessage(res) && !Array.isArray(res) ? create(output, res as never) : res;
    };
  }
  return out;
}

/**
 * Handlers that receive their request as the full message, whatever the
 * caller passed: a Connect client hands the transport the init object it was
 * given (`{}` for "no filters"). The scenario's scope layer reads those
 * fields before dispatch, so normalize here rather than depending on when
 * the transport completes requests for individual handlers.
 */
export function withMessageRequests(handlers: MockHandlers, services: readonly DescService[] = DEMO_DEFAULT_SERVICES): MockHandlers {
  const inputs = new Map<string, DescMessage>();
  for (const service of services) for (const method of service.methods) inputs.set(`${service.typeName}/${method.name}`, method.input);
  const out: MockHandlers = {};
  for (const [key, handler] of Object.entries(handlers)) {
    const input = inputs.get(key);
    out[key] = input ? (req: unknown) => handler(req && typeof req === "object" && !isMessage(req) && !Array.isArray(req) ? create(input, req as never) : req) : handler;
  }
  return out;
}
