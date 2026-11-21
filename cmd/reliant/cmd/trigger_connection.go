// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/ghdelegated"
)

// Which connection an automation acts through (north-star AW-45): `trigger
// get` names it — the integration, the account it acts as, its name and id —
// so a person can tell which of their GitHub accounts an activation listens
// and writes through without opening the app.

// connectionGetter is the one ConnectionService call `trigger get` makes.
type connectionGetter interface {
	GetConnection(context.Context, *connect.Request[reliantv1.GetConnectionRequest]) (*connect.Response[reliantv1.GetConnectionResponse], error)
}

// describeTriggerConnection is t's Connection line, with the connection read
// from the server. A read that fails (an older server, a connection since
// deleted) degrades to what the trigger alone says: the id.
func describeTriggerConnection(ctx context.Context, client connectionGetter, t *reliantv1.Trigger) string {
	id := t.GetConnectionId()
	if id == "" || strings.HasPrefix(id, "controlplane:") {
		return triggerConnectionLabel(t, nil)
	}
	resp, err := client.GetConnection(ctx, connect.NewRequest(&reliantv1.GetConnectionRequest{Id: id}))
	if err != nil && t.GetOrgId() != "" {
		// An org connection is read as the org's, by a member of it.
		org := t.GetOrgId()
		resp, err = client.GetConnection(ctx, connect.NewRequest(&reliantv1.GetConnectionRequest{Id: id, OwnerOrgId: &org}))
	}
	if err != nil {
		return triggerConnectionLabel(t, nil)
	}
	return triggerConnectionLabel(t, resp.Msg.GetConnection())
}

// triggerConnectionLabel words the connection t acts through: c when it was
// read, else the id alone. "" for a trigger with no integration and no
// connection (a schedule, a webhook).
func triggerConnectionLabel(t *reliantv1.Trigger, c *reliantv1.Connection) string {
	id := t.GetConnectionId()
	integ := t.GetIntegration().GetIntegration()
	switch {
	case id == "" && integ == "":
		return ""
	case id == "":
		// An access-gated integration (hosted GitHub) routes events by the
		// owner's own access and binds no connection.
		return fmt.Sprintf("none bound: %s events reach it through the owner's own access", integ)
	case ghdelegated.IsOrgAppConnectionID(id):
		installation, _ := ghdelegated.ParseOrgAppConnectionID(id)
		return fmt.Sprintf("the organization's GitHub App installation %d (%s)", installation, id)
	case id == ghdelegated.ConnectionID:
		return fmt.Sprintf("your GitHub access through Reliant (%s)", id)
	case c == nil:
		return id
	}
	parts := []string{c.GetIntegrationId()}
	if account := c.GetAccountLabel(); account != "" {
		parts = append(parts, account)
	}
	label := strings.Join(parts, " · ")
	if name := c.GetName(); name != "" && name != c.GetAccountLabel() {
		label += fmt.Sprintf(" (%q)", name)
	}
	label += " " + id
	if c.GetOwnerKind() == reliantv1.ConnectionOwnerKind_CONNECTION_OWNER_KIND_ORG {
		label += ", the organization's"
	}
	if status := connectionStatusLabel(c.GetStatus()); status != "active" {
		label += ", " + status
		if reason := c.GetStatusReason(); reason != "" {
			label += " (" + reason + ")"
		}
	}
	return label
}
