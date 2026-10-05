package validation

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	structpb "google.golang.org/protobuf/types/known/structpb"
)

func actionWorkflow(uses string, with map[string]*structpb.Value) *reliantv1.Workflow {
	return &reliantv1.Workflow{
		Name:  "wf",
		Entry: []string{"call"},
		Nodes: []*reliantv1.Node{{
			Id: "call", Type: model.NodeTypeAction,
			Args: &reliantv1.Node_Action{Action: &reliantv1.ActionArgs{
				Uses: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: uses}},
				With: with,
			}},
		}},
	}
}

func errorsText(r *Result) string {
	var b strings.Builder
	for _, e := range r.Errors() {
		b.WriteString(e.Error() + "\n")
	}
	return b.String()
}

func TestActionNodeValidUses(t *testing.T) {
	wf := actionWorkflow("http/request@1", map[string]*structpb.Value{"url": structpb.NewStringValue("https://example.com")})
	if r := StaticAnalysis(wf, nil); r.HasErrors() {
		t.Fatalf("valid action node rejected: %s", errorsText(r))
	}
}

func TestActionNodeUnknownUsesFailsValidation(t *testing.T) {
	for _, uses := range []string{"nope/request@1", "http/nope@1", "http/request@9", "http/request", ""} {
		r := StaticAnalysis(actionWorkflow(uses, nil), nil)
		if !r.HasErrors() {
			t.Errorf("uses %q must fail validation", uses)
		}
	}
}

func TestActionNodeParamsCheckedAgainstManifestSchema(t *testing.T) {
	missing := StaticAnalysis(actionWorkflow("http/request@1", nil), nil)
	if !missing.HasErrors() || !strings.Contains(errorsText(missing), "url") {
		t.Errorf("a missing required param must fail: %s", errorsText(missing))
	}
	unknown := StaticAnalysis(actionWorkflow("http/request@1", map[string]*structpb.Value{
		"url": structpb.NewStringValue("https://x"), "bogus": structpb.NewStringValue("1"),
	}), nil)
	if !unknown.HasErrors() || !strings.Contains(errorsText(unknown), "bogus") {
		t.Errorf("an unknown param must fail: %s", errorsText(unknown))
	}
	// A templated value cannot be type-checked statically, so it is not rejected.
	templated := StaticAnalysis(actionWorkflow("http/request@1", map[string]*structpb.Value{
		"url": structpb.NewStringValue("{{ inputs.u }}"),
	}), nil)
	if templated.HasErrors() {
		t.Errorf("templated param rejected: %s", errorsText(templated))
	}
}
