package templates

import (
	"regexp"
	"strings"
	"testing"
)

func TestNextDevlogExcludesRawRequestURLs(t *testing.T) {
	for _, output := range []string{"static", "standalone", "server"} {
		t.Run(output, func(t *testing.T) {
			config := renderTelemetryTmpl(t, "nextjs/next.config.ts.tmpl", telemetryData(output))
			const literal = `logging: { incomingRequests: { ignore: [/(?:^|\/)__forge\/log(?:\?|$)/] } },`
			if !strings.Contains(config, literal) {
				t.Fatal("Next request logging must exclude browser-log URL before route handling")
			}
			pattern := regexp.MustCompile(`(?:^|/)__forge/log(?:\?|$)`)
			for _, path := range []string{"/__forge/log", "/internal/__forge/log?token=synthetic", "/custom/path/__forge/log?query=synthetic"} {
				if !pattern.MatchString(path) {
					t.Errorf("log URL not excluded: %s", path)
				}
			}
			for _, path := range []string{"/internal", "/api/health", "/other?key=synthetic", "/__forge/log-other"} {
				if pattern.MatchString(path) {
					t.Errorf("unrelated request suppressed: %s", path)
				}
			}
		})
	}
	route := renderTelemetryTmpl(t, "nextjs/src/app/%5F_forge/log/route.ts", telemetryData("standalone"))
	if !strings.Contains(route, "new URL(request.url).search") {
		t.Fatal("receiver must reject query-bearing devlog requests")
	}
}
