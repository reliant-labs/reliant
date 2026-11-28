package validation

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/serveraction"
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

func TestHostedUseRequiresFixedHTTPAndNoOtherAuth(t *testing.T) {
	literal := func(s string) *reliantv1.CelString {
		return &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: s}}
	}
	for _, tc := range []struct {
		name, uses, extra, connection string
		valid                         bool
	}{
		{name: "valid", uses: "http/request@1", valid: true},
		{name: "other action", uses: "github/issue.create@1"},
		{name: "headers", uses: "http/request@1", extra: "headers"},
		{name: "connection parameter", uses: "http/request@1", extra: "connection"},
		{name: "connection field", uses: "http/request@1", connection: "saved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf := actionWorkflow(tc.uses, map[string]*structpb.Value{"url": structpb.NewStringValue("https://example.com/rpc")})
			args := wf.Nodes[0].GetAction()
			args.HostedUse = literal("grant")
			if tc.connection != "" {
				args.Connection = literal(tc.connection)
			}
			if tc.extra != "" {
				args.With[tc.extra] = structpb.NewStringValue("forbidden")
			}
			r := StaticAnalysis(wf, nil)
			if tc.valid == r.HasErrors() {
				t.Fatalf("valid=%v errors=%s", tc.valid, errorsText(r))
			}
		})
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

// withServerActions makes the server-action catalog validation resolves
// against the given specs for the rest of the test.
func withServerActions(t *testing.T, specs ...serveraction.Spec) {
	t.Helper()
	c, err := serveraction.NewCatalog(specs...)
	if err != nil {
		t.Fatal(err)
	}
	prev := serverActions
	serverActions = func() *serveraction.Catalog { return c }
	t.Cleanup(func() { serverActions = prev })
}

func TestServerActionResolvesAgainstTheServerActionCatalog(t *testing.T) {
	withServerActions(t, serveraction.Spec{Name: "reliant/test.echo@1", Description: "echo", Params: map[string]any{
		"type": "object", "required": []any{"text"}, "additionalProperties": false,
		"properties": map[string]any{"text": map[string]any{"type": "string"}, "n": map[string]any{"type": "integer"}},
	}})
	if r := StaticAnalysis(actionWorkflow("reliant/test.echo@1", map[string]*structpb.Value{"text": structpb.NewStringValue("hi")}), nil); r.HasErrors() {
		t.Fatalf("a declared server action was rejected: %s", errorsText(r))
	}
	templated := StaticAnalysis(actionWorkflow("reliant/test.echo@1", map[string]*structpb.Value{"text": structpb.NewStringValue("{{ inputs.t }}")}), nil)
	if templated.HasErrors() {
		t.Errorf("templated param rejected: %s", errorsText(templated))
	}
	for name, with := range map[string]map[string]*structpb.Value{
		"text":  nil,
		"bogus": {"text": structpb.NewStringValue("hi"), "bogus": structpb.NewStringValue("x")},
		"n":     {"text": structpb.NewStringValue("hi"), "n": structpb.NewStringValue("seven")},
	} {
		r := StaticAnalysis(actionWorkflow("reliant/test.echo@1", with), nil)
		if !r.HasErrors() || !strings.Contains(errorsText(r), name) {
			t.Errorf("params %v must fail naming %q: %s", with, name, errorsText(r))
		}
	}
}

func TestUnknownServerActionFailsValidation(t *testing.T) {
	withServerActions(t, serveraction.Spec{Name: "reliant/test.echo@1", Description: "echo"})
	for _, uses := range []string{"reliant/test.nope@1", "reliant/test.echo@2", "reliant/nope.echo@1"} {
		r := StaticAnalysis(actionWorkflow(uses, nil), nil)
		if !r.HasErrors() || !strings.Contains(errorsText(r), "unknown server action") {
			t.Errorf("uses %q must fail as an unknown server action: %s", uses, errorsText(r))
		}
	}
	for _, uses := range []string{"reliant/Test.echo@1", "reliant/test.echo", "reliant/echo@1"} {
		if r := StaticAnalysis(actionWorkflow(uses, nil), nil); !r.HasErrors() {
			t.Errorf("malformed server action %q must fail validation", uses)
		}
	}
}

// No server action ships before its implementation does: until then every
// reliant/ name is unknown, so no workflow can name one.
func TestShippedServerActionsAreDeclaredOnlyWithTheirImplementations(t *testing.T) {
	r := StaticAnalysis(actionWorkflow("reliant/pr.next@1", nil), nil)
	if len(serveraction.Builtin().Names()) == 0 && !r.HasErrors() {
		t.Fatalf("reliant/pr.next@1 must be unknown while the catalog is empty")
	}
}

func TestServerActionTakesNoIntegrationAuthentication(t *testing.T) {
	withServerActions(t, serveraction.Spec{Name: "reliant/test.echo@1", Description: "echo"})
	literal := func(s string) *reliantv1.CelString {
		return &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: s}}
	}
	connection := actionWorkflow("reliant/test.echo@1", nil)
	connection.Nodes[0].GetAction().Connection = literal("saved")
	if r := StaticAnalysis(connection, nil); !r.HasErrors() || !strings.Contains(errorsText(r), "connection") {
		t.Errorf("a server action with a connection must fail: %s", errorsText(r))
	}
	hosted := actionWorkflow("reliant/test.echo@1", nil)
	hosted.Nodes[0].GetAction().HostedUse = literal("grant")
	if r := StaticAnalysis(hosted, nil); !r.HasErrors() {
		t.Errorf("a server action with hosted_use must fail")
	}
}
