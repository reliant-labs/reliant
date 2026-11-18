// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// A frontend's `service.name`, derived exactly as forge derives it
// (internal/naming.FrontendServiceName): `<project>-<frontend>`. The generated
// observability — the frontend dashboard's panel scope and the frontend alert
// defaults — filters on that name, so telemetry reporting anything else is
// invisible to both. The cases in testdata/frontend-service-names.json are
// read by this package's tests AND by internal/naming's, so the two spellings
// cannot drift apart.

/** `<project>-<frontend>`: the service.name forge's observability filters on. */
export function frontendServiceName(project: string, frontend: string): string {
  return `${project}-${frontend}`;
}
