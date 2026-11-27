// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"strings"
	"testing"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// A Reliant server action holds no connection: it runs as the run itself on
// Reliant's own state, so its capability is the action, not an integration's
// default connection, and its disclosure says so.
func TestServerActionCapabilityAndDisclosure(t *testing.T) {
	t.Parallel()
	node := &pb.Node{Id: "decide", Type: "action", Args: &pb.Node_Action{Action: &pb.ActionArgs{
		Uses: &pb.CelString{Value: &pb.CelString_Literal{Literal: "reliant/pr.next@1"}},
	}}}
	got := nodeCapabilities(node, nil, "", nil)
	if len(got) != 1 || got[0] != "server_action:reliant/pr.next@1" {
		t.Fatalf("capabilities = %v, want [server_action:reliant/pr.next@1]", got)
	}
	disclosed := strings.Join(nodePrivilegeDisclosure(node, nil, "", nil), "\n")
	if !strings.Contains(disclosed, "Reliant server action") || strings.Contains(disclosed, "connection=") {
		t.Fatalf("disclosure = %q", disclosed)
	}
}
