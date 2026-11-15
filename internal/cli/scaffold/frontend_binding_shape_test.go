package scaffold

import (
	"strings"
	"testing"
)

func TestFrontendScaffoldHasRebindableReference(t *testing.T) {
	for _, binding := range []frontendBinding{frontendOnHost, frontendHosted, frontendOnBucket} {
		got, status := spliceFrontendIntoEnvKCL("_database_url = \"dsn\"\n_bundle = forge.Bundle {\n    project = \"landing\"\n    secret_provider = forge.FileSecrets {}\n}\n", "landing", "prod", "web", "nextjs", binding, 3000, []string{"none"})
		if status != frontendKCLApplied {
			t.Fatal(status)
		}
		if !strings.Contains(got, "_web_frontend = forge.Frontend {") {
			t.Errorf("missing standalone declaration:\n%s", got)
		}
		if !strings.Contains(got, "(_web_frontend)\n") {
			t.Errorf("missing single rebindable line:\n%s", got)
		}
	}
}
