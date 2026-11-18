package naming

// FrontendServiceName is the OpenTelemetry service.name a web frontend's
// browser telemetry reports: "<project>-<frontend>", e.g. "shop-web".
//
// It is the ONE derivation of that name. The frontend's generated
// src/lib/otel_gen.ts bakes it as SERVICE_NAME (the value the runtime
// sends), and the generated observability — the frontend dashboard's scope
// and the frontend alert defaults — filters on it, so the two cannot name
// different services. An earlier split had the dashboards derive
// "<project>-frontend" while a hand-wired runtime reported
// "<project>-<frontend>", and every frontend alert matched nothing.
func FrontendServiceName(project, frontend string) string {
	return project + "-" + frontend
}
