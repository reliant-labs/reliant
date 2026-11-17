package cli

// `forge obs receivers list|create|test|delete`: where the org's alerts go —
// Slack, a webhook, email, PagerDuty — over control-plane AlertingService.
// Receivers are the org's, not an env's, so they need no deploy tree: a
// monitoring-only project routes its alerts from the CLI. Creating, testing
// and deleting need a person holding obs:write; listing needs membership.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
)

const (
	procListReceivers  = "controlplane.v1.AlertingService/ListReceivers"
	procCreateReceiver = "controlplane.v1.AlertingService/CreateReceiver"
	procDeleteReceiver = "controlplane.v1.AlertingService/DeleteReceiver"
	procTestReceiver   = "controlplane.v1.AlertingService/TestReceiver"
)

// receiverTypes maps the CLI's type names onto ReceiverType.
var receiverTypes = map[string]string{
	"slack":     "RECEIVER_TYPE_SLACK",
	"webhook":   "RECEIVER_TYPE_WEBHOOK",
	"email":     "RECEIVER_TYPE_EMAIL",
	"pagerduty": "RECEIVER_TYPE_PAGERDUTY",
}

// wireReceiver is AlertingService's Receiver.
type wireReceiver struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Settings  map[string]string `json:"settings"`
	HasSecret bool              `json:"hasSecret"`
	CreatedAt time.Time         `json:"createdAt"`
}

func newObsReceiversCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "receivers",
		Short: "Route the org's alerts to Slack, a webhook, email or PagerDuty",
		Long: `Receivers are where the organization's alerts are delivered. Every org also
has the Reliant console's in-app inbox. They need no deploy tree, so a
monitoring-only project routes its alerts from here.

  forge obs receivers create oncall --type slack --secret "$SLACK_WEBHOOK_URL" --setting channel=#oncall
  forge obs receivers create hooks --type webhook --setting url=https://example.com/alerts --secret "$BEARER"
  forge obs receivers create team --type email --setting to=ops@example.com
  forge obs receivers create pager --type pagerduty --secret "$PD_ROUTING_KEY"
  forge obs receivers test <id>
  forge obs receivers list
  forge obs receivers delete <id>

The secret (a Slack webhook URL, a bearer token, a PagerDuty routing key) is
write-only: it is never shown again.`,
	}
	cmd.AddCommand(newObsReceiversListCmd(), newObsReceiversCreateCmd(), newObsReceiversTestCmd(), newObsReceiversDeleteCmd())
	return cmdutil.StrictGroup(cmd)
}

func newObsReceiversListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the org's alert receivers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := defaultObsAdminDeps(cmd)
			ctx := cmd.Context()
			s, _, err := obsAdminSession(ctx, d)
			if err != nil {
				return err
			}
			var out struct {
				Receivers []wireReceiver `json:"receivers"`
			}
			if err := s.Caller.Call(ctx, procListReceivers, map[string]any{"orgId": s.OrgID}, &out); err != nil {
				return receiverErr(err)
			}
			if jsonOut {
				return writeJSON(d.Stdout, map[string]any{"schema": "forge.obs.receivers/v1", "receivers": emptyIfNil(out.Receivers)})
			}
			if len(out.Receivers) == 0 {
				fmt.Fprintln(d.Stdout, "no receivers: alerts reach the Reliant console's inbox only")
				return nil
			}
			rows := make([][]string, 0, len(out.Receivers))
			for _, r := range out.Receivers {
				rows = append(rows, []string{r.ID, r.Name, strings.ToLower(strings.TrimPrefix(r.Type, "RECEIVER_TYPE_")), settingsText(r.Settings)})
			}
			obsout.Table(d.Stdout, []string{"ID", "NAME", "TYPE", "SETTINGS"}, rows, 60)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print a forge.obs.receivers/v1 document")
	return cmd
}

func settingsText(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k]
	}
	return strings.Join(parts, " ")
}

func newObsReceiversCreateCmd() *cobra.Command {
	var typ, secret string
	var settings []string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an alert receiver",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wireType, ok := receiverTypes[strings.ToLower(typ)]
			if !ok {
				return obsbackend.Wrong("--type %q is not a receiver type: slack, webhook, email or pagerduty", typ)
			}
			set := map[string]string{}
			for _, kv := range settings {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return obsbackend.Wrong("--setting %q is not key=value", kv)
				}
				set[k] = v
			}
			d := defaultObsAdminDeps(cmd)
			ctx := cmd.Context()
			s, _, err := obsAdminSession(ctx, d)
			if err != nil {
				return err
			}
			var out struct {
				Receiver wireReceiver `json:"receiver"`
			}
			req := map[string]any{"orgId": s.OrgID, "name": args[0], "type": wireType, "settings": set, "secret": secret}
			if err := s.Caller.Call(ctx, procCreateReceiver, req, &out); err != nil {
				return receiverErr(err)
			}
			if jsonOut {
				return writeJSON(d.Stdout, out.Receiver)
			}
			fmt.Fprintf(d.Stdout, "created receiver %s (%s)\ntry it: forge obs receivers test %s\n", out.Receiver.Name, out.Receiver.ID, out.Receiver.ID)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&typ, "type", "", "slack | webhook | email | pagerduty")
	_ = cmd.MarkFlagRequired("type")
	f.StringVar(&secret, "secret", "", "slack: the incoming-webhook URL; webhook: a bearer token (optional); pagerduty: the Events v2 routing key")
	f.StringArrayVar(&settings, "setting", nil, "key=value (repeatable): slack channel=, webhook url= (https), email to= (comma-separated), pagerduty severity=")
	f.BoolVar(&jsonOut, "json", false, "print the receiver as JSON")
	return cmd
}

func newObsReceiversTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test <receiver-id>",
		Short: "Send a test alert to a receiver",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := defaultObsAdminDeps(cmd)
			ctx := cmd.Context()
			s, _, err := obsAdminSession(ctx, d)
			if err != nil {
				return err
			}
			var out struct {
				Accepted     bool `json:"accepted"`
				ReceiverLive bool `json:"receiverLive"`
			}
			if err := s.Caller.Call(ctx, procTestReceiver, map[string]any{"orgId": s.OrgID, "id": args[0]}, &out); err != nil {
				return receiverErr(err)
			}
			switch {
			case out.Accepted && out.ReceiverLive:
				fmt.Fprintln(d.Stdout, "test alert accepted: it should arrive within a minute")
			case out.Accepted:
				fmt.Fprintln(d.Stdout, "test alert accepted, but the receiver is not live in the alert router yet (a new one takes up to a minute): try again shortly")
			default:
				return obsbackend.Undetermined("the alert router did not accept the test alert")
			}
			return nil
		},
	}
}

func newObsReceiversDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <receiver-id>",
		Short: "Delete an alert receiver",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := defaultObsAdminDeps(cmd)
			ctx := cmd.Context()
			s, _, err := obsAdminSession(ctx, d)
			if err != nil {
				return err
			}
			if err := s.Caller.Call(ctx, procDeleteReceiver, map[string]any{"orgId": s.OrgID, "id": args[0]}, nil); err != nil {
				return receiverErr(err)
			}
			fmt.Fprintf(d.Stdout, "deleted receiver %s\n", args[0])
			return nil
		},
	}
}

// receiverErr explains a refusal a person can act on.
func receiverErr(err error) error {
	var ce *cloud.Error
	if errors.As(err, &ce) {
		switch {
		case ce.Code == cloud.CodePermissionDenied:
			return obsbackend.Wrong("changing the org's alert receivers needs a person holding the obs:write permission "+
				"(owners and admins hold it): %v", err)
		case ce.Code == cloud.CodeUnimplemented || ce.Code == cloud.CodeUnavailable:
			return obsbackend.Undetermined("this control plane does not serve alerting").Cause(err)
		}
	}
	return obsbackend.MapCloudError(err)
}
