package trigger

import (
	"testing"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/assert"
)

func lit(s string) *pb.CelString  { return &pb.CelString{Value: &pb.CelString_Literal{Literal: s}} }
func expr(s string) *pb.CelString { return &pb.CelString{Value: &pb.CelString_Expr{Expr: s}} }

func TestNodeCapabilities(t *testing.T) {
	values := map[string]any{"conn": "conn_fixed", "target": "staging"}
	tainted := map[string]bool{"event_conn": true}
	action := func(a *pb.ActionArgs) *pb.Node { return &pb.Node{Args: &pb.Node_Action{Action: a}} }
	run := func(r *pb.RunArgs) *pb.Node { return &pb.Node{Args: &pb.Node_Run{Run: r}} }
	for _, tc := range []struct {
		name string
		node *pb.Node
		want []string
	}{
		{"action on the integration's default connection", action(&pb.ActionArgs{Uses: lit("slack/post_message@1")}), []string{"connection:slack/default"}},
		{"action on a fixed connection", action(&pb.ActionArgs{Uses: lit("github/create_issue@1"), Connection: lit("conn_1")}), []string{"connection:conn_1"}},
		{"action on an input-resolved connection", action(&pb.ActionArgs{Uses: lit("github/create_issue@1"), Connection: expr("{{ inputs.conn }}")}), []string{"connection:conn_fixed"}},
		{"action on an event-chosen connection", action(&pb.ActionArgs{Uses: lit("github/create_issue@1"), Connection: expr("{{ inputs.event_conn }}")}), []string{"connection:dynamic"}},
		{"unauthenticated http request", action(&pb.ActionArgs{Uses: lit("http/request@1")}), nil},
		{"hosted use grant", action(&pb.ActionArgs{Uses: lit("http/request@1"), HostedUse: lit("grant_trader")}), []string{"secret:hosted_use/grant_trader"}},
		{"shell step inherits credentials", run(&pb.RunArgs{Command: lit("npm test")}), []string{"secret:machine_credentials"}},
		{"shell step without credentials", run(&pb.RunArgs{Command: lit("npm test"), Credentials: "none"}), nil},
		{"fixed prod deploy", run(&pb.RunArgs{Command: lit("reliant forge env deploy prod --yes"), Credentials: "none"}), []string{"deploy:prod"}},
		{"fixed promote to production", run(&pb.RunArgs{Command: lit("forge env promote staging production"), Credentials: "none"}), []string{"deploy:prod"}},
		{"staging deploy is not prod", run(&pb.RunArgs{Command: lit("reliant forge env deploy staging"), Credentials: "none"}), nil},
		{"deploy target from an input resolves", run(&pb.RunArgs{Command: expr("reliant forge env deploy {{ inputs.target }}"), Credentials: "none"}), nil},
		{"git publish", &pb.Node{Args: &pb.Node_GitPublish{GitPublish: &pb.GitPublishArgs{}}}, []string{"connection:github/git_publish"}},
		{"approval holds nothing", &pb.Node{Args: &pb.Node_Approval{Approval: &pb.ApprovalArgs{}}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, nodeCapabilities(tc.node, values, "wf", tainted))
		})
	}
}

func TestRowAndConnectionCapabilities(t *testing.T) {
	assert.Equal(t, []string{"identity:owner", "machine:d1"}, rowCapabilities(&core.Trigger{DaemonID: "d1"}))
	assert.Equal(t, []string{"identity:app"}, rowCapabilities(&core.Trigger{DaemonID: "d1", NoMachine: true, ActingIdentity: core.ActingIdentityApp}),
		"a no-machine activation holds no machine")
	assert.Equal(t, []string{"connection:github/c1", "scope:github/repo", "scope:github/workflow"},
		connectionCapabilities(&core.Connection{ID: "c1", IntegrationID: "github", Scopes: []string{"repo", "workflow"}}))
	assert.Nil(t, connectionCapabilities(nil))
}
