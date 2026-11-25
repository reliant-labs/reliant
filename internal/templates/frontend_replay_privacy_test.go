package templates

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

func TestGeneratedReplayPrivacyGuardsOlderRuntimes(t *testing.T) {
	if testing.Short() {
		t.Skip("executes generated TypeScript with node; runs in task test")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	for _, platform := range []string{"vite-spa", "nextjs"} {
		src := renderTelemetryTmpl(t, platform+"/src/lib/otel_gen.ts.tmpl", telemetryData("static"))
		for _, privacy := range []string{`{}`, `{"doNotTrack":"1"}`, `{"doNotTrack":"yes"}`, `{"globalPrivacyControl":true}`, `{"msDoNotTrack":"1"}`} {
			t.Run(platform+privacy, func(t *testing.T) {
				dir := writeTelemetryHarness(t, platform, src)
				cmd := exec.Command(node, "--experimental-strip-types", "--no-warnings", "main.mjs")
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), `FORGE_TEST_RUNTIME={"OTEL_ENDPOINT":"/_otel","OTEL_REPLAY":"true"}`, "FORGE_TEST_PRIVACY="+privacy)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("node: %v\n%s", err, out)
				}
				var result struct {
					Options map[string]any `json:"options"`
				}
				if err := json.Unmarshal(out, &result); err != nil {
					t.Fatal(err)
				}
				if result.Options["replay"] != (privacy == `{}`) {
					t.Fatalf("old runtime received replay=%v for %s", result.Options["replay"], privacy)
				}
			})
		}
	}
}
