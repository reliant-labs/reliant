package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// TestRunBufGenerateTypeScript_MissingPluginIsAnError pins the outcome of
// running `forge generate` in a checkout whose frontend has no node_modules.
//
// It used to print a warning and return nil. Every later step then ran as
// though TypeScript codegen had succeeded: the hooks step emitted
// `<svc>-service-hooks_gen.ts` importing `@/gen/services/<svc>/v1/<svc>_pb`,
// the mocks step emitted fixtures importing the same module — and the module
// was never written. The result is a frontend that does not compile, produced
// by a `forge generate` that exited 0.
//
// That is the shape of failure forge exists to prevent: the diagnostic
// arrives at `next build` time, in a different repo, attributed to the
// service that was merely the most recent one added. The pre-flight must
// fail the run instead, naming the remedy.
func TestRunBufGenerateTypeScript_MissingPluginIsAnError(t *testing.T) {
	dir := t.TempDir()
	feDir := filepath.Join(dir, "frontends", "web")
	if err := os.MkdirAll(feDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A committed buf.gen.yaml naming the local plugin — but no
	// node_modules/.bin/protoc-gen-es anywhere.
	bufGen := "version: v2\nplugins:\n  - local: ./frontends/web/node_modules/.bin/protoc-gen-es\n    out: frontends/web/src/gen\n"
	if err := os.WriteFile(filepath.Join(feDir, "buf.gen.yaml"), []byte(bufGen), 0o644); err != nil {
		t.Fatal(err)
	}

	fe := config.FrontendConfig{Name: "web", Type: "nextjs"}
	err := runBufGenerateTypeScript(fe, &config.ProjectConfig{}, dir)
	if err == nil {
		t.Fatal("runBufGenerateTypeScript returned nil with the TypeScript plugin missing — " +
			"the run continues and later steps emit hooks and mocks importing _pb modules that were never generated")
	}
	if !strings.Contains(err.Error(), "npm install") {
		t.Errorf("error should name the remedy (`npm install` in the frontend), got: %v", err)
	}
	if !strings.Contains(err.Error(), "web") {
		t.Errorf("error should name the frontend, got: %v", err)
	}
}
