// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/triggers"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// canonicalProto removes protojson's deliberately unstable formatting from
// the approval fingerprint. JSON object keys are sorted by encoding/json.
func canonicalProto(message proto.Message) string {
	raw, _ := protojson.Marshal(message)
	var value any
	_ = json.Unmarshal(raw, &value)
	stable, _ := json.Marshal(value)
	return string(stable)
}

// refused is an InvalidArgument whose fixed message is also its safe
// classification (tools.ActivationRefusal).
func refused(msg string) error { return tools.RefuseActivation(msg, svcerr.InvalidArgument(msg)) }

// refusedPrecondition is refused for a FailedPrecondition.
func refusedPrecondition(msg string) error {
	return tools.RefuseActivation(msg, svcerr.FailedPrecondition(msg))
}

// PrepareActivation uses the same definition, setup, access and match-scope
// validation as the app. It writes no activation and grants no authority.
func (s *Service) PrepareActivation(ctx context.Context, user, operation string, p tools.ActivationParams) (tools.ActivationPreview, error) {
	_, preview, err := s.prepareAgentActivation(ctx, user, operation, p)
	return preview, err
}
func (s *Service) prepareAgentActivation(ctx context.Context, user, operation string, p tools.ActivationParams) (*pb.TriggerDefinition, tools.ActivationPreview, error) {
	ctx = context.WithValue(ctx, auth.UserIDContextKey, user)
	var existing *core.Trigger
	var err error
	def := &pb.TriggerDefinition{ActingIdentity: pb.ActingIdentity_ACTING_IDENTITY_OWNER}
	if operation != tools.ToolCreateTriggerActivation {
		if p.TriggerID == "" {
			return nil, tools.ActivationPreview{}, refused("trigger_id is required")
		}
		existing, err = s.ownedTrigger(ctx, p.TriggerID)
		if err != nil {
			return nil, tools.ActivationPreview{}, tools.RefuseActivation("activation not found", err)
		}
		if existing.WorkflowTrigger == nil {
			return nil, tools.ActivationPreview{}, refused("only declared workflow activations can be managed by these tools")
		}
		rendered, renderErr := triggers.ToProto(existing, nil, nil, nil)
		if renderErr != nil {
			return nil, tools.ActivationPreview{}, svcerr.Wrap(renderErr)
		}
		def = &pb.TriggerDefinition{Name: rendered.Name, ProjectId: rendered.ProjectId, WorktreeId: rendered.WorktreeId, Enabled: proto.Bool(rendered.Enabled), Workflow: rendered.Workflow, Presets: rendered.Presets, Params: rendered.Params, Message: rendered.Message, DaemonId: rendered.DaemonId, NoMachine: rendered.NoMachine, NotifyOnComplete: rendered.NotifyOnComplete, ConnectionId: rendered.ConnectionId, ActingIdentity: rendered.ActingIdentity, Source: &pb.TriggerDefinition_WorkflowTrigger{WorkflowTrigger: rendered.GetWorkflowTrigger()}}
	}
	if p.Name != "" {
		def.Name = p.Name
	}
	if p.Workflow != "" {
		def.Workflow = p.Workflow
	}
	if p.ProjectID != "" {
		def.ProjectId = p.ProjectID
	}
	if p.WorkflowTrigger != "" {
		def.Source = &pb.TriggerDefinition_WorkflowTrigger{WorkflowTrigger: p.WorkflowTrigger}
	}
	if def.GetWorkflowTrigger() == "" || def.Workflow == "" {
		return nil, tools.ActivationPreview{}, refused("workflow and workflow_trigger are required")
	}
	if p.MachineID != nil && *p.MachineID != "" && p.NoMachine != nil && *p.NoMachine {
		return nil, tools.ActivationPreview{}, refused("machine_id and no_machine are mutually exclusive")
	}
	if p.MachineID != nil {
		def.DaemonId = *p.MachineID
		if *p.MachineID != "" {
			def.NoMachine = false
		}
	}
	if p.NoMachine != nil {
		def.NoMachine = *p.NoMachine
		if *p.NoMachine {
			def.DaemonId = ""
		}
	}
	if p.ConnectionID != nil {
		def.ConnectionId = p.ConnectionID
	}
	if p.Message != nil {
		def.Message = *p.Message
	}
	if p.Params != nil {
		def.Params = map[string]*structpb.Value{}
		for k, v := range p.Params {
			value, e := structpb.NewValue(v)
			if e != nil {
				return nil, tools.ActivationPreview{}, tools.RefuseActivation("params."+k+" is not a JSON value", svcerr.InvalidArgument(e.Error()))
			}
			def.Params[k] = value
		}
	}
	if p.Presets != nil {
		def.Presets = p.Presets
	}
	if p.ActingIdentity != nil {
		switch *p.ActingIdentity {
		case "owner":
			def.ActingIdentity = pb.ActingIdentity_ACTING_IDENTITY_OWNER
		case "app":
			def.ActingIdentity = pb.ActingIdentity_ACTING_IDENTITY_APP
		default:
			return nil, tools.ActivationPreview{}, refused("acting_identity must be owner or app")
		}
	}
	switch operation {
	case tools.ToolEnableTrigger:
		def.Enabled = proto.Bool(true)
	case tools.ToolPauseTrigger:
		def.Enabled = proto.Bool(false)
	case tools.ToolCreateTriggerActivation:
		def.Enabled = proto.Bool(true)
	}
	// Pausing must remain possible even if the workflow was deleted or its
	// setup broke. Like the app's pause RPC, it needs ownership, not setup.
	if operation == tools.ToolPauseTrigger {
		if p.Name != "" || p.Workflow != "" || p.WorkflowTrigger != "" || p.ProjectID != "" || p.MachineID != nil || p.NoMachine != nil || p.ConnectionID != nil || p.Params != nil || p.Presets != nil || p.Message != nil || p.ActingIdentity != nil {
			return nil, tools.ActivationPreview{}, refused("pause_trigger only accepts trigger_id")
		}
		copy := *existing
		copy.ProjectName = ""
		copy.DaemonName = ""
		saved, _ := json.Marshal(copy) // memory-only digest material
		safe, _ := automationpolicy.SafeJSON(&copy)
		machineName := existing.DaemonName
		if machine, e := s.deps.Repo.GetDaemon(ctx, existing.DaemonID); e == nil && machine != nil {
			if machine.Name != "" {
				machineName = machine.Name
			}
		}
		title := fmt.Sprintf("Pause activation %s (%s). Owner: %s. Machine: %s (id %s). No future scheduled/event runs until enabled. Saved configuration: %s", "(name withheld)", existing.ID, user, machineName, existing.DaemonID, safe)
		hash := sha256.Sum256(append([]byte(title), saved...))
		revision := time.Time{}
		if existing != nil {
			revision = existing.UpdatedAt
		}
		if len(title) > automationpolicy.MaxDisplayBytes {
			return nil, tools.ActivationPreview{}, refusedPrecondition("Activation approval display size limit exceeded")
		}
		return def, tools.ActivationPreview{Title: title, Hash: fmt.Sprintf("%x", hash), Revision: revision}, nil
	}
	if operation == tools.ToolEnableTrigger && (p.Name != "" || p.Workflow != "" || p.WorkflowTrigger != "" || p.ProjectID != "" || p.MachineID != nil || p.NoMachine != nil || p.ConnectionID != nil || p.Params != nil || p.Presets != nil || p.Message != nil || p.ActingIdentity != nil) {
		return nil, tools.ActivationPreview{}, refused("enable_trigger only accepts trigger_id; use update_trigger_activation to edit")
	}
	if operation != tools.ToolCreateTriggerActivation && operation != tools.ToolUpdateTriggerActivation && operation != tools.ToolEnableTrigger {
		return nil, tools.ActivationPreview{}, refused("unknown activation operation")
	}
	row, err := s.triggerFromDefinition(ctx, user, def, existing)
	if err != nil {
		return nil, tools.ActivationPreview{}, tools.RefuseActivation("activation settings rejected ("+svcerr.Code(err).String()+")", err)
	}
	row.Enabled = def.Enabled == nil || *def.Enabled
	if operation != tools.ToolPauseTrigger {
		if err = s.refreshTriggerAccess(ctx, row); err != nil {
			return nil, tools.ActivationPreview{}, tools.RefuseActivation("integration access could not be refreshed ("+svcerr.Code(err).String()+")", err)
		}
	}
	wf, err := launch.ResolveValidatedRunWorkflow(ctx, s.deps.Repo, user, def.Workflow, def.ProjectId)
	if err != nil {
		return nil, tools.ActivationPreview{}, tools.RefuseActivation("workflow not found in this project, or it does not validate", err)
	}
	wt := triggers.FindDeclaredTrigger(wf, def.GetWorkflowTrigger())
	if wt == nil {
		return nil, tools.ActivationPreview{}, tools.RefuseActivation("workflow does not declare that trigger", svcerr.NotFound("declared trigger"))
	}
	if operation != tools.ToolPauseTrigger {
		_, err = s.checkReachability(ctx, user, wf, wt, &pb.EnableWorkflowRequest{ConnectionIds: map[string]string{wt.GetIntegration().GetIntegration(): def.GetConnectionId()}, ActingIdentity: def.ActingIdentity}, row)
		if err != nil {
			return nil, tools.ActivationPreview{}, tools.RefuseActivation("an integration input's values are not reachable with the connected account", err)
		}
	}
	// Include the full resolved graph: tools, permissions, sources and inputs.
	// A workflow or activation changed while waiting must be approved again.
	machineName := "No machine (server-only)"
	if !row.NoMachine {
		machine, e := s.deps.Repo.GetDaemon(ctx, row.DaemonID)
		if e != nil {
			return nil, tools.ActivationPreview{}, tools.RefuseActivation("machine not found", e)
		}
		machineName = machine.Name
		if machineName == "" && machine.Hostname != nil {
			machineName = *machine.Hostname
		}
	}
	// Resolve the default connection before approval, so a changed default
	// cannot silently redirect a previously approved activation.
	def.ConnectionId = row.ConnectionID
	closure, err := s.activationSnapshot(ctx, user, def, wf)
	if err != nil {
		return nil, tools.ActivationPreview{}, tools.RefuseActivation("workflow dependencies could not be resolved for approval ("+svcerr.Code(err).String()+")", err)
	}
	definition := canonicalProto(def)
	// Volatile names/health are not authorization inputs; configuration and its
	// revision are. Holding the saved revision in the hash detects an edit.
	var previous any
	if existing != nil {
		copy := *existing
		copy.ProjectName = ""
		copy.DaemonName = ""
		previous = struct {
			Definition *core.Trigger
			UpdatedAt  time.Time
		}{Definition: &copy, UpdatedAt: existing.UpdatedAt}
	}
	before, _ := json.Marshal(previous)
	// No raw configuration is ever a card title. Hash the full canonical
	// dependency closure in memory, display only the allowlisted projection.
	raw, err := json.Marshal(struct {
		Definition string
		Previous   string
		Closure    *activationSnapshot
	}{definition, string(before), closure})
	if err != nil || len(raw) > maxApprovalSnapshotBytes {
		return nil, tools.ActivationPreview{}, refusedPrecondition("Activation approval snapshot size limit exceeded")
	}
	safe, _ := automationpolicy.SafeJSON(row)
	connectionIdentity := "default/delegated at runtime (dynamic identity)"
	if c, ok := closure.Connection.(*core.Connection); ok && c != nil {
		connectionIdentity = fmt.Sprintf("id %s; integration %s; account %s", discloseScope(c.ID), discloseScope(c.IntegrationID), discloseScope(func() string {
			if c.ExternalAccountID != nil {
				return *c.ExternalAccountID
			}
			return "unknown"
		}()))
	}
	title := fmt.Sprintf("Machine: %s (id %s)\nOperation: %s\nRun as user: %s; integration identity: %s\nConnection identity: %s\nActivation: %s\n%s\n%s", machineName, row.DaemonID, operation, user, def.ActingIdentity, connectionIdentity, safe, sourceDisclosure(row), strings.Join(closure.Summary, "\n"))
	if len(title) > automationpolicy.MaxDisplayBytes {
		return nil, tools.ActivationPreview{}, refusedPrecondition("Activation approval display size limit exceeded")
	}
	hash := sha256.Sum256(append(raw, []byte(title)...))
	revision := time.Time{}
	if existing != nil {
		revision = existing.UpdatedAt
	}
	return def, tools.ActivationPreview{Title: title, Hash: fmt.Sprintf("%x", hash), Revision: revision}, nil
}

// ManageActivation always revalidates the preview before calling the app RPC.
func (s *Service) ManageActivation(ctx context.Context, user, operation string, p tools.ActivationParams, approvedHash string) (tools.TriggerSummary, error) {
	if automationpolicy.From(ctx).Unattended {
		return tools.TriggerSummary{}, svcerr.PermissionDenied(automationpolicy.Refusal())
	}
	// Serializable snapshot + dependency locks bind all app re-resolutions
	// to the approved dependency set. A row revision
	// check alone does not protect mutable workflows, presets or connections.
	var result tools.TriggerSummary
	var effects *activationEffects
	err := s.deps.Repo.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		project := p.ProjectID
		if p.TriggerID != "" {
			existing, err := s.ownedTrigger(context.WithValue(txCtx, auth.UserIDContextKey, user), p.TriggerID)
			if err != nil {
				return err
			}
			if project == "" {
				project = existing.ProjectID
			}
		}
		if err := s.deps.Repo.LockActivationDependencies(txCtx, user, project); err != nil {
			return svcerr.Wrap(err)
		}
		effects = &activationEffects{}
		txCtx = context.WithValue(txCtx, activationEffectsKey{}, effects)
		var err error
		result, err = s.manageActivationAtomic(txCtx, user, operation, p, approvedHash)
		return err
	})
	if err != nil {
		return tools.TriggerSummary{}, err
	}
	for _, effect := range effects.pending {
		if err := effect(ctx); err != nil {
			return tools.TriggerSummary{}, svcerr.FailedPrecondition("Activation configuration committed, but external reconciliation failed; inspect the activation before retrying")
		}
	}
	return result, nil
}

func (s *Service) manageActivationAtomic(ctx context.Context, user, operation string, p tools.ActivationParams, approvedHash string) (tools.TriggerSummary, error) {
	if approvedHash == "" {
		return tools.TriggerSummary{}, svcerr.PermissionDenied("An interactive approval is required for activation management")
	}
	def, preview, err := s.prepareAgentActivation(ctx, user, operation, p)
	if err != nil {
		return tools.TriggerSummary{}, err
	}
	if approvedHash != "" && approvedHash != preview.Hash {
		return tools.TriggerSummary{}, svcerr.FailedPrecondition("The activation or workflow changed after approval. Request a new approval; nothing was changed.")
	}
	ctx = context.WithValue(ctx, auth.UserIDContextKey, user)
	if !preview.Revision.IsZero() {
		ctx = automationpolicy.WithRevision(ctx, preview.Revision)
	}
	var out *pb.Trigger
	switch operation {
	case tools.ToolCreateTriggerActivation:
		response, e := s.CreateTrigger(ctx, connect.NewRequest(&pb.CreateTriggerRequest{Trigger: def}))
		err = e
		if e == nil {
			out = response.Msg.Trigger
		}
	case tools.ToolUpdateTriggerActivation:
		response, e := s.UpdateTrigger(ctx, connect.NewRequest(&pb.UpdateTriggerRequest{Id: p.TriggerID, Trigger: def}))
		err = e
		if e == nil {
			out = response.Msg.Trigger
		}
	case tools.ToolEnableTrigger, tools.ToolPauseTrigger:
		response, e := s.SetTriggerEnabled(ctx, connect.NewRequest(&pb.SetTriggerEnabledRequest{Id: p.TriggerID, Enabled: operation == tools.ToolEnableTrigger}))
		err = e
		if e == nil {
			out = response.Msg.Trigger
		}
	default:
		return tools.TriggerSummary{}, svcerr.InvalidArgument("unknown activation operation")
	}
	if err != nil {
		return tools.TriggerSummary{}, err
	}
	machineName := out.DaemonName
	if out.DaemonId != "" {
		if machine, e := s.deps.Repo.GetDaemon(ctx, out.DaemonId); e == nil {
			machineName = machine.Name
			if machineName == "" && machine.Hostname != nil {
				machineName = *machine.Hostname
			}
		}
	}
	return tools.TriggerSummary{ID: out.Id, Name: out.Name, Workflow: out.Workflow, WorkflowTrigger: out.GetWorkflowTrigger(), Enabled: out.Enabled, ProjectID: out.ProjectId, DaemonID: out.DaemonId, DaemonName: machineName, ConnectionID: out.GetConnectionId()}, nil
}
