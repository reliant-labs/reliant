package modelserialization

import (
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestSerializerBoundaryAllowlist(t *testing.T) {
	for _, p := range []string{"encoding/xml", "encoding/gob", "gopkg.in/yaml.v3", "google.golang.org/protobuf/encoding/protojson"} {
		if !serializer(p) {
			t.Errorf("missing serializer boundary %s", p)
		}
	}
	if serializer("encoding/json") {
		t.Fatal("JSON's legacy-compatible shape must remain allowed")
	}
	if serializer("example.test/encoding/xml") {
		t.Fatal("unrelated package matched a serializer")
	}
}

func TestAnalyzer(t *testing.T) {
	testkit.Slow(t, "analysistest invokes the Go loader on small typed fixtures")
	analysistest.Run(t, analysistest.TestData(), Analyzer, "github.com/reliant-labs/reliant/cases")
}

func TestDriverRejectsTypeErrors(t *testing.T) {
	testkit.Slow(t, "analysistest invokes the Go loader on an intentionally broken fixture")
	if Analyzer.RunDespiteErrors {
		t.Fatal("partially typed packages must not be analyzed")
	}
	recorder := &driverErrors{}
	analysistest.Run(recorder, analysistest.TestData(), Analyzer, "github.com/reliant-labs/reliant/broken")
	if !strings.Contains(strings.Join(recorder.messages, "\n"), "analysis skipped due to errors in package") {
		t.Fatalf("driver did not reject the type error: %v", recorder.messages)
	}
}

// Capture the expected analysistest load failure, not a successful partial run.
type driverErrors struct{ messages []string }

func (r *driverErrors) Errorf(format string, args ...any) {
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
}
