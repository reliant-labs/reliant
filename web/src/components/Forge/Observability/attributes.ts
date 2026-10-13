// Copyright (c) 2025 Reliant Labs

/**
 * The attribute keys a reader can filter and group by — control-plane's
 * allowlist (contract K1; control-plane internal/obsquery/attrs.go), with the
 * words the console shows for each.
 *
 * The SERVER is the authority: it refuses any key not on its list, plus — for
 * a metric — the metric's own declared attributes, which no fixed list can
 * know. So the picker offers this catalog and still accepts a typed key; an
 * unknown one comes back as InvalidArgument naming it.
 *
 * Each key's class decides how "Save as code" spells it in a dashboard panel
 * (obsdash field vocabulary, forge internal/obsdash/spec.go): a resource key
 * is `resource.attr.<k>`, a span key `span.attr.<k>`, and so on.
 */

export type AttributeClass = "resource" | "span" | "event" | "metric";

export interface AttributeKey {
  key: string;
  label: string;
  class: AttributeClass;
}

/** The two keys every signal carries, which obsdash names directly rather than as resource.attr.<k>. */
export const ENV_KEY = "deployment.environment.name";
export const SERVICE_KEY = "service.name";
export const VERSION_KEY = "service.version";

export const ATTRIBUTE_KEYS: readonly AttributeKey[] = [
  { key: ENV_KEY, label: "Environment", class: "resource" },
  { key: SERVICE_KEY, label: "Service", class: "resource" },
  { key: VERSION_KEY, label: "Version", class: "resource" },
  { key: "k8s.workload.name", label: "Workload", class: "resource" },
  { key: "k8s.container.name", label: "Container", class: "resource" },
  { key: "container.image.name", label: "Image", class: "resource" },
  { key: "container.image.tag", label: "Image tag", class: "resource" },
  { key: "k8s.pod.name", label: "Pod", class: "resource" },
  { key: "k8s.namespace.name", label: "Namespace", class: "resource" },
  { key: "service.namespace", label: "Service namespace", class: "resource" },
  { key: "service.instance.id", label: "Service instance", class: "resource" },
  { key: "reliant.env.id", label: "Environment id", class: "resource" },
  { key: "reliant.deployment.id", label: "Deployment id", class: "resource" },
  { key: "status", label: "Span status", class: "span" },
  { key: "rpc.method", label: "RPC method", class: "span" },
  { key: "rpc.service", label: "RPC service", class: "span" },
  { key: "rpc.system", label: "RPC system", class: "span" },
  { key: "http.route", label: "HTTP route", class: "span" },
  { key: "http.request.method", label: "HTTP method", class: "span" },
  { key: "http.response.status_code", label: "HTTP status", class: "span" },
  { key: "server.address", label: "Server address", class: "span" },
  { key: "peer.service", label: "Peer service", class: "span" },
  { key: "error.type", label: "Error type", class: "span" },
  { key: "db.system", label: "Database", class: "span" },
  { key: "db.operation.name", label: "DB operation", class: "span" },
  { key: "messaging.system", label: "Messaging system", class: "span" },
  { key: "messaging.destination.name", label: "Messaging destination", class: "span" },
  { key: "k8s.event.reason", label: "Event reason", class: "event" },
  { key: "k8s.event.type", label: "Event type", class: "event" },
  { key: "k8s.object.kind", label: "Event object kind", class: "event" },
  { key: "k8s.object.name", label: "Event object", class: "event" },
  { key: "k8s.container.last_terminated_reason", label: "Last exit reason", class: "metric" },
];

const BY_KEY = new Map(ATTRIBUTE_KEYS.map((a) => [a.key, a]));

/** Section headings for the picker, in order. */
export const CLASS_LABELS: Record<AttributeClass, string> = {
  resource: "Where it ran",
  span: "Requests",
  event: "Kubernetes events",
  metric: "Metrics",
};

/** The group-by choices offered first: the comparisons people make most. */
export const GROUP_BY_SUGGESTIONS: readonly string[] = [
  ENV_KEY,
  SERVICE_KEY,
  VERSION_KEY,
  "rpc.method",
  "status",
  "k8s.workload.name",
  "k8s.container.name",
];

/** The server's limits (QueryScope.filters ≤ 20, group_by ≤ 3, an `in` list ≤ 50). */
export const MAX_FILTERS = 20;
export const MAX_GROUP_BY = 3;
export const MAX_IN_VALUES = 50;

/** A key the console has words for, or the key itself. */
export function attributeLabel(key: string): string {
  return BY_KEY.get(key)?.label ?? key;
}

export function attributeClass(key: string): AttributeClass | undefined {
  return BY_KEY.get(key)?.class;
}

/** Keys are dotted identifiers; anything else cannot be an attribute and is refused before it reaches a URL. */
const KEY_SHAPE = /^[A-Za-z0-9_][A-Za-z0-9_.\-/]{0,127}$/;

export function isAttributeKeyShaped(key: string): boolean {
  return KEY_SHAPE.test(key);
}

/** Which telemetry the autocomplete reads a key's values from. */
export function signalForKey(key: string): "traces" | "logs" | "metrics" | undefined {
  switch (attributeClass(key)) {
    case "span":
      return "traces";
    case "event":
      return "logs";
    case "metric":
      return "metrics";
    default:
      return undefined;
  }
}
