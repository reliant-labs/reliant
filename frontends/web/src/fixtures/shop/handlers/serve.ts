// Copyright (c) 2025 Reliant Labs

/**
 * One service's typed answers as scenario handlers: `mockService`'s typed
 * overrides, each request completed into its message first (a Connect client
 * hands the transport the init object it was given), and each plain unary
 * answer completed into its response message (defaults.ts withMessageRequests
 * and completed). A streaming answer, an iterable of messages, passes as it
 * is. The app layer's modules (app.ts) answer through this.
 */
import type { DescService } from "@bufbuild/protobuf";
import { mockService, type MockHandlers, type MockOverrides } from "@reliantlabs/forge-web-runtime/mock-transport";

import { completed, withMessageRequests } from "./defaults";

export function serve<S extends DescService>(service: S, overrides: MockOverrides<S>): MockHandlers {
  const handlers = mockService(service, overrides);
  const unary = new Set(service.methods.filter((m) => m.methodKind === "unary").map((m) => `${service.typeName}/${m.name}`));
  const unaryHandlers = completed(service, Object.fromEntries(Object.entries(handlers).filter(([key]) => unary.has(key))));
  return withMessageRequests({ ...handlers, ...unaryHandlers }, [service]);
}
