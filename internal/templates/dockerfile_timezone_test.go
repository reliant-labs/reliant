package templates

import (
	"strings"
	"testing"
)

// Both deployed runtime stages lack the Go toolchain's fallback zoneinfo.zip.
// Keep named-zone support consistent between a debug session and production.
func TestDockerfile_RuntimeTimezoneData(t *testing.T) {
	src := string(renderProject(t, "Dockerfile.tmpl", projectData()))
	for _, stage := range []string{"debug", "production"} {
		t.Run(stage, func(t *testing.T) {
			start := strings.Index(src, " AS "+stage+"\n")
			if start < 0 {
				t.Fatalf("missing %s stage", stage)
			}
			body := src[start:]
			if end := strings.Index(body, "\nFROM "); end >= 0 {
				body = body[:end]
			}
			if !strings.Contains(body, "RUN apk --no-cache add ca-certificates tzdata\n") {
				t.Fatalf("%s runtime must install timezone data for time.LoadLocation:\n%s", stage, body)
			}
		})
	}
}
